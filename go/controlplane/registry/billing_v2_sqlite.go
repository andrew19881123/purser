package registry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetTeamBillingReport aggregates the inference_audit_log for a specific team.
// The "team ID" equals the tenant_id stored in the team's API keys.
//
// Cost is calculated via a LEFT JOIN on model_pricing using the most-recently
// effective pricing row for each model (effective_from <= end). If no pricing
// row exists for a model the cost contribution is zero, so the query is always
// safe to call even when the model_pricing table is empty.
//
// Results are grouped by model_id, ordered by total_tokens DESC.
func (r *SQLiteRegistry) GetTeamBillingReport(ctx context.Context, teamID string, start, end time.Time) (*TeamBillingReport, error) {
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
			ial.model_id,
			COUNT(*)                                       AS request_count,
			SUM(ial.prompt_tokens)                         AS prompt_tokens,
			SUM(ial.completion_tokens)                     AS completion_tokens,
			SUM(ial.prompt_tokens + ial.completion_tokens) AS total_tokens,
			AVG(ial.latency_ms)                            AS avg_latency_ms,
			COALESCE(SUM(
				(ial.prompt_tokens     / 1000.0) * COALESCE(cp.input_price_per_1k,  0) +
				(ial.completion_tokens / 1000.0) * COALESCE(cp.output_price_per_1k, 0)
			), 0) AS cost_usd
		FROM inference_audit_log ial
		LEFT JOIN current_pricing cp ON ial.model_id = cp.model_id
		WHERE ial.tenant_id = ?
		  AND ial.timestamp BETWEEN ? AND ?
		GROUP BY ial.model_id
		ORDER BY total_tokens DESC`,
		fmtTime(end), teamID, fmtTime(start), fmtTime(end),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: get team billing report: %w", err)
	}
	defer rows.Close()

	var (
		byModel       []BillingTenantUsage
		totalRequests int64
		inputTokens   int64
		outputTokens  int64
		totalTokens   int64
		totalCostUSD  float64
	)

	for rows.Next() {
		var (
			tu           BillingTenantUsage
			avgLatencyMs sql.NullFloat64
			costUSD      float64
		)
		if err := rows.Scan(
			&tu.ModelID,
			&tu.RequestCount,
			&tu.PromptTokens,
			&tu.CompletionTokens,
			&tu.TotalTokens,
			&avgLatencyMs,
			&costUSD,
		); err != nil {
			return nil, fmt.Errorf("registry: get team billing report: scan: %w", err)
		}
		if avgLatencyMs.Valid {
			tu.AvgLatencyMs = avgLatencyMs.Float64
		}
		tu.TenantID = teamID
		tu.PeriodStart = start.UTC()
		tu.PeriodEnd = end.UTC()

		totalRequests += tu.RequestCount
		inputTokens += tu.PromptTokens
		outputTokens += tu.CompletionTokens
		totalTokens += tu.TotalTokens
		totalCostUSD += costUSD
		byModel = append(byModel, tu)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get team billing report: %w", err)
	}

	if byModel == nil {
		byModel = []BillingTenantUsage{}
	}

	return &TeamBillingReport{
		TeamID:        teamID,
		PeriodStart:   start.UTC(),
		PeriodEnd:     end.UTC(),
		TotalRequests: totalRequests,
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		TotalTokens:   totalTokens,
		TotalCostUSD:  totalCostUSD,
		ByModel:       byModel,
	}, nil
}

// GetOrgBillingReport aggregates billing for all teams belonging to orgID.
//
// Teams are discovered from inference_audit_log using the naming convention
// "<orgID>/<teamSlug>" for tenant_id values. For example, if the org ID is
// "acme-corp", API keys for its teams should use tenant values like
// "acme-corp/engineering" and "acme-corp/finance".
//
// The org report sums TotalCostUSD and TotalTokens across all discovered teams.
func (r *SQLiteRegistry) GetOrgBillingReport(ctx context.Context, orgID string, start, end time.Time) (*OrgBillingReport, error) {
	// Discover distinct team IDs (tenant_ids) that belong to this org.
	// Pattern: "<orgID>/<anything>" — the slash separator prevents false matches
	// when one org ID is a prefix of another (e.g. "acme" vs "acme-corp").
	pattern := orgID + "/%"
	teamRows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT tenant_id FROM inference_audit_log
		 WHERE tenant_id LIKE ? AND timestamp BETWEEN ? AND ?
		 ORDER BY tenant_id`,
		pattern, fmtTime(start), fmtTime(end),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: get org billing report: list teams: %w", err)
	}
	defer teamRows.Close()

	var teamIDs []string
	for teamRows.Next() {
		var tid string
		if err := teamRows.Scan(&tid); err != nil {
			return nil, fmt.Errorf("registry: get org billing report: scan team id: %w", err)
		}
		teamIDs = append(teamIDs, tid)
	}
	if err := teamRows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get org billing report: iterate teams: %w", err)
	}

	var (
		teamReports  []TeamBillingReport
		totalCostUSD float64
		totalTokens  int64
	)

	for _, tid := range teamIDs {
		tr, err := r.GetTeamBillingReport(ctx, tid, start, end)
		if err != nil {
			return nil, fmt.Errorf("registry: get org billing report: team %q: %w", tid, err)
		}
		tr.OrgID = orgID
		totalCostUSD += tr.TotalCostUSD
		totalTokens += tr.TotalTokens
		teamReports = append(teamReports, *tr)
	}

	if teamReports == nil {
		teamReports = []TeamBillingReport{}
	}

	return &OrgBillingReport{
		OrgID:        orgID,
		PeriodStart:  start.UTC(),
		PeriodEnd:    end.UTC(),
		TotalCostUSD: totalCostUSD,
		TotalTokens:  totalTokens,
		Teams:        teamReports,
	}, nil
}
