package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultInferenceLimit = 100
const maxInferenceLimit = 1000

// inferenceFirstSeq is the seq assigned to the genesis entry of the inference
// audit log chain.
const inferenceFirstSeq int64 = 1

// inferenceGenesisPrevHash is the PrevHash of the first entry: 64 hex zeros
// (32 all-zero bytes), matching the convention used by the admin audit chain.
const inferenceGenesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// canonicalInferenceEventBytes returns the deterministic byte encoding for an
// inference event entry in the hash chain. The encoding is:
//
//	rawBytes(prevHash) — 32 bytes (genesis: all zeros)
//	seq                — int64 big-endian
//	request_id         — length-prefixed (4-byte BE uint32 + string)
//	api_key_hash       — length-prefixed
//	model_id           — length-prefixed
//	tenant_id          — length-prefixed
//	timestamp          — RFC3339 formatted, length-prefixed
//	endpoint           — length-prefixed
//	finish_reason      — length-prefixed
//
// latency_ms and client_ip_prefix are deliberately excluded: latency_ms can
// vary across retries and client_ip_prefix is GDPR-sensitive. Excluding them
// keeps the canonical form stable and compliant.
func canonicalInferenceEventBytes(e *InferenceEvent, seq int64, prevHash string) []byte {
	var buf bytes.Buffer
	writeStr := func(s string) {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(s)))
		buf.Write(l)
		buf.WriteString(s)
	}
	writePrevHash := func(h string) {
		b, _ := hex.DecodeString(h)
		if len(b) == 0 {
			b = make([]byte, 32) // genesis or empty: all zeros
		}
		buf.Write(b)
	}
	writePrevHash(prevHash)
	binary.Write(&buf, binary.BigEndian, seq) //nolint:errcheck // bytes.Buffer never errors
	writeStr(e.RequestID)
	writeStr(e.APIKeyHash)
	writeStr(e.ModelID)
	writeStr(e.TenantID)
	writeStr(e.Timestamp.Format(time.RFC3339))
	writeStr(e.Endpoint)
	writeStr(e.FinishReason)
	return buf.Bytes()
}

// RecordInferenceEvent appends one event to the inference_audit_log table and
// extends the tamper-evident hash chain. INSERT OR IGNORE on the UNIQUE
// request_id column makes the call idempotent: a duplicate submission is
// silently discarded and nil is returned. A nil event is accepted as a no-op.
//
// The hash chain fields (seq, prev_hash, hash) are assigned under inferenceAuditMu
// so concurrent writers always receive a monotonic, gap-free seq and the correct
// prev_hash.
func (r *SQLiteRegistry) RecordInferenceEvent(ctx context.Context, event *InferenceEvent) error {
	if event == nil {
		return nil
	}
	ts := event.Timestamp
	if ts.IsZero() {
		ts = nowUTC()
	}
	// Use a canonical copy with the finalised timestamp so canonicalInferenceEventBytes
	// hashes exactly the value that will be persisted.
	ev := *event
	ev.Timestamp = ts

	r.inferenceAuditMu.Lock()
	defer r.inferenceAuditMu.Unlock()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("registry: record inference event %q: begin: %w", event.RequestID, err)
	}
	defer tx.Rollback()

	// Read the current chain tail.
	var lastSeq sql.NullInt64
	var lastHash sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT seq, hash FROM inference_audit_log
		WHERE seq IS NOT NULL
		ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &lastHash)

	var seq int64
	var prevHash string
	switch {
	case err == sql.ErrNoRows:
		seq = inferenceFirstSeq
		prevHash = inferenceGenesisPrevHash
	case err != nil:
		return fmt.Errorf("registry: record inference event %q: read tail: %w", event.RequestID, err)
	default:
		seq = lastSeq.Int64 + 1
		prevHash = lastHash.String
	}

	// Compute the chain hash for this entry.
	raw := canonicalInferenceEventBytes(&ev, seq, prevHash)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	_, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO inference_audit_log
			(request_id, api_key_hash, model_id, tenant_id, timestamp,
			 prompt_tokens, completion_tokens, endpoint, client_ip_prefix,
			 latency_ms, finish_reason, seq, prev_hash, hash,
			 model_revision, model_quantization, node_id, inference_engine)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.RequestID,
		ev.APIKeyHash,
		ev.ModelID,
		ev.TenantID,
		fmtTime(ev.Timestamp),
		ev.PromptTokens,
		ev.CompletionTokens,
		ev.Endpoint,
		ev.ClientIPPrefix,
		ev.LatencyMs,
		ev.FinishReason,
		seq, prevHash, hash,
		ev.ModelRevision,
		ev.ModelQuantization,
		ev.NodeID,
		ev.InferenceEngine,
	)
	if err != nil {
		return fmt.Errorf("registry: record inference event %q: %w", event.RequestID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("registry: record inference event %q: commit: %w", event.RequestID, err)
	}
	return nil
}

