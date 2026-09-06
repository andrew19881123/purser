package registry

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

const defaultInferenceLimit = 100
const maxInferenceLimit = 1000

// RecordInferenceEvent appends one event to the inference_audit_log table.
// INSERT OR IGNORE on the UNIQUE request_id column makes the call idempotent:
// a duplicate submission is silently discarded and nil is returned.
// A nil event is accepted as a no-op.
func (r *SQLiteRegistry) RecordInferenceEvent(ctx context.Context, event *InferenceEvent) error {
	if event == nil {
		return nil
	}
	ts := event.Timestamp
	if ts.IsZero() {
		ts = nowUTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO inference_audit_log
			(request_id, api_key_hash, model_id, tenant_id, timestamp,
			 prompt_tokens, completion_tokens, endpoint, client_ip_prefix,
			 latency_ms, finish_reason,
			 model_revision, model_quantization, node_id, inference_engine)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID,
		event.APIKeyHash,
		event.ModelID,
		event.TenantID,
		fmtTime(ts),
		event.PromptTokens,
		event.CompletionTokens,
		event.Endpoint,
		event.ClientIPPrefix,
		event.LatencyMs,
		event.FinishReason,
		event.ModelRevision,
		event.ModelQuantization,
		event.NodeID,
		event.InferenceEngine,
	)
	if err != nil {
		return fmt.Errorf("registry: record inference event %q: %w", event.RequestID, err)
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
