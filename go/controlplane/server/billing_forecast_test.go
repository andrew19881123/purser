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

// TestBillingForecast_Happy verifies that /billing/forecast returns 200 with a
// non-empty forecast array when inference events exist in the current period.
func TestBillingForecast_Happy(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	// Seed model pricing so cost > 0.
	if err := reg.UpsertModelPricing(context.Background(), &registry.ModelPricing{
		ModelID:          "model-a",
		EffectiveFrom:    time.Now().Add(-24 * time.Hour),
		InputPricePer1K:  0.001,
		OutputPricePer1K: 0.002,
	}); err != nil {
		t.Fatalf("UpsertModelPricing: %v", err)
	}

	// Seed quota/budget for tenant "acme/eng".
	if err := reg.UpsertTenantQuota(context.Background(), &registry.TenantQuota{
		TenantID:          "acme/eng",
		MonthlyCostBudget: 500.0,
	}); err != nil {
		t.Fatalf("UpsertTenantQuota: %v", err)
	}

	// Seed two inference events for "acme/eng".
	seedInferenceEvent(t, h, "acme/eng", "model-a", 10000, 5000, 150.0)
	seedInferenceEvent(t, h, "acme/eng", "model-a", 20000, 8000, 200.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/forecast", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Forecast []registry.BillingForecastEntry `json:"forecast"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	if len(resp.Forecast) == 0 {
		t.Fatalf("forecast is empty, expected at least one entry")
	}

	entry := resp.Forecast[0]

	if entry.OrgID == "" {
		t.Errorf("org_id is empty")
	}
	if entry.DaysElapsed < 1 {
		t.Errorf("days_elapsed = %d, want >= 1", entry.DaysElapsed)
	}
	if entry.DaysInPeriod < 28 || entry.DaysInPeriod > 31 {
		t.Errorf("days_in_period = %d, want 28–31", entry.DaysInPeriod)
	}
	if entry.PeriodStart.IsZero() {
		t.Error("period_start is zero")
	}
	if entry.PeriodEnd.IsZero() {
		t.Error("period_end is zero")
	}
	// cost > 0 because model pricing is configured.
	if entry.CostUsedUSD <= 0 {
		t.Errorf("cost_used_usd = %f, want > 0 (model pricing is set)", entry.CostUsedUSD)
	}
	// burn_rate = cost / days_elapsed
	wantBurn := entry.CostUsedUSD / float64(entry.DaysElapsed)
	if abs64(entry.BurnRateDailyUSD-wantBurn) > 0.0001 {
		t.Errorf("burn_rate_daily_usd = %f, want %f", entry.BurnRateDailyUSD, wantBurn)
	}
	// projected = burn * days_in_period
	wantProj := entry.BurnRateDailyUSD * float64(entry.DaysInPeriod)
	if abs64(entry.ProjectedMonthlyUSD-wantProj) > 0.0001 {
		t.Errorf("projected_monthly_usd = %f, want %f", entry.ProjectedMonthlyUSD, wantProj)
	}
	// Budget fields present since we configured a budget.
	if entry.BudgetUSD == nil {
		t.Error("budget_usd is nil, expected a value (quota was configured)")
	}
	if entry.BudgetRemainingUSD == nil {
		t.Error("budget_remaining_usd is nil, expected a value")
	}
	if entry.QuotaUtilizationPct == nil {
		t.Error("quota_utilization_pct is nil, expected a value")
	}
}

// TestBillingForecast_NoUsage verifies that /billing/forecast returns an empty
// forecast array when the inference_audit_log has no rows for the current period.
func TestBillingForecast_NoUsage(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/forecast", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Forecast []registry.BillingForecastEntry `json:"forecast"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	// Forecast should be an empty array, not null.
	if resp.Forecast == nil {
		t.Errorf("forecast is null, want []")
	}
	if len(resp.Forecast) != 0 {
		t.Errorf("forecast len = %d, want 0", len(resp.Forecast))
	}
}

// TestBillingForecast_NoBudget verifies that tenants without a quota configured
// receive forecast entries without budget-related fields.
func TestBillingForecast_NoBudget(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	// Seed event but do NOT configure a quota/budget.
	seedInferenceEvent(t, h, "tenant-no-budget", "model-x", 100, 50, 100.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/forecast", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Forecast []registry.BillingForecastEntry `json:"forecast"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	if len(resp.Forecast) == 0 {
		t.Fatalf("forecast is empty, expected one entry for tenant-no-budget")
	}

	entry := resp.Forecast[0]
	if entry.BudgetUSD != nil {
		t.Errorf("budget_usd = %v, want nil (no quota configured)", *entry.BudgetUSD)
	}
	if entry.BudgetRemainingUSD != nil {
		t.Errorf("budget_remaining_usd = %v, want nil", *entry.BudgetRemainingUSD)
	}
	if entry.DaysUntilExhaustion != nil {
		t.Errorf("days_until_exhaustion = %v, want nil", *entry.DaysUntilExhaustion)
	}
}

// TestBillingForecast_EnterpriseGated verifies that 402 is returned without a license.
func TestBillingForecast_EnterpriseGated(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/forecast", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
}

