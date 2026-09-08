package registry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UpsertSLOConfig inserts or replaces the SLO configuration for cfg.ModelID.
// The special model_id "*" represents the global default threshold.
func (r *SQLiteRegistry) UpsertSLOConfig(ctx context.Context, cfg *SLOConfigRow) error {
	if cfg == nil {
		return nil
	}
	cfg.UpdatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO slo_configs (model_id, ttft_ms, tbt_ms, target_compliance, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(model_id) DO UPDATE SET
			ttft_ms           = excluded.ttft_ms,
			tbt_ms            = excluded.tbt_ms,
			target_compliance = excluded.target_compliance,
			updated_at        = excluded.updated_at`,
		cfg.ModelID,
		cfg.TTFTMs,
		cfg.TBTMs,
		cfg.TargetCompliance,
		fmtTime(cfg.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("registry: upsert_slo_config %q: %w", cfg.ModelID, err)
	}
	return nil
}

// GetSLOConfig returns the SLO configuration for modelID, or ErrNotFound.
func (r *SQLiteRegistry) GetSLOConfig(ctx context.Context, modelID string) (*SLOConfigRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT model_id, ttft_ms, tbt_ms, target_compliance, updated_at
		FROM slo_configs
		WHERE model_id = ?`, modelID)

	var cfg SLOConfigRow
	var updatedAt string
	if err := row.Scan(&cfg.ModelID, &cfg.TTFTMs, &cfg.TBTMs, &cfg.TargetCompliance, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("registry: get_slo_config %q: %w", modelID, err)
	}
	cfg.UpdatedAt, _ = time.Parse(tsLayout, updatedAt)
	return &cfg, nil
}

// GetAllSLOConfigs returns all rows in slo_configs, ordered by model_id.
func (r *SQLiteRegistry) GetAllSLOConfigs(ctx context.Context) ([]*SLOConfigRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT model_id, ttft_ms, tbt_ms, target_compliance, updated_at
		FROM slo_configs
		ORDER BY model_id`)
	if err != nil {
		return nil, fmt.Errorf("registry: get_all_slo_configs: %w", err)
	}
	defer rows.Close()

	var cfgs []*SLOConfigRow
	for rows.Next() {
		var cfg SLOConfigRow
		var updatedAt string
		if err := rows.Scan(&cfg.ModelID, &cfg.TTFTMs, &cfg.TBTMs, &cfg.TargetCompliance, &updatedAt); err != nil {
			return nil, fmt.Errorf("registry: get_all_slo_configs: scan: %w", err)
		}
		cfg.UpdatedAt, _ = time.Parse(tsLayout, updatedAt)
		cfgs = append(cfgs, &cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get_all_slo_configs: %w", err)
	}
	if cfgs == nil {
		cfgs = []*SLOConfigRow{}
	}
	return cfgs, nil
}

// GetSLOComplianceByModel returns per-model TTFT compliance stats for the given
// time window. When modelID is non-empty, only that model is returned.
// thresholdMs is the TTFT SLO threshold: a request is "compliant" when its
// latency_ms is in the range (0, thresholdMs).
func (r *SQLiteRegistry) GetSLOComplianceByModel(ctx context.Context, start, end time.Time, modelID string, thresholdMs float64) ([]ModelSLOStat, error) {
	query := `
		SELECT
			model_id,
			COUNT(*)                                                          AS request_count,
			SUM(CASE WHEN latency_ms > 0 AND latency_ms < ? THEN 1 ELSE 0 END) AS compliant_count
		FROM inference_audit_log
		WHERE timestamp BETWEEN ? AND ?`
	args := []any{thresholdMs, fmtTime(start), fmtTime(end)}

	if modelID != "" {
		query += " AND model_id = ?"
		args = append(args, modelID)
	}
	query += " GROUP BY model_id ORDER BY model_id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("registry: get_slo_compliance_by_model: %w", err)
	}
	defer rows.Close()

	var stats []ModelSLOStat
	for rows.Next() {
		var s ModelSLOStat
		if err := rows.Scan(&s.ModelID, &s.RequestCount, &s.CompliantCount); err != nil {
			return nil, fmt.Errorf("registry: get_slo_compliance_by_model: scan: %w", err)
		}
		s.PeriodStart = start
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get_slo_compliance_by_model: %w", err)
	}
	if stats == nil {
		stats = []ModelSLOStat{}
	}
	return stats, nil
}
