package registry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// --- Deployment approvals (AI Act Art.14 human oversight) ------------------

func (r *SQLiteRegistry) RequestDeploymentApproval(ctx context.Context, approval *DeploymentApproval) error {
	if approval.RequestedAt.IsZero() {
		approval.RequestedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO deployment_approvals
		    (deployment_id, model_id, requester, requested_at, status)
		VALUES (?, ?, ?, ?, 'pending')`,
		approval.DeploymentID, approval.ModelID, approval.Requester,
		fmtTime(approval.RequestedAt))
	if err != nil {
		return fmt.Errorf("registry: request deployment approval %q: %w", approval.DeploymentID, err)
	}
	id, err := res.LastInsertId()
	if err == nil {
		approval.ID = id
	}
	approval.Status = "pending"
	return nil
}

func scanApproval(s interface{ Scan(...any) error }) (*DeploymentApproval, error) {
	var (
		a           DeploymentApproval
		reviewer    sql.NullString
		reviewedAt  sql.NullString
		notes       sql.NullString
		requestedAt sql.NullString
	)
	if err := s.Scan(
		&a.ID, &a.DeploymentID, &a.ModelID, &a.Requester,
		&requestedAt, &a.Status,
		&reviewer, &reviewedAt, &notes,
	); err != nil {
		return nil, err
	}
	a.RequestedAt = parseTime(requestedAt)
	if reviewer.Valid {
		a.Reviewer = reviewer.String
	}
	if reviewedAt.Valid && reviewedAt.String != "" {
		t := parseTime(reviewedAt)
		if !t.IsZero() {
			a.ReviewedAt = &t
		}
	}
	if notes.Valid {
		a.Notes = notes.String
	}
	return &a, nil
}

const approvalCols = `id, deployment_id, model_id, requester, requested_at, status, reviewer, reviewed_at, notes`

func (r *SQLiteRegistry) GetDeploymentApproval(ctx context.Context, deploymentID string) (*DeploymentApproval, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+approvalCols+` FROM deployment_approvals WHERE deployment_id = ?`,
		deploymentID)
	a, err := scanApproval(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get deployment approval %q: %w", deploymentID, err)
	}
	return a, nil
}

func (r *SQLiteRegistry) ListDeploymentApprovals(ctx context.Context, status string, limit int) ([]*DeploymentApproval, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+approvalCols+` FROM deployment_approvals ORDER BY requested_at DESC LIMIT ?`,
			limit)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+approvalCols+` FROM deployment_approvals WHERE status = ? ORDER BY requested_at DESC LIMIT ?`,
			status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("registry: list deployment approvals: %w", err)
	}
	defer rows.Close()
	var out []*DeploymentApproval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list deployment approvals: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateDeploymentApprovalStatus(ctx context.Context, deploymentID, status, reviewer, notes string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE deployment_approvals
		SET status = ?, reviewer = ?, reviewed_at = ?, notes = ?
		WHERE deployment_id = ?`,
		status, reviewer, fmtTime(now), notes, deploymentID)
	if err != nil {
		return fmt.Errorf("registry: update deployment approval %q: %w", deploymentID, err)
	}
	return mustAffect(res, "deployment_approval", deploymentID)
}
