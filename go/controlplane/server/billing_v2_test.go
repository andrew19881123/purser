package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newBillingV2License returns an enterprise license with the "billing" feature.
func newBillingV2License(t *testing.T) *license.License {
	t.Helper()
	now := time.Now().UTC()
	return signedLicense(t, license.Payload{
		Licensee: "Billing V2 Test Corp",
		Features: []string{"billing"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
}

// TestHandleTeamBillingReport_EnterpriseGated verifies that the team billing
// endpoint returns 402 Payment Required when no enterprise license is present.
func TestHandleTeamBillingReport_EnterpriseGated(t *testing.T) {
	// No license — community edition.
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/teams/team-eng/billing", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	var errBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
}

// TestHandleOrgBillingReport_EnterpriseGated verifies that the org billing
// endpoint returns 402 Payment Required without a valid billing license.
func TestHandleOrgBillingReport_EnterpriseGated(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/orgs/acme/billing", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleOrgBillingReport_DefaultWindow verifies that the org billing
// endpoint returns 200 OK with a valid license, using the default 30-day
// window when no start/end query parameters are supplied.
func TestHandleOrgBillingReport_DefaultWindow(t *testing.T) {
	reg := newReg(t)
	lic := newBillingV2License(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	// Seed an event for a team under the "acme" org.
	seedInferenceEvent(t, h, "acme/eng", "model-a", 100, 50, 120.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/orgs/acme/billing", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var report registry.OrgBillingReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v; raw=%s", err, rec.Body.String())
	}
	if report.OrgID != "acme" {
		t.Errorf("org_id = %q, want acme", report.OrgID)
	}
	if len(report.Teams) != 1 {
		t.Fatalf("teams len = %d, want 1; %+v", len(report.Teams), report.Teams)
	}
	if report.Teams[0].TeamID != "acme/eng" {
		t.Errorf("team_id = %q, want acme/eng", report.Teams[0].TeamID)
	}
	// period_start and period_end must be present (default window = 30 days back).
	if report.PeriodStart.IsZero() {
		t.Error("period_start is zero")
	}
	if report.PeriodEnd.IsZero() {
		t.Error("period_end is zero")
	}
}

// TestHandleTeamBillingReport_WithData verifies that the team endpoint returns
// correct totals when inference events exist for the queried team.
func TestHandleTeamBillingReport_WithData(t *testing.T) {
	reg := newReg(t)
	lic := newBillingV2License(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	// Two requests from team-a, one from team-b (noise).
	seedInferenceEvent(t, h, "team-a", "model-x", 200, 100, 150.0)
	seedInferenceEvent(t, h, "team-a", "model-x", 300, 120, 200.0)
	seedInferenceEvent(t, h, "team-b", "model-x", 999, 999, 100.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/teams/team-a/billing", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var report registry.TeamBillingReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if report.TeamID != "team-a" {
		t.Errorf("team_id = %q, want team-a", report.TeamID)
	}
	if report.TotalRequests != 2 {
		t.Errorf("total_requests = %d, want 2", report.TotalRequests)
	}
	// input: 200+300 = 500; output: 100+120 = 220; total = 720
	if report.TotalTokens != 720 {
		t.Errorf("total_tokens = %d, want 720", report.TotalTokens)
	}
	if len(report.ByModel) != 1 {
		t.Errorf("by_model len = %d, want 1", len(report.ByModel))
	}
}