// TestModelAdoption_Daily verifies that /billing/models/adoption returns a
// daily time-series grouped by model when inference events exist.
func TestModelAdoption_Daily(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	// Seed events for two models.
	seedInferenceEvent(t, h, "team-a", "llama-8b", 100, 50, 120.0)
	seedInferenceEvent(t, h, "team-a", "llama-8b", 200, 80, 100.0)
	seedInferenceEvent(t, h, "team-b", "mistral-7b", 150, 70, 90.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/models/adoption?window=daily&days=7", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp registry.ModelAdoptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	if resp.Window != "daily" {
		t.Errorf("window = %q, want daily", resp.Window)
	}
	if resp.Days != 7 {
		t.Errorf("days = %d, want 7", resp.Days)
	}
	if len(resp.Series) == 0 {
		t.Fatalf("series is empty, expected model entries")
	}

	// Both models should appear.
	modelIDs := make(map[string]bool)
	for _, s := range resp.Series {
		modelIDs[s.ModelID] = true
		if len(s.Buckets) == 0 {
			t.Errorf("model %q has empty buckets", s.ModelID)
		}
	}
	if !modelIDs["llama-8b"] {
		t.Errorf("llama-8b not in series; got %v", resp.Series)
	}

	// Verify llama-8b has 2 requests (both seeded today so same bucket).
	for _, s := range resp.Series {
		if s.ModelID != "llama-8b" {
			continue
		}
		var total int64
		for _, b := range s.Buckets {
			total += b.Requests
		}
		if total != 2 {
			t.Errorf("llama-8b total requests = %d, want 2", total)
		}
	}
}

// TestModelAdoption_Weekly verifies that the weekly window is accepted and
// returns ISO-week-bucketed data.
func TestModelAdoption_Weekly(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	seedInferenceEvent(t, h, "team-a", "model-w", 100, 50, 120.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/models/adoption?window=weekly&days=30", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp registry.ModelAdoptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	if resp.Window != "weekly" {
		t.Errorf("window = %q, want weekly", resp.Window)
	}
	if len(resp.Series) == 0 {
		t.Fatalf("series is empty, expected model-w entry")
	}
	// Weekly bucket keys should contain 'W' (e.g. "2026-W36").
	for _, s := range resp.Series {
		for _, b := range s.Buckets {
			if len(b.Date) < 7 {
				t.Errorf("bucket date %q looks too short for weekly format", b.Date)
			}
		}
	}
}

// TestModelAdoption_EmptyLog verifies that an empty series array is returned
// when the inference_audit_log has no rows.
func TestModelAdoption_EmptyLog(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/models/adoption", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp registry.ModelAdoptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	if resp.Series == nil {
		t.Errorf("series is null, want []")
	}
	if len(resp.Series) != 0 {
		t.Errorf("series len = %d, want 0", len(resp.Series))
	}
}

// TestBillingReport_SLACompliance verifies that /billing/report?sla_threshold_ms=2000
// populates a sla_stats array in the response.
func TestBillingReport_SLACompliance(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	// Seed events: team-eng gets 3 events, 2 below 2000ms threshold, 1 above.
	seedInferenceEvent(t, h, "team-eng", "model-a", 100, 50, 500.0)  // compliant
	seedInferenceEvent(t, h, "team-eng", "model-a", 200, 80, 1500.0) // compliant
	seedInferenceEvent(t, h, "team-eng", "model-a", 300, 90, 3000.0) // non-compliant

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report?sla_threshold_ms=2000", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var report struct {
		SLAStats []registry.TenantSLAStat `json:"sla_stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}

	if len(report.SLAStats) == 0 {
		t.Fatalf("sla_stats is empty, expected at least one entry")
	}

	// Find team-eng entry.
	var found *registry.TenantSLAStat
	for i := range report.SLAStats {
		if report.SLAStats[i].TenantID == "team-eng" {
			found = &report.SLAStats[i]
		}
	}
	if found == nil {
		t.Fatalf("team-eng not found in sla_stats; got %+v", report.SLAStats)
	}

	if found.SLAThresholdMs != 2000 {
		t.Errorf("sla_threshold_ms = %v, want 2000", found.SLAThresholdMs)
	}
	// 2 out of 3 requests are below 2000ms → compliance rate = 0.6667
	wantRate := 2.0 / 3.0
	if abs64(found.SLAComplianceRate-wantRate) > 0.001 {
		t.Errorf("sla_compliance_rate = %f, want ~%f", found.SLAComplianceRate, wantRate)
	}
}

// TestBillingReport_SLACompliance_NoParam verifies that sla_stats is absent
// (omitempty) when sla_threshold_ms is not specified.
func TestBillingReport_SLACompliance_NoParam(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})
	h := srv.Handler()

	seedInferenceEvent(t, h, "team-eng", "model-a", 100, 50, 500.0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/report", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["sla_stats"]; ok {
		t.Errorf("sla_stats present in response without sla_threshold_ms param; body=%s", rec.Body.String())
	}
}

// TestModelAdoption_BadDays verifies that a non-integer or out-of-range days
// param returns 400 Bad Request.
func TestModelAdoption_BadDays(t *testing.T) {
	reg := newReg(t)
	lic := newBillingLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	tests := []string{"days=abc", "days=0", "days=100"}
	for _, q := range tests {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/billing/models/adoption?"+q, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query=%q: status = %d, want 400", q, rec.Code)
		}
	}
}

// abs64 is a float64 absolute value helper for test assertions.
func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
