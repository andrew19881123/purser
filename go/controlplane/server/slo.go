package server

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// sloDefaultTTFTMs is the global default TTFT SLO threshold in milliseconds
// when no explicit SLO config row exists for a model or a global "*" default.
const sloDefaultTTFTMs = 2000

// sloDefaultTBTMs is the global default inter-token latency SLO threshold.
const sloDefaultTBTMs = 500

// sloDefaultTargetCompliance is the minimum compliance fraction considered "met".
const sloDefaultTargetCompliance = 0.95

// sloMinRequests is the minimum number of requests needed before the status is
// "met" or "breached"; below this the status is "insufficient_data".
const sloMinRequests = 10

// sloContract is the configured SLO for a model.
type sloContract struct {
	TTFTMs           int     `json:"ttft_ms"`
	TBTMs            int     `json:"tbt_ms"`
	TargetCompliance float64 `json:"target_compliance"`
}

// sloActual is the measured compliance for a model over the query window.
type sloActual struct {
	TTFTCompliance *float64  `json:"ttft_compliance"` // nil when insufficient data
	TBTCompliance  *float64  `json:"tbt_compliance"`  // always nil (not in audit log)
	RequestCount   int64     `json:"request_count"`
	PeriodStart    time.Time `json:"period_start"`
}

// sloModelEntry is one element of the models array in the compliance response.
type sloModelEntry struct {
	ModelID string      `json:"model_id"`
	SLO     sloContract `json:"slo"`
	Actual  sloActual   `json:"actual"`
	// Status is "met", "breached", or "insufficient_data".
	Status string `json:"status"`
}

// handleSLOCompliance serves GET /api/v1/slo/compliance.
//
// Query parameters:
//
//	model_id     — filter to one model (optional)
//	window_hours — lookback window in hours (default 24, max 168)
//
// Response shape:
//
//	{
//	  "window_hours": 24,
//	  "generated_at": "2026-09-08T21:00:00Z",
//	  "models": [
//	    {
//	      "model_id": "llama3-8b",
//	      "slo": { "ttft_ms": 2000, "tbt_ms": 500, "target_compliance": 0.95 },
//	      "actual": {
//	        "ttft_compliance": 0.987,
//	        "tbt_compliance": null,
//	        "request_count": 1420,
//	        "period_start": "2026-09-07T21:00:00Z"
//	      },
//	      "status": "met"
//	    }
//	  ]
//	}
//
// Status values:
//   - "met"               — actual ttft_compliance >= target_compliance
//   - "breached"          — actual ttft_compliance < target_compliance
//   - "insufficient_data" — request_count < 10
func (s *Server) handleSLOCompliance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	modelFilter := q.Get("model_id")

	windowHours := 24
	if v := q.Get("window_hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 168 {
			s.writeError(w, http.StatusBadRequest, "bad_request",
				"window_hours must be an integer between 1 and 168")
			return
		}
		windowHours = n
	}

	now := time.Now().UTC()
	windowStart := now.Add(-time.Duration(windowHours) * time.Hour)

	// Load all persisted SLO configs for per-model threshold resolution.
	allCfgs, err := s.reg.GetAllSLOConfigs(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "slo_configs_failed", err.Error())
		return
	}

	cfgByModel := make(map[string]*registry.SLOConfigRow, len(allCfgs))
	for _, c := range allCfgs {
		cfgByModel[c.ModelID] = c
	}

	// resolveSLO returns the effective (ttft_ms, tbt_ms, target_compliance) for
	// a model: explicit entry > global "*" entry > hard-coded defaults.
	resolveSLO := func(modelID string) (ttft, tbt int, target float64) {
		if c, ok := cfgByModel[modelID]; ok {
			return c.TTFTMs, c.TBTMs, c.TargetCompliance
		}
		if g, ok := cfgByModel["*"]; ok {
			return g.TTFTMs, g.TBTMs, g.TargetCompliance
		}
		return sloDefaultTTFTMs, sloDefaultTBTMs, sloDefaultTargetCompliance
	}

	// Build compliance stats.  We group models by their effective TTFT threshold
	// so we can issue one query per unique threshold instead of one per model.
	// In practice there are 1-3 unique thresholds across all models.
	statsByModel := make(map[string]registry.ModelSLOStat)

	if modelFilter != "" {
		// Single-model case: one query with the model's specific threshold.
		ttftMs, _, _ := resolveSLO(modelFilter)
		stats, err := s.reg.GetSLOComplianceByModel(r.Context(), windowStart, now, modelFilter, float64(ttftMs))
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "slo_query_failed", err.Error())
			return
		}
		for _, st := range stats {
			statsByModel[st.ModelID] = st
		}
	} else {
		// All-models case.
		// 1. Issue one query per distinct threshold for explicitly configured models.
		threshToModels := make(map[int][]string)
		for _, c := range allCfgs {
			if c.ModelID != "*" {
				threshToModels[c.TTFTMs] = append(threshToModels[c.TTFTMs], c.ModelID)
			}
		}
		for threshMs, ids := range threshToModels {
			for _, mid := range ids {
				stats, err := s.reg.GetSLOComplianceByModel(r.Context(), windowStart, now, mid, float64(threshMs))
				if err != nil {
					s.writeError(w, http.StatusInternalServerError, "slo_query_failed", err.Error())
					return
				}
				for _, st := range stats {
					statsByModel[st.ModelID] = st
				}
			}
		}

		// 2. Query all remaining models (those not explicitly configured) using
		// the global default threshold.  Any model already captured above will
		// be skipped (higher-priority per-model config wins).
		globalTTFT, _, _ := resolveSLO("__no_such_model__")
		allStats, err := s.reg.GetSLOComplianceByModel(r.Context(), windowStart, now, "" /*all*/, float64(globalTTFT))
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "slo_query_failed", err.Error())
			return
		}
		for _, st := range allStats {
			if _, exists := statsByModel[st.ModelID]; !exists {
				statsByModel[st.ModelID] = st
			}
		}
	}

	// Build the response models slice.
	models := make([]sloModelEntry, 0, len(statsByModel))
	for _, st := range statsByModel {
		ttftMs, tbtMs, target := resolveSLO(st.ModelID)

		entry := sloModelEntry{
			ModelID: st.ModelID,
			SLO: sloContract{
				TTFTMs:           ttftMs,
				TBTMs:            tbtMs,
				TargetCompliance: target,
			},
			Actual: sloActual{
				RequestCount:  st.RequestCount,
				PeriodStart:   st.PeriodStart,
				TBTCompliance: nil, // TBT is not recorded in inference_audit_log
			},
		}

		if st.RequestCount < sloMinRequests {
			entry.Status = "insufficient_data"
		} else {
			// compliance = compliant_count / request_count.
			// Unrecorded latencies (latency_ms == 0) are in request_count but not
			// in compliant_count, so they conservatively lower the compliance rate.
			compliance := float64(st.CompliantCount) / float64(st.RequestCount)
			entry.Actual.TTFTCompliance = &compliance
			if compliance >= target {
				entry.Status = "met"
			} else {
				entry.Status = "breached"
			}
		}

		models = append(models, entry)
	}

	// Sort by model_id for deterministic output.
	sort.Slice(models, func(i, j int) bool {
		return models[i].ModelID < models[j].ModelID
	})

	s.writeJSON(w, http.StatusOK, map[string]any{
		"window_hours": windowHours,
		"generated_at": now,
		"models":       models,
	})
}
