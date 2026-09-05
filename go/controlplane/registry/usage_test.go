package registry_test

import (
	"context"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

func TestRecordAndGetKeyUsage(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	// Seed an API key so the foreign-key join works in GetUsageSummary.
	if err := reg.CreateAPIKey(ctx, &registry.APIKey{
		ID: "k1", Name: "test", KeyHash: "deadbeef", Tenant: "acme", Quota: 1000, Enabled: true,
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// No records yet — all zeros.
	s, err := reg.GetKeyUsage(ctx, "k1")
	if err != nil {
		t.Fatalf("get key usage (empty): %v", err)
	}
	if s.APIKeyID != "k1" || s.TotalRequests != 0 || s.InputTokens != 0 || s.OutputTokens != 0 {
		t.Errorf("empty summary mismatch: %+v", s)
	}

	// Record two requests.
	if err := reg.RecordUsage(ctx, "k1", "llama-3-8b", 100, 50); err != nil {
		t.Fatalf("record usage 1: %v", err)
	}
	if err := reg.RecordUsage(ctx, "k1", "llama-3-8b", 200, 80); err != nil {
		t.Fatalf("record usage 2: %v", err)
	}

	s, err = reg.GetKeyUsage(ctx, "k1")
	if err != nil {
		t.Fatalf("get key usage after records: %v", err)
	}
	if s.APIKeyID != "k1" {
		t.Errorf("api_key_id = %q, want k1", s.APIKeyID)
	}
	if s.TotalRequests != 2 {
		t.Errorf("total_requests = %d, want 2", s.TotalRequests)
	}
	if s.InputTokens != 300 {
		t.Errorf("input_tokens = %d, want 300", s.InputTokens)
	}
	if s.OutputTokens != 130 {
		t.Errorf("output_tokens = %d, want 130", s.OutputTokens)
	}
}

func TestGetUsageSummaryGroupedByTenant(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	// Two tenants, two keys each.
	keys := []registry.APIKey{
		{ID: "k1", Name: "k1", KeyHash: "h1", Tenant: "acme", Enabled: true},
		{ID: "k2", Name: "k2", KeyHash: "h2", Tenant: "acme", Enabled: true},
		{ID: "k3", Name: "k3", KeyHash: "h3", Tenant: "beta", Enabled: true},
	}
	for _, k := range keys {
		k := k
		if err := reg.CreateAPIKey(ctx, &k); err != nil {
			t.Fatalf("create key %s: %v", k.ID, err)
		}
	}

	// acme/k1: 2 requests
	_ = reg.RecordUsage(ctx, "k1", "m1", 100, 40)
	_ = reg.RecordUsage(ctx, "k1", "m1", 200, 60)
	// acme/k2: 1 request
	_ = reg.RecordUsage(ctx, "k2", "m1", 50, 20)
	// beta/k3: 1 request
	_ = reg.RecordUsage(ctx, "k3", "m2", 300, 100)

	// All-time summary.
	tenants, err := reg.GetUsageSummary(ctx, time.Time{})
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("tenants count = %d, want 2; got %+v", len(tenants), tenants)
	}

	// Ordered by tenant name: acme < beta.
	acme := tenants[0]
	beta := tenants[1]
	if acme.Tenant != "acme" || beta.Tenant != "beta" {
		t.Fatalf("tenant names wrong: %q %q", acme.Tenant, beta.Tenant)
	}
	if acme.TotalRequests != 3 {
		t.Errorf("acme total_requests = %d, want 3", acme.TotalRequests)
	}
	if acme.InputTokens != 350 {
		t.Errorf("acme input_tokens = %d, want 350", acme.InputTokens)
	}
	if acme.OutputTokens != 120 {
		t.Errorf("acme output_tokens = %d, want 120", acme.OutputTokens)
	}
	if beta.TotalRequests != 1 || beta.InputTokens != 300 || beta.OutputTokens != 100 {
		t.Errorf("beta mismatch: %+v", beta)
	}
}

func TestGetUsageSummaryWithSinceFilter(t *testing.T) {
	ctx := context.Background()
	reg := openTemp(t)

	if err := reg.CreateAPIKey(ctx, &registry.APIKey{
		ID: "k1", Name: "k1", KeyHash: "h1", Tenant: "acme", Enabled: true,
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Record a usage now, then note the time, then record another.
	_ = reg.RecordUsage(ctx, "k1", "m1", 100, 40)
	mark := time.Now().UTC()
	// Small sleep to ensure the second record is strictly after mark.
	time.Sleep(10 * time.Millisecond)
	_ = reg.RecordUsage(ctx, "k1", "m1", 200, 60)

	// Since mark: only the second request should appear.
	tenants, err := reg.GetUsageSummary(ctx, mark)
	if err != nil {
		t.Fatalf("get summary with since: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("tenants = %d, want 1", len(tenants))
	}
	if tenants[0].TotalRequests != 1 {
		t.Errorf("total_requests = %d, want 1", tenants[0].TotalRequests)
	}
	if tenants[0].InputTokens != 200 {
		t.Errorf("input_tokens = %d, want 200", tenants[0].InputTokens)
	}
}
