package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// seedInferenceEventLatency inserts one inference event with an explicit latency.
func seedInferenceEventLatency(t *testing.T, h http.Handler, tenantID, modelID string, latencyMs float64) {
	t.Helper()
	// Use a unique request ID derived from the nano timestamp to avoid collisions.
	seedInferenceEvent(t, h, tenantID, modelID, 100, 50, latencyMs)
}

// sloComplianceResponse is the top-level shape of GET /api/v1/slo/compliance.
type sloComplianceResponse struct {
	WindowHours int              `json:"window_hours"`
	GeneratedAt time.Time        `json:"generated_at"`
	Models      []sloModelResult `json:"models"`
}

type sloModelResult struct {
	ModelID string            `json:"model_id"`
	SLO     sloContractResult `json:"slo"`
	Actual  sloActualResult   `json:"actual"`
	Status  string            `json:"status"`
}

type sloContractResult struct {
	TTFTMs           int     `json:"ttft_ms"`
	TBTMs            int     `json:"tbt_ms"`
	TargetCompliance float64 `json:"target_compliance"`
}

type sloActualResult struct {
	TTFTCompliance *float64  `json:"ttft_compliance"`
	TBTCompliance  *float64  `json:"tbt_compliance"`
	RequestCount   int64     `json:"request_count"`
	PeriodStart    time.Time `json:"period_start"`
}

// doSLOCompliance performs GET /api/v1/slo/compliance and decodes the response.
func doSLOCompliance(t *testing.T, h http.Handler, query string) (sloComplianceResponse, int) {
	t.Helper()
	path := "/api/v1/slo/compliance"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		var resp sloComplianceResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode slo/compliance: %v; raw=%s", err, rec.Body.String())
		}
		return resp, rec.Code
	}
	return sloComplianceResponse{}, rec.Code
}

// findModel returns the sloModelResult for modelID or fails the test.
func findModel(t *testing.T, resp sloComplianceResponse, modelID string) sloModelResult {
	t.Helper()
	for _, m := range resp.Models {
		if m.ModelID == modelID {
			return m
		}
	}
	t.Fatalf("model %q not found in response; got %+v", modelID, resp.Models)
	return sloModelResult{}
}

// TestSLOCompliance_Met seeds enough compliant requests (latency < 2000ms) so
// that the compliance rate exceeds the 0.95 target. Expects status "met".
func TestSLOCompliance_Met(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// Seed 20 compliant requests (latency 100ms) for model "fast-model".
	for i := 0; i < 20; i++ {
		seedInferenceEventLatency(t, h, "team-a", "fast-model", 100.0)
	}

	resp, code := doSLOCompliance(t, h, "model_id=fast-model")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.WindowHours != 24 {
		t.Errorf("window_hours = %d, want 24", resp.WindowHours)
	}
	m := findModel(t, resp, "fast-model")

	if m.Status != "met" {
		t.Errorf("status = %q, want met; actual=%+v", m.Status, m.Actual)
	}
	if m.Actual.TTFTCompliance == nil {
		t.Fatal("ttft_compliance is nil, want a value")
	}
	if *m.Actual.TTFTCompliance < 0.95 {
		t.Errorf("ttft_compliance = %f, want >= 0.95", *m.Actual.TTFTCompliance)
	}
	if m.Actual.TBTCompliance != nil {
		t.Errorf("tbt_compliance = %v, want nil (not in audit log)", *m.Actual.TBTCompliance)
	}
	// Default SLO should be applied (no config stored).
	if m.SLO.TTFTMs != 2000 {
		t.Errorf("slo.ttft_ms = %d, want 2000 (default)", m.SLO.TTFTMs)
	}
}

// TestSLOCompliance_Breached seeds mostly non-compliant requests (latency >
// 2000ms). Expects status "breached".
func TestSLOCompliance_Breached(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// Seed 10 slow requests (5000ms each) + 2 fast.  Compliance ≈ 0.167 < 0.95.
	for i := 0; i < 10; i++ {
		seedInferenceEventLatency(t, h, "team-b", "slow-model", 5000.0)
	}
	for i := 0; i < 2; i++ {
		seedInferenceEventLatency(t, h, "team-b", "slow-model", 500.0)
	}

	resp, code := doSLOCompliance(t, h, "model_id=slow-model")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	m := findModel(t, resp, "slow-model")

	if m.Status != "breached" {
		t.Errorf("status = %q, want breached; actual=%+v", m.Status, m.Actual)
	}
	if m.Actual.TTFTCompliance == nil {
		t.Fatal("ttft_compliance is nil, want a value")
	}
	if *m.Actual.TTFTCompliance >= 0.95 {
		t.Errorf("ttft_compliance = %f, want < 0.95", *m.Actual.TTFTCompliance)
	}
}

