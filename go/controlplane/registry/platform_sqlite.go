// platform_sqlite.go — SQLite implementations for v0.4 platform types:
// PlatformOrg, PlatformTeam, CustomRole, OrgMember, TeamMember.
//
// Schema tables are created by SQLiteRegistry.Migrate via createPlatformTables.
// All queries follow the same RFC3339Nano / JSON-blob conventions used in
// sqlite.go.
package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// createPlatformTables is called by SQLiteRegistry.Migrate to ensure all v0.4
// platform tables exist. The statements are idempotent (CREATE TABLE/INDEX IF
// NOT EXISTS) so they are safe to run on every startup against an existing DB.
func (r *SQLiteRegistry) createPlatformTables(ctx context.Context) error {
	stmts := []string{
		// platform_orgs: top-level organisational units.
		`CREATE TABLE IF NOT EXISTS platform_orgs (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`,

		// platform_teams: groups of users within an org.
		`CREATE TABLE IF NOT EXISTS platform_teams (
			id          TEXT PRIMARY KEY,
			org_id      TEXT NOT NULL,
			name        TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL,
			UNIQUE (org_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_platform_teams_org ON platform_teams(org_id)`,

		// custom_roles: named permission sets scoped to an org.
		`CREATE TABLE IF NOT EXISTS custom_roles (
			id          TEXT PRIMARY KEY,
			org_id      TEXT NOT NULL,
			name        TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			permissions TEXT NOT NULL DEFAULT '[]',
			is_system   INTEGER NOT NULL DEFAULT 0,
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL,
			UNIQUE (org_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_custom_roles_org ON custom_roles(org_id)`,

		// org_members: user memberships in platform_orgs.
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id    TEXT NOT NULL,
			user_sub  TEXT NOT NULL,
			role      TEXT NOT NULL DEFAULT 'member',
			joined_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_sub)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_org_members_user ON org_members(user_sub)`,

		// team_members: user memberships in platform_teams with an assigned role.
		`CREATE TABLE IF NOT EXISTS team_members (
			team_id   TEXT NOT NULL,
			user_sub  TEXT NOT NULL,
			role_id   TEXT NOT NULL DEFAULT '',
			joined_at TEXT NOT NULL,
			PRIMARY KEY (team_id, user_sub)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_sub)`,
	}
	for _, s := range stmts {
		if _, err := r.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("registry: createPlatformTables: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// PlatformOrg
// ---------------------------------------------------------------------------

func (r *SQLiteRegistry) UpsertPlatformOrg(ctx context.Context, org *PlatformOrg) error {
	now := fmtTime(nowUTC())
	if org.CreatedAt.IsZero() {
		org.CreatedAt = nowUTC()
	}
	org.UpdatedAt = nowUTC()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO platform_orgs (id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name        = excluded.name,
		   description = excluded.description,
		   updated_at  = ?`,
		org.ID, org.Name, org.Description,
		fmtTime(org.CreatedAt), fmtTime(org.UpdatedAt),
		now,
	)
	return err
}

func (r *SQLiteRegistry) GetPlatformOrg(ctx context.Context, id string) (*PlatformOrg, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at FROM platform_orgs WHERE id = ?`, id)
	return scanPlatformOrg(row)
}

func scanPlatformOrg(row *sql.Row) (*PlatformOrg, error) {
	var o PlatformOrg
	var ca, ua sql.NullString
	if err := row.Scan(&o.ID, &o.Name, &o.Description, &ca, &ua); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	o.CreatedAt = parseTime(ca)
	o.UpdatedAt = parseTime(ua)
	return &o, nil
}

// ---------------------------------------------------------------------------
// PlatformTeam
// ---------------------------------------------------------------------------

func (r *SQLiteRegistry) UpsertPlatformTeam(ctx context.Context, team *PlatformTeam) error {
	now := fmtTime(nowUTC())
	if team.CreatedAt.IsZero() {
		team.CreatedAt = nowUTC()
	}
	team.UpdatedAt = nowUTC()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO platform_teams (id, org_id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   org_id      = excluded.org_id,
		   name        = excluded.name,
		   description = excluded.description,
		   updated_at  = ?`,
		team.ID, team.OrgID, team.Name, team.Description,
		fmtTime(team.CreatedAt), fmtTime(team.UpdatedAt),
		now,
	)
	return err
}

