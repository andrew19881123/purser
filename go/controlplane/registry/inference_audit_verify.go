package registry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// VerifyInferenceChain walks inference_audit_log in seq order and checks that
// each entry's stored hash matches the value recomputed from its content and
// the previous entry's hash (the chain rule).
//
// It returns:
//
//	length    — number of chained rows examined (rows with a non-NULL seq)
//	verified  — true when every entry is consistent
//	breakSeq  — the seq value of the first inconsistent entry (-1 when verified=true)
//	err       — a database or scan error (distinct from a chain failure)
//
// Three failure modes are detected:
//
//  1. Sequence gap: seq is not exactly prevSeq+1 (indicates deletion or insertion).
//  2. Hash mismatch: recomputed hash != stored hash (indicates content tampering).
//  3. Chain link broken: storedPrevHash != the previous entry's hash.
func (r *SQLiteRegistry) VerifyInferenceChain(ctx context.Context) (length int64, verified bool, breakSeq int64, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT seq, request_id, api_key_hash, model_id, tenant_id, timestamp,
		       endpoint, finish_reason, prev_hash, hash
		FROM inference_audit_log
		WHERE seq IS NOT NULL
		ORDER BY seq ASC`)
	if err != nil {
		return 0, false, -1, fmt.Errorf("registry: verify inference chain: %w", err)
	}
	defer rows.Close()

	prevHash := inferenceGenesisPrevHash
	var prevSeq int64 = -1

	for rows.Next() {
		var (
			seq            int64
			storedPrevHash sql.NullString
			storedHash     sql.NullString
			event          InferenceEvent
			ts             sql.NullString
		)

		if err := rows.Scan(
			&seq, &event.RequestID, &event.APIKeyHash, &event.ModelID,
			&event.TenantID, &ts, &event.Endpoint,
			&event.FinishReason, &storedPrevHash, &storedHash,
		); err != nil {
			return length, false, seq, fmt.Errorf("registry: verify inference chain: scan: %w", err)
		}
		event.Timestamp = parseTime(ts)

		// Check sequence continuity.
		if prevSeq >= 0 && seq != prevSeq+1 {
			return length, false, seq, nil
		}
		// Check that the genesis entry starts at inferenceFirstSeq.
		if prevSeq < 0 && seq != inferenceFirstSeq {
			return length, false, seq, nil
		}

		// Check chain link: storedPrevHash must equal the previous entry's hash
		// (or the genesis constant for the first entry).
		if storedPrevHash.String != prevHash {
			return length, false, seq, nil
		}

		// Recompute the hash and compare with the stored value.
		raw := canonicalInferenceEventBytes(&event, seq, storedPrevHash.String)
		sum := sha256.Sum256(raw)
		expected := hex.EncodeToString(sum[:])

		if expected != storedHash.String {
			return length, false, seq, nil
		}

		prevHash = storedHash.String
		prevSeq = seq
		length++
	}

	if err := rows.Err(); err != nil {
		return length, false, -1, fmt.Errorf("registry: verify inference chain: rows: %w", err)
	}
	return length, true, -1, nil
}
