package registry

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GetBillingForecast computes daily burn rate and projected monthly spend for
// every tenant that has inference activity in the current calendar-month billing
// period. Budget data is sourced from tenant_quotas when available.
//
// For each tenant the algorithm is:
//
//	burn_rate_daily_usd     = cost_used_usd / days_elapsed  (days_elapsed ≥ 1)
//	projected_monthly_usd   = burn_rate_daily_usd * days_in_period
//	budget_remaining_usd    = budget_usd - cost_used_usd         (only if budget set)
//	days_until_exhaustion   = budget_remaining_usd / burn_rate   (only if burn_rate > 0 and budget set)
//	quota_utilization_pct   = (cost_used_usd / budget_usd) * 100 (only if budget > 0)
//
// Tenants with zero cost activity are omitted. Returns an empty slice when the
// inference_audit_log has no rows for the current period.
func (r *SQLiteRegistry) GetBillingForecast(ctx context.Context, now time.Time) ([]BillingForecastEntry, error) {
	now = now.UTC()

	// Billing period = calendar month containing `now`.
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	// End of the period = last nanosecond of the last day in the month.
	nextMonth := periodStart.AddDate(0, 1, 0)
	periodEnd := nextMonth.Add(-time.Nanosecond)

	daysInPeriod := int(nextMonth.Sub(periodStart).Hours() / 24)

	// days_elapsed = days since period_start, clamped to [1, daysInPeriod].
	rawElapsed := int(now.Sub(periodStart).Hours()/24) + 1
	if rawElapsed < 1 {
		rawElapsed = 1
	}
	if rawElapsed > daysInPeriod {
		rawElapsed = daysInPeriod
	}
	daysElapsed := rawElapsed

	// Query cost per tenant for the current period using the same pricing JOIN
	// as GetTeamBillingReport. Rows with zero cost are also included so we can
	// show tenants that are active but have no pricing configured.
	rows, err := r.db.QueryContext(ctx, `
		WITH current_pricing AS (
			SELECT model_id, input_price_per_1k, output_price_per_1k
			FROM model_pricing mp_outer
			WHERE effective_from = (
				SELECT MAX(effective_from) FROM model_pricing mp_inner
				WHERE mp_inner.model_id = mp_outer.model_id
				  AND mp_inner.effective_from <= ?
			)
		)
		SELECT
			ial.tenant_id,
			COALESCE(SUM(
				(ial.prompt_tokens     / 1000.0) * COALESCE(cp.input_price_per_1k,  0) +
				(ial.completion_tokens / 1000.0) * COALESCE(cp.output_price_per_1k, 0)
			), 0) AS cost_used_usd,
			COALESCE(tq.monthly_cost_budget, 0) AS monthly_cost_budget
		FROM inference_audit_log ial
		LEFT JOIN current_pricing cp ON ial.model_id = cp.model_id
		LEFT JOIN tenant_quotas tq ON ial.tenant_id = tq.tenant_id
		WHERE ial.timestamp BETWEEN ? AND ?
		GROUP BY ial.tenant_id
		HAVING COUNT(*) > 0
		ORDER BY cost_used_usd DESC`,
		fmtTime(now), fmtTime(periodStart), fmtTime(now),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: get_billing_forecast: %w", err)
	}
	defer rows.Close()

	var entries []BillingForecastEntry

	for rows.Next() {
		var (
			tenantID    string
			costUsedUSD float64
			budgetUSD   float64
		)
		if err := rows.Scan(&tenantID, &costUsedUSD, &budgetUSD); err != nil {
			return nil, fmt.Errorf("registry: get_billing_forecast: scan: %w", err)
		}

		// Derive org_id / team_id from the tenant naming convention.
		orgID, teamID := splitTenantID(tenantID)

		burnRate := costUsedUSD / float64(daysElapsed)
		projected := burnRate * float64(daysInPeriod)

		entry := BillingForecastEntry{
			OrgID:               orgID,
			TeamID:              teamID,
			PeriodStart:         periodStart,
			PeriodEnd:           periodEnd,
			DaysElapsed:         daysElapsed,
			DaysInPeriod:        daysInPeriod,
			CostUsedUSD:         costUsedUSD,
			BurnRateDailyUSD:    burnRate,
			ProjectedMonthlyUSD: projected,
		}

		if budgetUSD > 0 {
			b := budgetUSD
			entry.BudgetUSD = &b

			remaining := budgetUSD - costUsedUSD
			entry.BudgetRemainingUSD = &remaining

			utilPct := (costUsedUSD / budgetUSD) * 100
			entry.QuotaUtilizationPct = &utilPct

			if burnRate > 0 {
				days := remaining / burnRate
				entry.DaysUntilExhaustion = &days
			}
		}

		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get_billing_forecast: %w", err)
	}

	if entries == nil {
		entries = []BillingForecastEntry{}
	}
	return entries, nil
}