// TestSLOCompliance_InsufficientData seeds fewer than 10 requests. Expects
// status "insufficient_data" and a nil ttft_compliance.
func TestSLOCompliance_InsufficientData(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// Seed only 5 events — below the 10-request minimum.
	for i := 0; i < 5; i++ {
		seedInferenceEventLatency(t, h, "team-c", "sparse-model", 100.0)
	}

	resp, code := doSLOCompliance(t, h, "model_id=sparse-model")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	m := findModel(t, resp, "sparse-model")

	if m.Status != "insufficient_data" {
		t.Errorf("status = %q, want insufficient_data", m.Status)
	}
	if m.Actual.TTFTCompliance != nil {
		t.Errorf("ttft_compliance = %v, want nil for insufficient_data", *m.Actual.TTFTCompliance)
	}
	if m.Actual.RequestCount != 5 {
		t.Errorf("request_count = %d, want 5", m.Actual.RequestCount)
	}
}

// TestSLOCompliance_ModelFilter verifies that ?model_id= filters correctly and
// other models present in the audit log are excluded from the response.
func TestSLOCompliance_ModelFilter(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// Seed events for two models.
	for i := 0; i < 15; i++ {
		seedInferenceEventLatency(t, h, "team-d", "model-alpha", 100.0)
	}
	for i := 0; i < 15; i++ {
		seedInferenceEventLatency(t, h, "team-d", "model-beta", 100.0)
	}

	resp, code := doSLOCompliance(t, h, "model_id=model-alpha")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("models len = %d, want 1 (filter should exclude model-beta)", len(resp.Models))
	}
	if resp.Models[0].ModelID != "model-alpha" {
		t.Errorf("model_id = %q, want model-alpha", resp.Models[0].ModelID)
	}
}

// TestSLOCompliance_DefaultConfig verifies that a model without an explicit SLO
// config in the registry uses the 2000ms TTFT default and 0.95 target.
func TestSLOCompliance_DefaultConfig(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// No SLO config stored — relies on defaults.
	for i := 0; i < 20; i++ {
		seedInferenceEventLatency(t, h, "team-e", "unconfigured-model", 500.0)
	}

	resp, code := doSLOCompliance(t, h, "model_id=unconfigured-model")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	m := findModel(t, resp, "unconfigured-model")

	// Verify defaults are applied.
	if m.SLO.TTFTMs != 2000 {
		t.Errorf("slo.ttft_ms = %d, want 2000 (default)", m.SLO.TTFTMs)
	}
	if m.SLO.TBTMs != 500 {
		t.Errorf("slo.tbt_ms = %d, want 500 (default)", m.SLO.TBTMs)
	}
	if m.SLO.TargetCompliance != 0.95 {
		t.Errorf("slo.target_compliance = %f, want 0.95 (default)", m.SLO.TargetCompliance)
	}
	// All 20 requests at 500ms < 2000ms → all compliant → status "met".
	if m.Status != "met" {
		t.Errorf("status = %q, want met", m.Status)
	}
}

// TestSLOCompliance_CustomConfig verifies that a model with a stored SLO config
// uses the custom threshold rather than the default.
func TestSLOCompliance_CustomConfig(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// Store a strict 500ms TTFT SLO for "strict-model".
	if err := reg.UpsertSLOConfig(context.Background(), &registry.SLOConfigRow{
		ModelID:          "strict-model",
		TTFTMs:           500,
		TBTMs:            200,
		TargetCompliance: 0.99,
	}); err != nil {
		t.Fatalf("UpsertSLOConfig: %v", err)
	}

	// Seed 20 requests at 800ms — all exceed the 500ms SLO.
	for i := 0; i < 20; i++ {
		seedInferenceEventLatency(t, h, "team-f", "strict-model", 800.0)
	}

	resp, code := doSLOCompliance(t, h, "model_id=strict-model")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	m := findModel(t, resp, "strict-model")

	if m.SLO.TTFTMs != 500 {
		t.Errorf("slo.ttft_ms = %d, want 500 (custom)", m.SLO.TTFTMs)
	}
	if m.SLO.TargetCompliance != 0.99 {
		t.Errorf("slo.target_compliance = %f, want 0.99 (custom)", m.SLO.TargetCompliance)
	}
	// 800ms > 500ms → non-compliant → breached.
	if m.Status != "breached" {
		t.Errorf("status = %q, want breached (800ms > 500ms threshold)", m.Status)
	}
}

// TestSLOCompliance_WindowHours verifies that window_hours is returned correctly
// and that bad values return 400.
func TestSLOCompliance_WindowHours(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})
	h := srv.Handler()

	// Valid window.
	resp, code := doSLOCompliance(t, h, "window_hours=48")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.WindowHours != 48 {
		t.Errorf("window_hours = %d, want 48", resp.WindowHours)
	}

	// Invalid values → 400.
	for _, bad := range []string{"window_hours=0", "window_hours=169", "window_hours=abc"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/slo/compliance?"+bad, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, rec.Code)
		}
	}
}

// TestSLOCompliance_EmptyLog verifies that an empty models array is returned
// when the inference_audit_log has no rows.
func TestSLOCompliance_EmptyLog(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Addr: ":0"})

	resp, code := doSLOCompliance(t, srv.Handler(), "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Models == nil {
		t.Error("models is null, want []")
	}
	if len(resp.Models) != 0 {
		t.Errorf("models len = %d, want 0", len(resp.Models))
	}
}