func (r *SQLiteRegistry) GetPlatformTeam(ctx context.Context, id string) (*PlatformTeam, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, org_id, name, description, created_at, updated_at FROM platform_teams WHERE id = ?`, id)
	var t PlatformTeam
	var ca, ua sql.NullString
	if err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Description, &ca, &ua); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.CreatedAt = parseTime(ca)
	t.UpdatedAt = parseTime(ua)
	return &t, nil
}

// ---------------------------------------------------------------------------
// CustomRole
// ---------------------------------------------------------------------------

func (r *SQLiteRegistry) CreateCustomRole(ctx context.Context, role *CustomRole) error {
	if role.CreatedAt.IsZero() {
		role.CreatedAt = nowUTC()
	}
	role.UpdatedAt = role.CreatedAt
	permsJSON, err := marshalStringSlice(role.Permissions)
	if err != nil {
		return fmt.Errorf("registry: CreateCustomRole: marshal permissions: %w", err)
	}
	isSystem := 0
	if role.IsSystem {
		isSystem = 1
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO custom_roles (id, org_id, name, description, permissions, is_system, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		role.ID, role.OrgID, role.Name, role.Description, permsJSON, isSystem,
		fmtTime(role.CreatedAt), fmtTime(role.UpdatedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (r *SQLiteRegistry) GetCustomRole(ctx context.Context, orgID, roleID string) (*CustomRole, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, org_id, name, description, permissions, is_system, created_at, updated_at
		 FROM custom_roles WHERE org_id = ? AND id = ?`, orgID, roleID)
	return scanCustomRole(row)
}

