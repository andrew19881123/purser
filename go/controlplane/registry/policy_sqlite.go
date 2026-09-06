package registry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// --- Policies (OPA/Rego) ---------------------------------------------------

const policyCols = `id, name, rego, enabled, created_at, updated_at`

func scanPolicy(s interface{ Scan(...any) error }) (*Policy, error) {
	var (
		p         Policy
		enabled   int
		createdAt sql.NullString
		updatedAt sql.NullString
	)
	if err := s.Scan(&p.ID, &p.Name, &p.Rego, &enabled, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	return &p, nil
}

// UpsertPolicy inserts a new policy or replaces an existing one (matched by
// name). updated_at is always set to now; created_at is preserved on updates.
func (r *SQLiteRegistry) UpsertPolicy(ctx context.Context, p *Policy) error {
	now := time.Now().UTC()
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO policies (name, rego, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			rego       = excluded.rego,
			enabled    = excluded.enabled,
			updated_at = excluded.updated_at`,
		p.Name, p.Rego, enabled, fmtTime(now), fmtTime(now))
	if err != nil {
		return fmt.Errorf("registry: upsert policy %q: %w", p.Name, err)
	}
	// Refresh the struct with server-assigned values.
	row := r.db.QueryRowContext(ctx, `SELECT `+policyCols+` FROM policies WHERE name=?`, p.Name)
	updated, err := scanPolicy(row)
	if err != nil {
		return fmt.Errorf("registry: upsert policy %q: re-read: %w", p.Name, err)
	}
	*p = *updated
	return nil
}

// GetPolicy returns the policy with the given name, or ErrNotFound.
func (r *SQLiteRegistry) GetPolicy(ctx context.Context, name string) (*Policy, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+policyCols+` FROM policies WHERE name=?`, name)
	p, err := scanPolicy(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get policy %q: %w", name, err)
	}
	return p, nil
}

// ListPolicies returns all stored policies ordered by name.
func (r *SQLiteRegistry) ListPolicies(ctx context.Context) ([]*Policy, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+policyCols+` FROM policies ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("registry: list policies: %w", err)
	}
	defer rows.Close()
	var out []*Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list policies: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePolicy removes the policy with the given name. Returns ErrNotFound
// when no such policy exists.
func (r *SQLiteRegistry) DeletePolicy(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM policies WHERE name=?`, name)
	if err != nil {
		return fmt.Errorf("registry: delete policy %q: %w", name, err)
	}
	return mustAffect(res, "policy", name)
}
