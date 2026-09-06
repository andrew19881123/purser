package registry_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// makeEvent returns a minimal InferenceEvent for chain tests.
func makeEvent(i int) *registry.InferenceEvent {
	return &registry.InferenceEvent{
		RequestID:    fmt.Sprintf("req-%d", i),
		APIKeyHash:   "abc123",
		ModelID:      "test-model",
		TenantID:     "team-test",
		Timestamp:    time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		Endpoint:     "openai",
		FinishReason: "stop",
	}
}

// TestInferenceChain_EmptyChain verifies that an empty log reports verified=true
// with length=0.
func TestInferenceChain_EmptyChain(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	length, verified, breakSeq, err := reg.VerifyInferenceChain(ctx)
	if err != nil {
		t.Fatalf("VerifyInferenceChain: %v", err)
	}
	if !verified {
		t.Errorf("empty chain: verified = false, want true")
	}
	if length != 0 {
		t.Errorf("empty chain: length = %d, want 0", length)
	}
	if breakSeq != -1 {
		t.Errorf("empty chain: breakSeq = %d, want -1", breakSeq)
	}
}

// TestInferenceChain_VerifyCleanChain records 5 events and confirms the chain
// verifies cleanly.
func TestInferenceChain_VerifyCleanChain(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	const n = 5
	for i := 0; i < n; i++ {
		if err := reg.RecordInferenceEvent(ctx, makeEvent(i)); err != nil {
			t.Fatalf("RecordInferenceEvent(%d): %v", i, err)
		}
	}

	length, verified, breakSeq, err := reg.VerifyInferenceChain(ctx)
	if err != nil {
		t.Fatalf("VerifyInferenceChain: %v", err)
	}
	if !verified {
		t.Errorf("clean chain: verified = false, breakSeq = %d", breakSeq)
	}
	if length != int64(n) {
		t.Errorf("clean chain: length = %d, want %d", length, n)
	}
	if breakSeq != -1 {
		t.Errorf("clean chain: breakSeq = %d, want -1", breakSeq)
	}
}

// TestInferenceChain_IdempotentDoesNotBreakChain confirms that submitting a
// duplicate request_id (INSERT OR IGNORE) does not create a spurious chain entry.
func TestInferenceChain_IdempotentDoesNotBreakChain(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	evt := makeEvent(0)
	for i := 0; i < 3; i++ {
		if err := reg.RecordInferenceEvent(ctx, evt); err != nil {
			t.Fatalf("RecordInferenceEvent (duplicate %d): %v", i, err)
		}
	}

	length, verified, _, err := reg.VerifyInferenceChain(ctx)
	if err != nil {
		t.Fatalf("VerifyInferenceChain: %v", err)
	}
	if !verified {
		t.Error("idempotent: chain should still be verified")
	}
	if length != 1 {
		t.Errorf("idempotent: length = %d, want 1 (duplicate was ignored)", length)
	}
}

// TestInferenceChain_DetectsTampering records events and then directly mutates
// one row's model_id, which changes the canonical bytes and thus the expected
// hash. VerifyInferenceChain should detect the break at the tampered seq.
func TestInferenceChain_DetectsTampering(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	const n = 5
	for i := 0; i < n; i++ {
		if err := reg.RecordInferenceEvent(ctx, makeEvent(i)); err != nil {
			t.Fatalf("RecordInferenceEvent(%d): %v", i, err)
		}
	}

	// Tamper with seq=3: change model_id so the canonical bytes no longer match the stored hash.
	sr := reg.(*registry.SQLiteRegistry)
	if _, err := sr.DB().Exec(
		"UPDATE inference_audit_log SET model_id = 'tampered-model' WHERE seq = 3",
	); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, verified, breakSeq, err := reg.VerifyInferenceChain(ctx)
	if err != nil {
		t.Fatalf("VerifyInferenceChain: %v", err)
	}
	if verified {
		t.Error("tampered chain: verified = true, want false")
	}
	if breakSeq != 3 {
		t.Errorf("tampered chain: breakSeq = %d, want 3", breakSeq)
	}
}

// TestInferenceChain_DetectsHashTampering mutates the stored hash directly (not
// the content). This simulates an attacker editing the hash column.
func TestInferenceChain_DetectsHashTampering(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	for i := 0; i < 3; i++ {
		if err := reg.RecordInferenceEvent(ctx, makeEvent(i)); err != nil {
			t.Fatalf("RecordInferenceEvent(%d): %v", i, err)
		}
	}

	sr := reg.(*registry.SQLiteRegistry)
	if _, err := sr.DB().Exec(
		"UPDATE inference_audit_log SET hash = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE seq = 2",
	); err != nil {
		t.Fatalf("tamper hash: %v", err)
	}

	_, verified, breakSeq, err := reg.VerifyInferenceChain(ctx)
	if err != nil {
		t.Fatalf("VerifyInferenceChain: %v", err)
	}
	if verified {
		t.Error("hash-tampered chain: verified = true, want false")
	}
	// seq=2 has a wrong hash; seq=3 will fail the prev_hash link check.
	if breakSeq != 2 && breakSeq != 3 {
		t.Errorf("hash-tampered chain: breakSeq = %d, want 2 or 3", breakSeq)
	}
}