func (r *SQLiteRegistry) ListCustomRoles(ctx context.Context, orgID string) ([]*CustomRole, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, org_id, name, description, permissions, is_system, created_at, updated_at
		 FROM custom_roles WHERE org_id = ? ORDER BY is_system DESC, name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []*CustomRole
	for rows.Next() {
		role, err := scanCustomRoleRows(rows)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	if roles == nil {
		roles = []*CustomRole{}
	}
	return roles, rows.Err()
}

func (r *SQLiteRegistry) UpdateCustomRole(ctx context.Context, role *CustomRole) error {
	role.UpdatedAt = nowUTC()
	permsJSON, err := marshalStringSlice(role.Permissions)
	if err != nil {
		return fmt.Errorf("registry: UpdateCustomRole: marshal permissions: %w", err)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE custom_roles SET name=?, description=?, permissions=?, updated_at=?
		 WHERE org_id=? AND id=?`,
		role.Name, role.Description, permsJSON, fmtTime(role.UpdatedAt),
		role.OrgID, role.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRegistry) DeleteCustomRole(ctx context.Context, orgID, roleID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM custom_roles WHERE org_id=? AND id=?`, orgID, roleID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRegistry) IsCustomRoleInUse(ctx context.Context, roleID string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_members WHERE role_id=?`, roleID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------
// OrgMember
// ---------------------------------------------------------------------------

func (r *SQLiteRegistry) ListOrgMembers(ctx context.Context, orgID string) ([]*OrgMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT org_id, user_sub, role, joined_at FROM org_members WHERE org_id=? ORDER BY joined_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []*OrgMember
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if members == nil {
		members = []*OrgMember{}
	}
	return members, rows.Err()
}

func (r *SQLiteRegistry) GetOrgMembershipsByUser(ctx context.Context, userSub string) ([]*OrgMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT org_id, user_sub, role, joined_at FROM org_members WHERE user_sub=?`, userSub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []*OrgMember
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if members == nil {
		members = []*OrgMember{}
	}
	return members, rows.Err()
}

func (r *SQLiteRegistry) UpsertOrgMember(ctx context.Context, m *OrgMember) error {
	if m.JoinedAt.IsZero() {
		m.JoinedAt = nowUTC()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_sub, role, joined_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(org_id, user_sub) DO UPDATE SET role=excluded.role`,
		m.OrgID, m.UserSub, m.Role, fmtTime(m.JoinedAt),
	)
	return err
}

// ---------------------------------------------------------------------------
// TeamMember
// ---------------------------------------------------------------------------

func (r *SQLiteRegistry) GetTeamMember(ctx context.Context, teamID, userSub string) (*TeamMember, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT team_id, user_sub, role_id, joined_at FROM team_members WHERE team_id=? AND user_sub=?`,
		teamID, userSub)
	var m TeamMember
	var ja sql.NullString
	if err := row.Scan(&m.TeamID, &m.UserSub, &m.RoleID, &ja); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.JoinedAt = parseTime(ja)
	return &m, nil
}

func (r *SQLiteRegistry) GetTeamMembershipsByUser(ctx context.Context, userSub string) ([]*TeamMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT team_id, user_sub, role_id, joined_at FROM team_members WHERE user_sub=?`, userSub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []*TeamMember
	for rows.Next() {
		m := &TeamMember{}
		var ja sql.NullString
		if err := rows.Scan(&m.TeamID, &m.UserSub, &m.RoleID, &ja); err != nil {
			return nil, err
		}
		m.JoinedAt = parseTime(ja)
		members = append(members, m)
	}
	if members == nil {
		members = []*TeamMember{}
	}
	return members, rows.Err()
}

func (r *SQLiteRegistry) UpsertTeamMember(ctx context.Context, m *TeamMember) error {
	if m.JoinedAt.IsZero() {
		m.JoinedAt = nowUTC()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO team_members (team_id, user_sub, role_id, joined_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(team_id, user_sub) DO UPDATE SET role_id=excluded.role_id`,
		m.TeamID, m.UserSub, m.RoleID, fmtTime(m.JoinedAt),
	)
	return err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func scanCustomRole(row *sql.Row) (*CustomRole, error) {
	var cr CustomRole
	var permsJSON string
	var isSystem int
	var ca, ua sql.NullString
	if err := row.Scan(&cr.ID, &cr.OrgID, &cr.Name, &cr.Description, &permsJSON, &isSystem, &ca, &ua); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cr.IsSystem = isSystem != 0
	cr.CreatedAt = parseTime(ca)
	cr.UpdatedAt = parseTime(ua)
	if err := json.Unmarshal([]byte(permsJSON), &cr.Permissions); err != nil {
		cr.Permissions = []string{}
	}
	if cr.Permissions == nil {
		cr.Permissions = []string{}
	}
	return &cr, nil
}

type customRoleScanner interface {
	Scan(dest ...any) error
}

func scanCustomRoleRows(rows customRoleScanner) (*CustomRole, error) {
	var cr CustomRole
	var permsJSON string
	var isSystem int
	var ca, ua sql.NullString
	if err := rows.Scan(&cr.ID, &cr.OrgID, &cr.Name, &cr.Description, &permsJSON, &isSystem, &ca, &ua); err != nil {
		return nil, err
	}
	cr.IsSystem = isSystem != 0
	cr.CreatedAt = parseTime(ca)
	cr.UpdatedAt = parseTime(ua)
	if err := json.Unmarshal([]byte(permsJSON), &cr.Permissions); err != nil {
		cr.Permissions = []string{}
	}
	if cr.Permissions == nil {
		cr.Permissions = []string{}
	}
	return &cr, nil
}

type orgMemberScanner interface {
	Scan(dest ...any) error
}

func scanOrgMember(s orgMemberScanner) (*OrgMember, error) {
	var m OrgMember
	var ja sql.NullString
	if err := s.Scan(&m.OrgID, &m.UserSub, &m.Role, &ja); err != nil {
		return nil, err
	}
	m.JoinedAt = parseTime(ja)
	return &m, nil
}

// marshalStringSlice encodes a (possibly nil) string slice as JSON.
func marshalStringSlice(ss []string) (string, error) {
	if ss == nil {
		ss = []string{}
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// isUniqueViolation detects a SQLite UNIQUE constraint error from the error message.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "unique") && strings.Contains(msg, "constraint")
}

// Ensure the parseTime helper can also parse a plain time.Time from a string
// by wrapping the zero-value check. parseTime is defined in sqlite.go; this
// file just uses it.
var _ = time.RFC3339Nano // keep import alive