// GetModelAdoptionSeries returns a request-count time-series grouped by model
// and bucketed by date (window="daily") or ISO week (window="weekly").
// days controls how far back from now to query (clamped to [1, 90]).
// Only the top 10 models by total requests are returned.
func (r *SQLiteRegistry) GetModelAdoptionSeries(ctx context.Context, window string, days int, now time.Time) (*ModelAdoptionResponse, error) {
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	if window != "weekly" {
		window = "daily"
	}

	since := now.UTC().Add(-time.Duration(days) * 24 * time.Hour)

	// Choose the SQLite date bucket expression.
	var bucketExpr string
	if window == "weekly" {
		bucketExpr = "strftime('%Y-W%W', timestamp)"
	} else {
		bucketExpr = "DATE(timestamp)"
	}

	query := fmt.Sprintf(`
		SELECT model_id, %s AS bucket, COUNT(*) AS requests, SUM(completion_tokens) AS tokens_out
		FROM inference_audit_log
		WHERE timestamp >= ?
		GROUP BY model_id, %s
		ORDER BY model_id, bucket`,
		bucketExpr, bucketExpr,
	)

	rows, err := r.db.QueryContext(ctx, query, fmtTime(since))
	if err != nil {
		return nil, fmt.Errorf("registry: get_model_adoption: %w", err)
	}
	defer rows.Close()

	// Accumulate into a map: modelID → []ModelAdoptionBucket
	type modelData struct {
		totalRequests int64
		buckets       []ModelAdoptionBucket
	}
	byModel := make(map[string]*modelData)
	var modelOrder []string

	for rows.Next() {
		var (
			modelID   string
			bucket    string
			requests  int64
			tokensOut sql.NullInt64
		)
		if err := rows.Scan(&modelID, &bucket, &requests, &tokensOut); err != nil {
			return nil, fmt.Errorf("registry: get_model_adoption: scan: %w", err)
		}
		var to int64
		if tokensOut.Valid {
			to = tokensOut.Int64
		}

		if _, ok := byModel[modelID]; !ok {
			byModel[modelID] = &modelData{}
			modelOrder = append(modelOrder, modelID)
		}
		md := byModel[modelID]
		md.totalRequests += requests
		md.buckets = append(md.buckets, ModelAdoptionBucket{
			Date:      bucket,
			Requests:  requests,
			TokensOut: to,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get_model_adoption: %w", err)
	}

	// Sort models by total requests DESC, keep top 10.
	sort.Slice(modelOrder, func(i, j int) bool {
		return byModel[modelOrder[i]].totalRequests > byModel[modelOrder[j]].totalRequests
	})
	const maxModels = 10
	if len(modelOrder) > maxModels {
		modelOrder = modelOrder[:maxModels]
	}

	series := make([]ModelAdoptionSeries, 0, len(modelOrder))
	for _, mid := range modelOrder {
		series = append(series, ModelAdoptionSeries{
			ModelID: mid,
			Buckets: byModel[mid].buckets,
		})
	}

	return &ModelAdoptionResponse{
		Window: window,
		Days:   days,
		Series: series,
	}, nil
}

// GetSLAComplianceByTenant returns the fraction of requests with latency_ms <
// thresholdMs for each tenant in the given billing window.
// Only rows where latency_ms > 0 are included in the denominator (rows with
// latency_ms=0 indicate unrecorded latency and are excluded to avoid skewing
// the rate). Returns an empty slice when no qualifying rows exist.
func (r *SQLiteRegistry) GetSLAComplianceByTenant(ctx context.Context, start, end time.Time, thresholdMs float64) ([]TenantSLAStat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			tenant_id,
			SUM(CASE WHEN latency_ms > 0 AND latency_ms < ? THEN 1 ELSE 0 END) AS compliant,
			SUM(CASE WHEN latency_ms > 0                   THEN 1 ELSE 0 END) AS total
		FROM inference_audit_log
		WHERE timestamp BETWEEN ? AND ?
		GROUP BY tenant_id
		HAVING total > 0`,
		thresholdMs, fmtTime(start), fmtTime(end),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: get_sla_compliance: %w", err)
	}
	defer rows.Close()

	var stats []TenantSLAStat
	for rows.Next() {
		var (
			tenantID  string
			compliant int64
			total     int64
		)
		if err := rows.Scan(&tenantID, &compliant, &total); err != nil {
			return nil, fmt.Errorf("registry: get_sla_compliance: scan: %w", err)
		}
		rate := 0.0
		if total > 0 {
			rate = float64(compliant) / float64(total)
		}
		stats = append(stats, TenantSLAStat{
			TenantID:          tenantID,
			SLAComplianceRate: rate,
			SLAThresholdMs:    thresholdMs,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get_sla_compliance: %w", err)
	}

	if stats == nil {
		stats = []TenantSLAStat{}
	}
	return stats, nil
}

// splitTenantID decomposes a tenant_id into (orgID, teamID) using the
// "<orgId>/<teamSlug>" naming convention. When no "/" is present, orgID equals
// the full tenantID and teamID is empty.
func splitTenantID(tenantID string) (orgID, teamID string) {
	if idx := strings.IndexByte(tenantID, '/'); idx >= 0 {
		return tenantID[:idx], tenantID[idx+1:]
	}
	return tenantID, ""
}
