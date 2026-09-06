package registry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetBillingReport queries inference_audit_log and returns a BillingReport for
// the given [start, end] window. When tenantID is non-empty only that tenant's
// rows are included. Rows are grouped by (tenant_id, model_id) and ordered by
// total_tokens DESC so the heaviest consumers appear first.
func (r *SQLiteRegistry) GetBillingReport(ctx context.Context, start, end time.Time, tenantID string) (*BillingReport, error) {
	query := `
		SELECT
			tenant_id,
			model_id,
			COUNT(*)                               AS request_count,
			SUM(prompt_tokens)                     AS prompt_tokens,
			SUM(completion_tokens)                 AS completion_tokens,
			SUM(prompt_tokens + completion_tokens) AS total_tokens,
			AVG(latency_ms)                        AS avg_latency_ms
		FROM inference_audit_log
		WHERE timestamp BETWEEN ? AND ?`

	args := []any{fmtTime(start), fmtTime(end)}

	if tenantID != "" {
		query += " AND tenant_id = ?"
		args = append(args, tenantID)
	}

	query += " GROUP BY tenant_id, model_id ORDER BY total_tokens DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("registry: get billing report: %w", err)
	}
	defer rows.Close()

	var tenants []BillingTenantUsage
	var totalRequests, totalTokens int64

	for rows.Next() {
		var (
			tu           BillingTenantUsage
			avgLatencyMs sql.NullFloat64
		)
		if err := rows.Scan(
			&tu.TenantID,
			&tu.ModelID,
			&tu.RequestCount,
			&tu.PromptTokens,
			&tu.CompletionTokens,
			&tu.TotalTokens,
			&avgLatencyMs,
		); err != nil {
			return nil, fmt.Errorf("registry: get billing report: scan: %w", err)
		}
		if avgLatencyMs.Valid {
			tu.AvgLatencyMs = avgLatencyMs.Float64
		}
		tu.PeriodStart = start.UTC()
		tu.PeriodEnd = end.UTC()
		totalRequests += tu.RequestCount
		totalTokens += tu.TotalTokens
		tenants = append(tenants, tu)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get billing report: %w", err)
	}

	if tenants == nil {
		tenants = []BillingTenantUsage{}
	}

	return &BillingReport{
		PeriodStart:   start.UTC(),
		PeriodEnd:     end.UTC(),
		Tenants:       tenants,
		TotalRequests: totalRequests,
		TotalTokens:   totalTokens,
	}, nil
}