// ListInferenceEvents returns paginated inference audit events that match the
// filter in req. All filter fields are optional (zero value = no filter).
// Rows are ordered by id ascending. The PageToken is an opaque decimal row-id
// cursor; NextPageToken is empty when there are no further pages.
func (r *SQLiteRegistry) ListInferenceEvents(ctx context.Context, req *ListInferenceEventsRequest) (*ListInferenceEventsResponse, error) {
	if req == nil {
		req = &ListInferenceEventsRequest{}
	}

	limit := int(req.Limit)
	switch {
	case limit <= 0:
		limit = defaultInferenceLimit
	case limit > maxInferenceLimit:
		limit = maxInferenceLimit
	}

	// Build WHERE clauses dynamically.
	var clauses []string
	var args []any

	// Cursor-based pagination: id > page_token (decimal row id).
	if req.PageToken != "" {
		cursorID, err := strconv.ParseInt(req.PageToken, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("registry: list inference events: invalid page_token %q: %w", req.PageToken, err)
		}
		clauses = append(clauses, "id > ?")
		args = append(args, cursorID)
	}
	if req.APIKeyHash != "" {
		clauses = append(clauses, "api_key_hash = ?")
		args = append(args, req.APIKeyHash)
	}
	if req.ModelID != "" {
		clauses = append(clauses, "model_id = ?")
		args = append(args, req.ModelID)
	}
	if req.TenantID != "" {
		clauses = append(clauses, "tenant_id = ?")
		args = append(args, req.TenantID)
	}
	if !req.After.IsZero() {
		clauses = append(clauses, "timestamp > ?")
		args = append(args, fmtTime(req.After))
	}
	if !req.Before.IsZero() {
		clauses = append(clauses, "timestamp < ?")
		args = append(args, fmtTime(req.Before))
	}

	query := `SELECT id, request_id, api_key_hash, model_id, tenant_id, timestamp,
		prompt_tokens, completion_tokens, endpoint, client_ip_prefix,
		latency_ms, finish_reason,
		model_revision, model_quantization, node_id, inference_engine
		FROM inference_audit_log`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	// Fetch one extra row to detect whether a next page exists.
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("registry: list inference events: %w", err)
	}
	defer rows.Close()

	var events []*InferenceEvent
	for rows.Next() {
		e, err := scanInferenceEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list inference events: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: list inference events: %w", err)
	}

	resp := &ListInferenceEventsResponse{}
	if len(events) > limit {
		// There is at least one more page: trim the sentinel row and set the cursor.
		resp.NextPageToken = strconv.FormatInt(events[limit-1].ID, 10)
		resp.Events = events[:limit]
	} else {
		resp.Events = events
	}
	return resp, nil
}

// scanInferenceEvent reads one row from the inference_audit_log SELECT.
func scanInferenceEvent(s interface{ Scan(...any) error }) (*InferenceEvent, error) {
	var (
		e  InferenceEvent
		ts sql.NullString
	)
	if err := s.Scan(
		&e.ID, &e.RequestID, &e.APIKeyHash, &e.ModelID, &e.TenantID, &ts,
		&e.PromptTokens, &e.CompletionTokens, &e.Endpoint, &e.ClientIPPrefix,
		&e.LatencyMs, &e.FinishReason,
		&e.ModelRevision, &e.ModelQuantization, &e.NodeID, &e.InferenceEngine,
	); err != nil {
		return nil, err
	}
	e.Timestamp = parseTime(ts)
	return &e, nil
}
