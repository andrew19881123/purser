package registry_test

import (
	"context"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

func TestRecordAndListInferenceEvent(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	evt := &registry.InferenceEvent{
		RequestID:        "req-001",
		APIKeyHash:       "abc123",
		ModelID:          "qwen3-moe",
		TenantID:         "team-eng",
		Timestamp:        now,
		PromptTokens:     100,
		CompletionTokens: 50,
		Endpoint:         "openai",
		FinishReason:     "stop",
	}

	// Happy path: record succeeds.
	if err := reg.RecordInferenceEvent(ctx, evt); err != nil {
		t.Fatalf("RecordInferenceEvent: %v", err)
	}

	// Idempotency — duplicate request_id must be silently ignored (no error, still 1 row).
	if err := reg.RecordInferenceEvent(ctx, evt); err != nil {
		t.Fatalf("RecordInferenceEvent (duplicate): %v", err)
	}

	resp, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{Limit: 10})
	if err != nil {
		t.Fatalf("ListInferenceEvents: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("events = %d, want 1 (idempotent)", len(resp.Events))
	}
	got := resp.Events[0]
	if got.RequestID != "req-001" {
		t.Errorf("RequestID = %q, want req-001", got.RequestID)
	}
	if got.APIKeyHash != "abc123" {
		t.Errorf("APIKeyHash = %q, want abc123", got.APIKeyHash)
	}
	if got.ModelID != "qwen3-moe" {
		t.Errorf("ModelID = %q, want qwen3-moe", got.ModelID)
	}
	if got.TenantID != "team-eng" {
		t.Errorf("TenantID = %q, want team-eng", got.TenantID)
	}
	if got.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", got.PromptTokens)
	}
	if got.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", got.CompletionTokens)
	}
	if got.Endpoint != "openai" {
		t.Errorf("Endpoint = %q, want openai", got.Endpoint)
	}

	// Filter by api_key_hash — must return the matching event.
	resp2, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{APIKeyHash: "abc123", Limit: 10})
	if err != nil {
		t.Fatalf("ListInferenceEvents (by key): %v", err)
	}
	if len(resp2.Events) != 1 {
		t.Errorf("events by key = %d, want 1", len(resp2.Events))
	}

	// Filter by non-matching key — must return empty result.
	resp3, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{APIKeyHash: "nomatch", Limit: 10})
	if err != nil {
		t.Fatalf("ListInferenceEvents (no match): %v", err)
	}
	if len(resp3.Events) != 0 {
		t.Errorf("events by non-matching key = %d, want 0", len(resp3.Events))
	}
}

func TestRecordInferenceEventNilIsNoOp(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()
	if err := reg.RecordInferenceEvent(ctx, nil); err != nil {
		t.Fatalf("RecordInferenceEvent(nil): %v", err)
	}
}

func TestListInferenceEventsFilters(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []*registry.InferenceEvent{
		{RequestID: "r1", APIKeyHash: "key-a", ModelID: "model-x", TenantID: "t1", Timestamp: base.Add(1 * time.Hour), Endpoint: "openai"},
		{RequestID: "r2", APIKeyHash: "key-b", ModelID: "model-y", TenantID: "t2", Timestamp: base.Add(2 * time.Hour), Endpoint: "anthropic"},
		{RequestID: "r3", APIKeyHash: "key-a", ModelID: "model-x", TenantID: "t1", Timestamp: base.Add(3 * time.Hour), Endpoint: "openai"},
	}
	for _, e := range events {
		if err := reg.RecordInferenceEvent(ctx, e); err != nil {
			t.Fatalf("RecordInferenceEvent %q: %v", e.RequestID, err)
		}
	}

	// Filter by model_id.
	r, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{ModelID: "model-x", Limit: 10})
	if err != nil {
		t.Fatalf("list by model: %v", err)
	}
	if len(r.Events) != 2 {
		t.Errorf("model-x events = %d, want 2", len(r.Events))
	}

	// Filter by tenant_id.
	r, err = reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{TenantID: "t2", Limit: 10})
	if err != nil {
		t.Fatalf("list by tenant: %v", err)
	}
	if len(r.Events) != 1 {
		t.Errorf("t2 events = %d, want 1", len(r.Events))
	}

	// Filter by time range: After r1, Before r3 → should return only r2.
	r, err = reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{
		After:  base.Add(1 * time.Hour),
		Before: base.Add(3 * time.Hour),
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list by time range: %v", err)
	}
	if len(r.Events) != 1 || r.Events[0].RequestID != "r2" {
		t.Errorf("time-range events = %v, want [r2]", r.Events)
	}
}

func TestListInferenceEventsPagination(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()

	// Insert 5 events.
	for i := 0; i < 5; i++ {
		e := &registry.InferenceEvent{
			RequestID:  "page-req-" + string(rune('A'+i)),
			APIKeyHash: "key-pag",
			ModelID:    "mdl",
			Timestamp:  time.Now().UTC(),
		}
		if err := reg.RecordInferenceEvent(ctx, e); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Page 1: limit 2.
	resp, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(resp.Events) != 2 {
		t.Fatalf("page 1 len = %d, want 2", len(resp.Events))
	}
	if resp.NextPageToken == "" {
		t.Fatal("page 1: NextPageToken empty, want cursor")
	}

	// Page 2: limit 2.
	resp2, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{Limit: 2, PageToken: resp.NextPageToken})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(resp2.Events) != 2 {
		t.Fatalf("page 2 len = %d, want 2", len(resp2.Events))
	}

	// Page 3: limit 2, expect 1 row and no next token.
	resp3, err := reg.ListInferenceEvents(ctx, &registry.ListInferenceEventsRequest{Limit: 2, PageToken: resp2.NextPageToken})
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(resp3.Events) != 1 {
		t.Fatalf("page 3 len = %d, want 1", len(resp3.Events))
	}
	if resp3.NextPageToken != "" {
		t.Errorf("page 3 NextPageToken = %q, want empty (last page)", resp3.NextPageToken)
	}
}
