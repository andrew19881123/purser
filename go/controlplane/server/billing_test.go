package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newBillingLicense returns a license with the "billing" feature enabled.
func newBillingLicense(t *testing.T) *license.License {
	t.Helper()
	now := time.Now().UTC()
	return signedLicense(t, license.Payload{
		Licensee: "Billing Test Corp",
		Features: []string{"billing"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
}

// seedInferenceEvent inserts one row into inference_audit_log via the server's
// POST /api/v1/inference-events endpoint (open when InternalToken is empty).
func seedInferenceEvent(t *testing.T, h http.Handler, tenantID, modelID string, prompt, completion int64, latencyMs float64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"request_id":        tenantID + "-" + modelID + "-" + strings.ReplaceAll(time.Now().String(), " ", ""),
		"tenant_id":         tenantID,
		"model_id":          modelID,
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"latency_ms":        latencyMs,
		"finish_reason":     "stop",
		"timestamp":         time.Now().UTC().Format(time.RFC3339Nano),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inference-events", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("seed inference event: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestBillingReport_NoLicense checks that GET /api/v1/billing/report returns
// 402 Payment Required when no enterprise license is loaded.
func TestBillingReport_NoLicense(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
}

// TestBillingReport_Licensed_Empty checks that a valid enterprise response is
// returned even when the inference_audit_log is empty (zero-value report).
func TestBillingReport_Licensed_Empty(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var report registry.BillingReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if len(report.Tenants) != 0 {
		t.Errorf("tenants = %d, want 0 (empty log)", len(report.Tenants))
	}
	if report.TotalRequests != 0 {
		t.Errorf("total_requests = %d, want 0", report.TotalRequests)
	}
}

// TestBillingReport_WithData verifies that the report groups correctly by
// tenant+model and computes correct aggregate token counts.
func TestBillingReport_WithData(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	h := srv.Handler()

	// Seed: 2 requests from team-eng on model-a, 1 from team-fin on model-b.
	seedInferenceEvent(t, h, "team-eng", "model-a", 100, 50, 200.0)
	seedInferenceEvent(t, h, "team-eng", "model-a", 200, 80, 150.0)
	seedInferenceEvent(t, h, "team-fin", "model-b", 300, 120, 300.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var report registry.BillingReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	// Should have two tenant+model rows.
	if len(report.Tenants) != 2 {
		t.Fatalf("tenants = %d, want 2; report=%+v", len(report.Tenants), report)
	}
	if report.TotalRequests != 3 {
		t.Errorf("total_requests = %d, want 3", report.TotalRequests)
	}

	// Find team-eng row (should have 2 requests).
	var engRow *registry.BillingTenantUsage
	for i := range report.Tenants {
		if report.Tenants[i].TenantID == "team-eng" {
			engRow = &report.Tenants[i]
		}
	}
	if engRow == nil {
		t.Fatalf("team-eng not found in report tenants")
	}
	if engRow.RequestCount != 2 {
		t.Errorf("team-eng request_count = %d, want 2", engRow.RequestCount)
	}
	if engRow.PromptTokens != 300 {
		t.Errorf("team-eng prompt_tokens = %d, want 300", engRow.PromptTokens)
	}
	if engRow.CompletionTokens != 130 {
		t.Errorf("team-eng completion_tokens = %d, want 130", engRow.CompletionTokens)
	}
}

// TestBillingReport_CSV verifies that ?format=csv returns text/csv with the
// correct Content-Type and at least the header row.
func TestBillingReport_CSV(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	seedInferenceEvent(t, srv.Handler(), "team-eng", "model-a", 100, 50, 100.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?format=csv", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "tenant_id") {
		t.Errorf("CSV missing header row; body=%q", body)
	}
	if !strings.Contains(body, "team-eng") {
		t.Errorf("CSV missing data row; body=%q", body)
	}
}

// TestBillingSummary_NoLicense checks that the summary endpoint (not
// enterprise-gated) is accessible without a license.
func TestBillingSummary_NoLicense(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/summary", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["total_requests"]; !ok {
		t.Errorf("response missing total_requests field; body=%v", body)
	}
}

// TestBillingReport_TenantFilter verifies that ?tenant_id filters correctly.
func TestBillingReport_TenantFilter(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	seedInferenceEvent(t, h, "team-eng", "model-a", 100, 50, 100.0)
	seedInferenceEvent(t, h, "team-fin", "model-b", 200, 80, 120.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?tenant_id=team-eng", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var report registry.BillingReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if len(report.Tenants) != 1 {
		t.Fatalf("tenants = %d, want 1 (filtered to team-eng)", len(report.Tenants))
	}
	if report.Tenants[0].TenantID != "team-eng" {
		t.Errorf("tenant_id = %q, want team-eng", report.Tenants[0].TenantID)
	}
}

// TestBillingReport_BadStartParam verifies that a malformed start param yields 400.
func TestBillingReport_BadStartParam(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?start=not-a-date", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetBillingReport_Registry tests the registry method directly (not via
// HTTP) to verify SQL grouping logic.
func TestGetBillingReport_Registry(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()
	now := time.Now().UTC()

	events := []*registry.InferenceEvent{
		{RequestID: "r1", TenantID: "a", ModelID: "m1", PromptTokens: 100, CompletionTokens: 50, LatencyMs: 200, Timestamp: now, FinishReason: "stop"},
		{RequestID: "r2", TenantID: "a", ModelID: "m1", PromptTokens: 200, CompletionTokens: 80, LatencyMs: 100, Timestamp: now, FinishReason: "stop"},
		{RequestID: "r3", TenantID: "b", ModelID: "m2", PromptTokens: 400, CompletionTokens: 200, LatencyMs: 300, Timestamp: now, FinishReason: "stop"},
	}
	for _, e := range events {
		if err := reg.RecordInferenceEvent(ctx, e); err != nil {
			t.Fatalf("record event: %v", err)
		}
	}

	start := now.Add(-time.Minute)
	end := now.Add(time.Minute)
	report, err := reg.GetBillingReport(ctx, start, end, "")
	if err != nil {
		t.Fatalf("GetBillingReport: %v", err)
	}

	if len(report.Tenants) != 2 {
		t.Fatalf("tenants = %d, want 2", len(report.Tenants))
	}
	if report.TotalRequests != 3 {
		t.Errorf("total_requests = %d, want 3", report.TotalRequests)
	}
	// Total tokens: (100+50)+(200+80)+(400+200) = 1030
	if report.TotalTokens != 1030 {
		t.Errorf("total_tokens = %d, want 1030", report.TotalTokens)
	}
}
