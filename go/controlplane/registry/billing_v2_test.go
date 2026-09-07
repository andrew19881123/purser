package registry_test

import (
	"context"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// seedEvent inserts one inference_audit_log row via RecordInferenceEvent.
func seedEvent(t *testing.T, reg registry.Registry, tenantID, modelID string, prompt, completion int64) {
	t.Helper()
	// Use a unique request_id derived from parameters + timestamp to avoid
	// collisions when calling seedEvent multiple times in a single test.
	id := tenantID + "-" + modelID + "-" + time.Now().String()
	err := reg.RecordInferenceEvent(context.Background(), &registry.InferenceEvent{
		RequestID:        id,
		APIKeyHash:       "testhash",
		ModelID:          modelID,
		TenantID:         tenantID,
		Timestamp:        time.Now().UTC(),
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMs:        100,
		FinishReason:     "stop",
	})
	if err != nil {
		t.Fatalf("seedEvent(%q, %q): %v", tenantID, modelID, err)
	}
}

// TestGetTeamBillingReport_EmptyPeriod verifies that a query over an empty log
// returns a zero-value report — not an error.
func TestGetTeamBillingReport_EmptyPeriod(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()

	report, err := reg.GetTeamBillingReport(ctx, "team-x", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("GetTeamBillingReport (empty): %v", err)
	}
	if report.TotalRequests != 0 {
		t.Errorf("total_requests = %d, want 0", report.TotalRequests)
	}
	if report.TotalTokens != 0 {
		t.Errorf("total_tokens = %d, want 0", report.TotalTokens)
	}
	if report.TotalCostUSD != 0 {
		t.Errorf("total_cost_usd = %f, want 0", report.TotalCostUSD)
	}
	if len(report.ByModel) != 0 {
		t.Errorf("by_model len = %d, want 0", len(report.ByModel))
	}
	if report.TeamID != "team-x" {
		t.Errorf("team_id = %q, want team-x", report.TeamID)
	}
}

// TestGetTeamBillingReport_WithData verifies that the report correctly groups
// by model and aggregates token counts.
func TestGetTeamBillingReport_WithData(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Two requests on model-a from the same team.
	seedEvent(t, reg, "team-eng", "model-a", 100, 50)
	seedEvent(t, reg, "team-eng", "model-a", 200, 80)
	// One request on model-b from the same team.
	seedEvent(t, reg, "team-eng", "model-b", 300, 120)
	// Noise: different team — must NOT appear in the report.
	seedEvent(t, reg, "team-fin", "model-a", 999, 999)

	report, err := reg.GetTeamBillingReport(ctx, "team-eng", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("GetTeamBillingReport: %v", err)
	}

	if report.TotalRequests != 3 {
		t.Errorf("total_requests = %d, want 3", report.TotalRequests)
	}
	// input: 100+200+300 = 600; output: 50+80+120 = 250; total = 850
	if report.InputTokens != 600 {
		t.Errorf("input_tokens = %d, want 600", report.InputTokens)
	}
	if report.OutputTokens != 250 {
		t.Errorf("output_tokens = %d, want 250", report.OutputTokens)
	}
	if report.TotalTokens != 850 {
		t.Errorf("total_tokens = %d, want 850", report.TotalTokens)
	}
	if len(report.ByModel) != 2 {
		t.Fatalf("by_model len = %d, want 2; %+v", len(report.ByModel), report.ByModel)
	}

	// model-a has more tokens (430 vs 420) so it should be first (ORDER BY total_tokens DESC).
	modelA := report.ByModel[0]
	if modelA.ModelID != "model-a" {
		t.Errorf("ByModel[0].ModelID = %q, want model-a", modelA.ModelID)
	}
	if modelA.RequestCount != 2 {
		t.Errorf("model-a request_count = %d, want 2", modelA.RequestCount)
	}
	if modelA.PromptTokens != 300 {
		t.Errorf("model-a prompt_tokens = %d, want 300", modelA.PromptTokens)
	}
	if modelA.CompletionTokens != 130 {
		t.Errorf("model-a completion_tokens = %d, want 130", modelA.CompletionTokens)
	}
}

// TestGetOrgBillingReport_SumsTeams verifies that GetOrgBillingReport discovers
// teams by the "<orgID>/<teamSlug>" naming convention and sums their totals.
func TestGetOrgBillingReport_SumsTeams(t *testing.T) {
	reg := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed two teams under "acme" org, using the naming convention.
	seedEvent(t, reg, "acme/eng", "model-a", 100, 50)  // 150 tokens
	seedEvent(t, reg, "acme/eng", "model-a", 200, 80)  // 280 tokens → eng total: 430
	seedEvent(t, reg, "acme/fin", "model-b", 300, 120) // 420 tokens → fin total: 420
	// Noise: different org — must NOT appear.
	seedEvent(t, reg, "other-org/team", "model-a", 500, 200)

	report, err := reg.GetOrgBillingReport(ctx, "acme", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("GetOrgBillingReport: %v", err)
	}

	if report.OrgID != "acme" {
		t.Errorf("org_id = %q, want acme", report.OrgID)
	}
	if len(report.Teams) != 2 {
		t.Fatalf("teams len = %d, want 2; %+v", len(report.Teams), report.Teams)
	}
	// Total tokens across both teams: 430 + 420 = 850.
	if report.TotalTokens != 850 {
		t.Errorf("total_tokens = %d, want 850", report.TotalTokens)
	}
	// Verify OrgID is propagated to each team report.
	for _, tr := range report.Teams {
		if tr.OrgID != "acme" {
			t.Errorf("team %q org_id = %q, want acme", tr.TeamID, tr.OrgID)
		}
	}
	// Verify that the noise team is absent.
	for _, tr := range report.Teams {
		if tr.TeamID == "other-org/team" {
			t.Errorf("unexpected team in org report: %q", tr.TeamID)
		}
	}
}
