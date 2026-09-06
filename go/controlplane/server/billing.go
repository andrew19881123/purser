package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"time"
)

// featureBilling is the enterprise entitlement required to access the full
// chargeback report endpoint. The quick summary endpoint (/billing/summary)
// is not gated and is used by the Settings page QuickStatsBar.
const featureBilling = "billing"

// handleBillingReport serves GET /api/v1/billing/report.
//
// Enterprise gate: requires the "billing" feature in the active license.
// Returns 402 Payment Required without a valid entitlement.
//
// Auth: any RBAC role that can read /api/v1/* (admin or viewer) — enforced
// globally by rbacMiddleware; no per-handler check needed.
//
// Query parameters (all optional):
//
//	start      — RFC3339 window start; defaults to now − 30 days
//	end        — RFC3339 window end;   defaults to now
//	tenant_id  — filter to one tenant; empty = all tenants
//	format     — "json" (default) or "csv"
//
// JSON response: BillingReport (registry.BillingReport).
// CSV response: Content-Type text/csv, Content-Disposition attachment.
func (s *Server) handleBillingReport(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureBilling) {
		s.writeLicenseRequired(w, featureBilling)
		return
	}

	start, end, err := parseBillingWindow(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	tenantID := r.URL.Query().Get("tenant_id")
	format := r.URL.Query().Get("format")

	report, err := s.reg.GetBillingReport(r.Context(), start, end, tenantID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "billing_report_failed", err.Error())
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="billing-report.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"tenant_id", "model_id", "request_count",
			"prompt_tokens", "completion_tokens", "total_tokens", "avg_latency_ms",
		})
		for _, tu := range report.Tenants {
			_ = cw.Write([]string{
				tu.TenantID,
				tu.ModelID,
				fmt.Sprintf("%d", tu.RequestCount),
				fmt.Sprintf("%d", tu.PromptTokens),
				fmt.Sprintf("%d", tu.CompletionTokens),
				fmt.Sprintf("%d", tu.TotalTokens),
				fmt.Sprintf("%.1f", tu.AvgLatencyMs),
			})
		}
		cw.Flush()
		return
	}

	s.writeJSON(w, http.StatusOK, report)
}

// handleBillingSummary serves GET /api/v1/billing/summary.
//
// This is a lighter endpoint that returns total requests and total tokens for
// the last 30 days, optionally filtered by tenant. It is NOT enterprise-gated
// and is used by the Settings-page QuickStatsBar and the Chargeback page header.
func (s *Server) handleBillingSummary(w http.ResponseWriter, r *http.Request) {
	end := time.Now().UTC()
	start := end.Add(-30 * 24 * time.Hour)
	tenantID := r.URL.Query().Get("tenant_id")

	report, err := s.reg.GetBillingReport(r.Context(), start, end, tenantID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "billing_summary_failed", err.Error())
		return
	}

	// Count distinct active tenants in the window.
	tenantSet := make(map[string]struct{}, len(report.Tenants))
	for _, tu := range report.Tenants {
		tenantSet[tu.TenantID] = struct{}{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"period_start":   report.PeriodStart,
		"period_end":     report.PeriodEnd,
		"total_requests": report.TotalRequests,
		"total_tokens":   report.TotalTokens,
		"active_tenants": len(tenantSet),
	})
}

// parseBillingWindow reads the ?start= and ?end= query params (RFC3339).
// When absent the defaults are: end = now, start = now − 30 days.
func parseBillingWindow(r *http.Request) (start, end time.Time, err error) {
	now := time.Now().UTC()
	end = now
	start = now.Add(-30 * 24 * time.Hour)

	if v := r.URL.Query().Get("start"); v != "" {
		t, parseErr := time.Parse(time.RFC3339, v)
		if parseErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start: must be RFC3339, got %q", v)
		}
		start = t
	}
	if v := r.URL.Query().Get("end"); v != "" {
		t, parseErr := time.Parse(time.RFC3339, v)
		if parseErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end: must be RFC3339, got %q", v)
		}
		end = t
	}
	return start, end, nil
}
