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
	required := approval.RequiredApprovals
	if required <= 0 {
		required = 1
	}
	var expiresAt any
	if approval.ExpiresAt != nil && !approval.ExpiresAt.IsZero() {
		expiresAt = fmtTime(*approval.ExpiresAt)
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO deployment_approvals
		    (deployment_id, model_id, requester, requested_at, status, required_approvals, expires_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?)`,
		approval.DeploymentID, approval.ModelID, approval.Requester,
		fmtTime(approval.RequestedAt), required, expiresAt)
	if err != nil {
		return fmt.Errorf("registry: request deployment approval %q: %w", approval.DeploymentID, err)
	}
	id, err := res.LastInsertId()
	if err == nil {
		approval.ID = id
	}
	approval.Status = "pending"
	approval.RequiredApprovals = required
	return nil
}

func scanApproval(s interface{ Scan(...any) error }) (*DeploymentApproval, error) {
	var (
		a                 DeploymentApproval
		reviewer          sql.NullString
		reviewedAt        sql.NullString
		notes             sql.NullString
		requestedAt       sql.NullString
		requiredApprovals sql.NullInt64
		expiresAt         sql.NullString
	)
	if err := s.Scan(
		&a.ID, &a.DeploymentID, &a.ModelID, &a.Requester,
		&requestedAt, &a.Status,
		&reviewer, &reviewedAt, &notes,
		&requiredApprovals, &expiresAt,
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
	if requiredApprovals.Valid && requiredApprovals.Int64 > 0 {
		a.RequiredApprovals = int(requiredApprovals.Int64)
	} else {
		a.RequiredApprovals = 1
	}
	if expiresAt.Valid && expiresAt.String != "" {
		t := parseTime(expiresAt)
		if !t.IsZero() {
			a.ExpiresAt = &t
		}
	}
	return &a, nil
}

const approvalCols = `id, deployment_id, model_id, requester, requested_at, status, reviewer, reviewed_at, notes, required_approvals, expires_at`

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

// RecordApprovalVote records reviewer's vote on the approval for deploymentID.
// Errors on self-approval (reviewer == requester), duplicate vote (UNIQUE
// constraint on (approval_id, reviewer)), and when the approval is not pending.
func (r *SQLiteRegistry) RecordApprovalVote(ctx context.Context, deploymentID, reviewer, vote, notes, ipAddress string) error {
	// Load the approval to get the ID and verify it is pending.
	approval, err := r.GetDeploymentApproval(ctx, deploymentID)
	if err != nil {
		return err
	}
	if approval.Status != "pending" {
		return fmt.Errorf("registry: approval %q is not pending (status: %s)", deploymentID, approval.Status)
	}
	if approval.Requester == reviewer {
		return fmt.Errorf("registry: self_approval_denied: reviewer %q is also the requester", reviewer)
	}

	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO deployment_approval_votes
		    (approval_id, reviewer, voted_at, vote, notes, ip_address)
		VALUES (?, ?, ?, ?, ?, ?)`,
		approval.ID, reviewer, fmtTime(now), vote, notes, ipAddress)
	if err != nil {
		// SQLite returns "UNIQUE constraint failed" on duplicate votes.
		return fmt.Errorf("registry: record approval vote %q: %w", deploymentID, err)
	}
	return nil
}

// GetApprovalVotes returns all votes cast for approvalID, ordered by voted_at.
func (r *SQLiteRegistry) GetApprovalVotes(ctx context.Context, approvalID int64) ([]ApprovalVote, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, approval_id, reviewer, voted_at, vote, notes, ip_address
		FROM deployment_approval_votes
		WHERE approval_id = ?
		ORDER BY voted_at ASC`,
		approvalID)
	if err != nil {
		return nil, fmt.Errorf("registry: get approval votes %d: %w", approvalID, err)
	}
	defer rows.Close()
	var out []ApprovalVote
	for rows.Next() {
		var (
			v       ApprovalVote
			votedAt sql.NullString
		)
		if err := rows.Scan(&v.ID, &v.ApprovalID, &v.Reviewer, &votedAt, &v.Vote, &v.Notes, &v.IPAddress); err != nil {
			return nil, fmt.Errorf("registry: get approval votes %d: %w", approvalID, err)
		}
		v.VotedAt = parseTime(votedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

// CheckApprovalQuorum returns (reached, approvedCount, requiredCount, error)
// for the approval associated with deploymentID.
func (r *SQLiteRegistry) CheckApprovalQuorum(ctx context.Context, deploymentID string) (bool, int, int, error) {
	approval, err := r.GetDeploymentApproval(ctx, deploymentID)
	if err != nil {
		return false, 0, 0, err
	}
	var count int
	row := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM deployment_approval_votes
		WHERE approval_id = ? AND vote = 'approved'`,
		approval.ID)
	if err := row.Scan(&count); err != nil {
		return false, 0, 0, fmt.Errorf("registry: check approval quorum %q: %w", deploymentID, err)
	}
	required := approval.RequiredApprovals
	if required <= 0 {
		required = 1
	}
	return count >= required, count, required, nil
}
