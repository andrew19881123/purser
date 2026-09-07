package server

import (
	"net/http"
	"strconv"
	"time"
)

// handleBillingForecast serves GET /api/v1/billing/forecast.
//
// Enterprise gate: requires the "billing" feature in the active license.
// Returns 402 Payment Required without a valid entitlement.
//
// Response: {"forecast": [BillingForecastEntry, ...]}
//
// Each entry shows the daily burn rate and projected monthly spend for one
// tenant based on inference activity in the current calendar-month billing
// period. Entries are ordered by cost_used_usd descending. An empty forecast
// array is returned when no inference events exist for the current period.
func (s *Server) handleBillingForecast(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureBilling) {
		s.writeLicenseRequired(w, featureBilling)
		return
	}

	entries, err := s.reg.GetBillingForecast(r.Context(), time.Now().UTC())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "billing_forecast_failed", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"forecast": entries})
}

// handleModelAdoption serves GET /api/v1/billing/models/adoption.
//
// Enterprise gate: requires the "billing" feature in the active license.
// Returns 402 Payment Required without a valid entitlement.
//
// Query parameters:
//
//	window — "daily" (default) or "weekly"
//	days   — number of days to look back (default 30, max 90)
//
// Response: ModelAdoptionResponse with per-model time-series bucketed by date
// or ISO week. Only the top 10 models by total request count are included.
func (s *Server) handleModelAdoption(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureBilling) {
		s.writeLicenseRequired(w, featureBilling)
		return
	}

	q := r.URL.Query()

	window := q.Get("window")
	if window != "weekly" {
		window = "daily"
	}

	days := 30
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			s.writeError(w, http.StatusBadRequest, "bad_request", "days must be an integer between 1 and 90")
			return
		}
		days = n
	}

	resp, err := s.reg.GetModelAdoptionSeries(r.Context(), window, days, time.Now().UTC())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "model_adoption_failed", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, resp)
}
