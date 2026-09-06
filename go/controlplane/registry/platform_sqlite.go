package registry

// platform_sqlite.go — SQLiteRegistry implementations for the v0.4
// multi-tenant platform model: organizations, teams, users, org/team
// memberships, custom roles, node pools, pool quotas, and effective
// permissions. All methods follow the conventions established by sqlite.go:
// scanXxx helpers, xxxCols column-list constants, fmtTime/parseTime, and
// mustAffect for UPDATE/DELETE row-count checks.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── Organizations ────────────────────────────────────────────────────────────

const orgCols = `id, name, slug, description, created_at, updated_at`

func scanOrg(s interface{ Scan(...any) error }) (*Organization, error) {
	var (
		o       Organization
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&o.ID, &o.Name, &o.Slug, &o.Description, &created, &updated); err != nil {
		return nil, err
	}
	o.CreatedAt = parseTime(created)
	o.UpdatedAt = parseTime(updated)
	return &o, nil
}

func (r *SQLiteRegistry) CreateOrganization(ctx context.Context, org *Organization) error {
	now := nowUTC()
	if org.CreatedAt.IsZero() {
		org.CreatedAt = now
	}
	org.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO organizations (id, name, slug, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		org.ID, org.Name, org.Slug, org.Description,
		fmtTime(org.CreatedAt), fmtTime(org.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create organization %q: %w", org.ID, err)
	}
	return nil
}

func (r *SQLiteRegistry) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+orgCols+` FROM organizations WHERE id = ?`, id)
	o, err := scanOrg(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get organization %q: %w", id, err)
	}
	return o, nil
}

func (r *SQLiteRegistry) GetOrganizationBySlug(ctx context.Context, slug string) (*Organization, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+orgCols+` FROM organizations WHERE slug = ?`, slug)
	o, err := scanOrg(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get organization by slug %q: %w", slug, err)
	}
	return o, nil
}

func (r *SQLiteRegistry) ListOrganizations(ctx context.Context) ([]*Organization, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+orgCols+` FROM organizations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("registry: list organizations: %w", err)
	}
	defer rows.Close()
	var out []*Organization
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list organizations: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateOrganization(ctx context.Context, org *Organization) error {
	org.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE organizations SET name=?, slug=?, description=?, updated_at=?
		WHERE id=?`,
		org.Name, org.Slug, org.Description, fmtTime(org.UpdatedAt), org.ID)
	if err != nil {
		return fmt.Errorf("registry: update organization %q: %w", org.ID, err)
	}
	return mustAffect(res, "organization", org.ID)
}

func (r *SQLiteRegistry) DeleteOrganization(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM organizations WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete organization %q: %w", id, err)
	}
	return mustAffect(res, "organization", id)
}

// ─── Teams ────────────────────────────────────────────────────────────────────

const teamCols = `id, org_id, name, slug, description, created_at, updated_at`

func scanTeam(s interface{ Scan(...any) error }) (*Team, error) {
	var (
		t       Team
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&t.ID, &t.OrgID, &t.Name, &t.Slug, &t.Description, &created, &updated); err != nil {
		return nil, err
	}
	t.CreatedAt = parseTime(created)
	t.UpdatedAt = parseTime(updated)
	return &t, nil
}

func (r *SQLiteRegistry) CreateTeam(ctx context.Context, team *Team) error {
	now := nowUTC()
	if team.CreatedAt.IsZero() {
		team.CreatedAt = now
	}
	team.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO teams (id, org_id, name, slug, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		team.ID, team.OrgID, team.Name, team.Slug, team.Description,
		fmtTime(team.CreatedAt), fmtTime(team.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create team %q: %w", team.ID, err)
	}
	return nil
}

func (r *SQLiteRegistry) GetTeam(ctx context.Context, id string) (*Team, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+teamCols+` FROM teams WHERE id = ?`, id)
	t, err := scanTeam(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get team %q: %w", id, err)
	}
	return t, nil
}

func (r *SQLiteRegistry) GetTeamBySlug(ctx context.Context, orgID, slug string) (*Team, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+teamCols+` FROM teams WHERE org_id = ? AND slug = ?`, orgID, slug)
	t, err := scanTeam(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get team by slug %q/%q: %w", orgID, slug, err)
	}
	return t, nil
}

func (r *SQLiteRegistry) ListTeamsByOrg(ctx context.Context, orgID string) ([]*Team, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+teamCols+` FROM teams WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("registry: list teams by org %q: %w", orgID, err)
	}
	defer rows.Close()
	var out []*Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list teams by org %q: %w", orgID, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateTeam(ctx context.Context, team *Team) error {
	team.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE teams SET name=?, slug=?, description=?, updated_at=?
		WHERE id=?`,
		team.Name, team.Slug, team.Description, fmtTime(team.UpdatedAt), team.ID)
	if err != nil {
		return fmt.Errorf("registry: update team %q: %w", team.ID, err)
	}
	return mustAffect(res, "team", team.ID)
}

func (r *SQLiteRegistry) DeleteTeam(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM teams WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete team %q: %w", id, err)
	}
	return mustAffect(res, "team", id)
}

// ─── Users ────────────────────────────────────────────────────────────────────

const userCols = `id, email, display_name, auth_method, created_at, last_seen_at`

func scanUser(s interface{ Scan(...any) error }) (*PlatformUser, error) {
	var (
		u          PlatformUser
		created    sql.NullString
		lastSeenAt sql.NullString
	)
	if err := s.Scan(&u.ID, &u.Email, &u.DisplayName, &u.AuthMethod, &created, &lastSeenAt); err != nil {
		return nil, err
	}
	u.CreatedAt = parseTime(created)
	if t := parseTime(lastSeenAt); !t.IsZero() {
		u.LastSeenAt = &t
	}
	return &u, nil
}

// UpsertPlatformUser inserts a new user or updates display_name and
// auth_method if the user already exists (e.g. on repeated logins).
func (r *SQLiteRegistry) UpsertPlatformUser(ctx context.Context, u *PlatformUser) error {
	now := nowUTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, auth_method, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			email        = excluded.email,
			display_name = excluded.display_name,
			auth_method  = excluded.auth_method`,
		u.ID, u.Email, u.DisplayName, u.AuthMethod,
		fmtTime(u.CreatedAt), fmtNullTimePt(u.LastSeenAt))
	if err != nil {
		return fmt.Errorf("registry: upsert platform user %q: %w", u.ID, err)
	}
	return nil
}

func (r *SQLiteRegistry) GetPlatformUser(ctx context.Context, id string) (*PlatformUser, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get platform user %q: %w", id, err)
	}
	return u, nil
}

func (r *SQLiteRegistry) GetPlatformUserByEmail(ctx context.Context, email string) (*PlatformUser, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get platform user by email %q: %w", email, err)
	}
	return u, nil
}

func (r *SQLiteRegistry) ListPlatformUsers(ctx context.Context) ([]*PlatformUser, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("registry: list platform users: %w", err)
	}
	defer rows.Close()
	var out []*PlatformUser
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list platform users: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdatePlatformUserLastSeen(ctx context.Context, id string, at time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET last_seen_at=? WHERE id=?`, fmtTime(at), id)
	if err != nil {
		return fmt.Errorf("registry: update user last_seen %q: %w", id, err)
	}
	return mustAffect(res, "user", id)
}

// ─── Org memberships ──────────────────────────────────────────────────────────

const orgMemberCols = `id, org_id, user_id, role, invited_by, created_at`

func scanOrgMember(s interface{ Scan(...any) error }) (*OrgMember, error) {
	var (
		m       OrgMember
		created sql.NullString
	)
	if err := s.Scan(&m.ID, &m.OrgID, &m.UserID, &m.Role, &m.InvitedBy, &created); err != nil {
		return nil, err
	}
	m.CreatedAt = parseTime(created)
	return &m, nil
}

// AddOrgMember inserts an org membership; if one already exists for the same
// (org_id, user_id) pair the existing row is preserved (upsert-or-ignore
// semantics so callers are idempotent).
func (r *SQLiteRegistry) AddOrgMember(ctx context.Context, m *OrgMember) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = nowUTC()
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO org_members (org_id, user_id, role, invited_by, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(org_id, user_id) DO NOTHING`,
		m.OrgID, m.UserID, m.Role, m.InvitedBy, fmtTime(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("registry: add org member %q/%q: %w", m.OrgID, m.UserID, err)
	}
	if id, err := res.LastInsertId(); err == nil && id != 0 {
		m.ID = id
	}
	return nil
}

func (r *SQLiteRegistry) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM org_members WHERE org_id=? AND user_id=?`, orgID, userID)
	if err != nil {
		return fmt.Errorf("registry: remove org member %q/%q: %w", orgID, userID, err)
	}
	return mustAffect(res, "org_member", orgID+"/"+userID)
}

func (r *SQLiteRegistry) GetOrgMember(ctx context.Context, orgID, userID string) (*OrgMember, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+orgMemberCols+` FROM org_members WHERE org_id=? AND user_id=?`, orgID, userID)
	m, err := scanOrgMember(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get org member %q/%q: %w", orgID, userID, err)
	}
	return m, nil
}

func (r *SQLiteRegistry) ListOrgMembers(ctx context.Context, orgID string) ([]*OrgMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+orgMemberCols+` FROM org_members WHERE org_id=? ORDER BY created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("registry: list org members %q: %w", orgID, err)
	}
	defer rows.Close()
	var out []*OrgMember
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list org members %q: %w", orgID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) ListUserOrgs(ctx context.Context, userID string) ([]*OrgMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+orgMemberCols+` FROM org_members WHERE user_id=? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("registry: list user orgs %q: %w", userID, err)
	}
	defer rows.Close()
	var out []*OrgMember
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list user orgs %q: %w", userID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ─── Team memberships ─────────────────────────────────────────────────────────

const teamMemberCols = `id, team_id, user_id, role_id, invited_by, created_at`

func scanTeamMember(s interface{ Scan(...any) error }) (*TeamMember, error) {
	var (
		m       TeamMember
		created sql.NullString
	)
	if err := s.Scan(&m.ID, &m.TeamID, &m.UserID, &m.RoleID, &m.InvitedBy, &created); err != nil {
		return nil, err
	}
	m.CreatedAt = parseTime(created)
	return &m, nil
}

func (r *SQLiteRegistry) AddTeamMember(ctx context.Context, m *TeamMember) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = nowUTC()
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO team_members (team_id, user_id, role_id, invited_by, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(team_id, user_id) DO UPDATE SET role_id=excluded.role_id`,
		m.TeamID, m.UserID, m.RoleID, m.InvitedBy, fmtTime(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("registry: add team member %q/%q: %w", m.TeamID, m.UserID, err)
	}
	if id, err := res.LastInsertId(); err == nil && id != 0 {
		m.ID = id
	}
	return nil
}

func (r *SQLiteRegistry) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM team_members WHERE team_id=? AND user_id=?`, teamID, userID)
	if err != nil {
		return fmt.Errorf("registry: remove team member %q/%q: %w", teamID, userID, err)
	}
	return mustAffect(res, "team_member", teamID+"/"+userID)
}

func (r *SQLiteRegistry) GetTeamMember(ctx context.Context, teamID, userID string) (*TeamMember, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+teamMemberCols+` FROM team_members WHERE team_id=? AND user_id=?`, teamID, userID)
	m, err := scanTeamMember(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get team member %q/%q: %w", teamID, userID, err)
	}
	return m, nil
}

func (r *SQLiteRegistry) ListTeamMembers(ctx context.Context, teamID string) ([]*TeamMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+teamMemberCols+` FROM team_members WHERE team_id=? ORDER BY created_at`, teamID)
	if err != nil {
		return nil, fmt.Errorf("registry: list team members %q: %w", teamID, err)
	}
	defer rows.Close()
	var out []*TeamMember
	for rows.Next() {
		m, err := scanTeamMember(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list team members %q: %w", teamID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) ListUserTeams(ctx context.Context, userID string) ([]*TeamMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+teamMemberCols+` FROM team_members WHERE user_id=? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("registry: list user teams %q: %w", userID, err)
	}
	defer rows.Close()
	var out []*TeamMember
	for rows.Next() {
		m, err := scanTeamMember(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list user teams %q: %w", userID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ─── Custom roles ─────────────────────────────────────────────────────────────

const roleCols = `id, org_id, name, description, permissions, is_system, created_at, updated_at`

func scanRole(s interface{ Scan(...any) error }) (*CustomRole, error) {
	var (
		r           CustomRole
		orgID       sql.NullString
		permissions string
		isSystem    int64
		created     sql.NullString
		updated     sql.NullString
	)
	if err := s.Scan(&r.ID, &orgID, &r.Name, &r.Description,
		&permissions, &isSystem, &created, &updated); err != nil {
		return nil, err
	}
	r.OrgID = orgID.String
	r.IsSystem = isSystem != 0
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	if permissions != "" && permissions != "[]" {
		_ = json.Unmarshal([]byte(permissions), &r.Permissions)
	}
	if r.Permissions == nil {
		r.Permissions = []string{}
	}
	return &r, nil
}

// permissionsJSON encodes a slice of permission strings as compact JSON.
func permissionsJSON(perms []string) string {
	if len(perms) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(perms)
	return string(b)
}

func (r *SQLiteRegistry) CreateCustomRole(ctx context.Context, role *CustomRole) error {
	now := nowUTC()
	if role.CreatedAt.IsZero() {
		role.CreatedAt = now
	}
	role.UpdatedAt = now
	var orgIDVal any
	if role.OrgID != "" {
		orgIDVal = role.OrgID
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO custom_roles (id, org_id, name, description, permissions, is_system, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		role.ID, orgIDVal, role.Name, role.Description,
		permissionsJSON(role.Permissions), boolToInt(role.IsSystem),
		fmtTime(role.CreatedAt), fmtTime(role.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create custom role %q: %w", role.ID, err)
	}
	return nil
}

func (r *SQLiteRegistry) GetCustomRole(ctx context.Context, id string) (*CustomRole, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+roleCols+` FROM custom_roles WHERE id = ?`, id)
	role, err := scanRole(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get custom role %q: %w", id, err)
	}
	return role, nil
}

// ListCustomRoles returns all roles for the given orgID, plus all platform
// built-in roles (org_id IS NULL). Pass "" to get only platform roles.
func (r *SQLiteRegistry) ListCustomRoles(ctx context.Context, orgID string) ([]*CustomRole, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if orgID == "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+roleCols+` FROM custom_roles WHERE org_id IS NULL ORDER BY name`)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+roleCols+` FROM custom_roles WHERE org_id = ? OR org_id IS NULL ORDER BY org_id NULLS FIRST, name`,
			orgID)
	}
	if err != nil {
		return nil, fmt.Errorf("registry: list custom roles: %w", err)
	}
	defer rows.Close()
	var out []*CustomRole
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list custom roles: %w", err)
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateCustomRole(ctx context.Context, role *CustomRole) error {
	role.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE custom_roles SET name=?, description=?, permissions=?, updated_at=?
		WHERE id=?`,
		role.Name, role.Description, permissionsJSON(role.Permissions),
		fmtTime(role.UpdatedAt), role.ID)
	if err != nil {
		return fmt.Errorf("registry: update custom role %q: %w", role.ID, err)
	}
	return mustAffect(res, "custom_role", role.ID)
}

// DeleteCustomRole removes a role. System roles (is_system=1) cannot be deleted.
func (r *SQLiteRegistry) DeleteCustomRole(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM custom_roles WHERE id=? AND is_system=0`, id)
	if err != nil {
		return fmt.Errorf("registry: delete custom role %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("registry: delete custom role %q: rows affected: %w", id, err)
	}
	if n == 0 {
		// Either not found or is_system — distinguish by existence
		var dummy string
		qErr := r.db.QueryRowContext(ctx, `SELECT id FROM custom_roles WHERE id=?`, id).Scan(&dummy)
		if qErr == sql.ErrNoRows {
			return ErrNotFound
		}
		return fmt.Errorf("registry: delete custom role %q: cannot delete system role", id)
	}
	return nil
}

// SeedSystemRoles inserts the six built-in platform roles if they are not
// already present. It is idempotent: running it twice is safe.
func (r *SQLiteRegistry) SeedSystemRoles(ctx context.Context) error {
	type seed struct {
		id          string
		name        string
		description string
		perms       []string
	}
	seeds := []seed{
		{
			id:          "platform_admin",
			name:        "Platform Admin",
			description: "Full platform administration access",
			perms: []string{
				PermPlatformOrgsCreate, PermPlatformOrgsDelete,
				PermPlatformPoolsManage, PermPlatformUsersInvite,
				PermOrgTeamsCreate, PermOrgTeamsDelete,
				PermOrgMembersInvite, PermOrgMembersRemove,
				PermOrgRolesCreate, PermOrgRolesDelete, PermOrgPoolsRequest,
				PermTeamModelsDeploy, PermTeamModelsUndeploy,
				PermTeamKeysCreate, PermTeamKeysRevoke,
				PermTeamMembersView, PermTeamMembersInvite, PermTeamMembersRemove,
				PermTeamMetricsView, PermTeamApprovalsView, PermTeamApprovalsReview,
				PermInferenceCall,
			},
		},
		{
			id:          "org_admin",
			name:        "Org Admin",
			description: "Full organization administration access",
			perms: []string{
				PermOrgTeamsCreate, PermOrgTeamsDelete,
				PermOrgMembersInvite, PermOrgMembersRemove,
				PermOrgRolesCreate, PermOrgRolesDelete, PermOrgPoolsRequest,
				PermTeamModelsDeploy, PermTeamModelsUndeploy,
				PermTeamKeysCreate, PermTeamKeysRevoke,
				PermTeamMembersView, PermTeamMembersInvite, PermTeamMembersRemove,
				PermTeamMetricsView, PermTeamApprovalsView, PermTeamApprovalsReview,
				PermInferenceCall,
			},
		},
		{
			id:          "team_admin",
			name:        "Team Admin",
			description: "Full team administration access",
			perms: []string{
				PermTeamModelsDeploy, PermTeamModelsUndeploy,
				PermTeamKeysCreate, PermTeamKeysRevoke,
				PermTeamMembersView, PermTeamMembersInvite, PermTeamMembersRemove,
				PermTeamMetricsView, PermTeamApprovalsView, PermTeamApprovalsReview,
				PermInferenceCall,
			},
		},
		{
			id:          "developer",
			name:        "Developer",
			description: "Deploy models, manage keys, run inference",
			perms: []string{
				PermTeamModelsDeploy, PermTeamModelsUndeploy,
				PermTeamKeysCreate,
				PermTeamMetricsView,
				PermInferenceCall,
			},
		},
		{
			id:          "viewer",
			name:        "Viewer",
			description: "Read-only access to team membership and metrics",
			perms: []string{
				PermTeamMembersView,
				PermTeamMetricsView,
			},
		},
		{
			id:          "inference_only",
			name:        "Inference Only",
			description: "May call inference endpoints only",
			perms:       []string{PermInferenceCall},
		},
	}

	now := fmtTime(nowUTC())
	for _, s := range seeds {
		_, err := r.db.ExecContext(ctx, `
			INSERT INTO custom_roles (id, org_id, name, description, permissions, is_system, created_at, updated_at)
			VALUES (?, NULL, ?, ?, ?, 1, ?, ?)
			ON CONFLICT(id) DO NOTHING`,
			s.id, s.name, s.description, permissionsJSON(s.perms), now, now)
		if err != nil {
			return fmt.Errorf("registry: seed system role %q: %w", s.id, err)
		}
	}
	return nil
}

// ─── Node pools ───────────────────────────────────────────────────────────────

const poolCols = `id, name, description, owner_type, owner_id, policy, created_at, updated_at`

func scanPool(s interface{ Scan(...any) error }) (*NodePool, error) {
	var (
		p       NodePool
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&p.ID, &p.Name, &p.Description,
		&p.OwnerType, &p.OwnerID, &p.Policy, &created, &updated); err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return &p, nil
}

func (r *SQLiteRegistry) CreateNodePool(ctx context.Context, p *NodePool) error {
	now := nowUTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO node_pools (id, name, description, owner_type, owner_id, policy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.OwnerType, p.OwnerID, p.Policy,
		fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create node pool %q: %w", p.ID, err)
	}
	return nil
}

func (r *SQLiteRegistry) GetNodePool(ctx context.Context, id string) (*NodePool, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+poolCols+` FROM node_pools WHERE id = ?`, id)
	p, err := scanPool(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get node pool %q: %w", id, err)
	}
	return p, nil
}

func (r *SQLiteRegistry) ListNodePools(ctx context.Context) ([]*NodePool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+poolCols+` FROM node_pools ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("registry: list node pools: %w", err)
	}
	defer rows.Close()
	var out []*NodePool
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list node pools: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateNodePool(ctx context.Context, p *NodePool) error {
	p.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE node_pools SET name=?, description=?, owner_type=?, owner_id=?, policy=?, updated_at=?
		WHERE id=?`,
		p.Name, p.Description, p.OwnerType, p.OwnerID, p.Policy, fmtTime(p.UpdatedAt), p.ID)
	if err != nil {
		return fmt.Errorf("registry: update node pool %q: %w", p.ID, err)
	}
	return mustAffect(res, "node_pool", p.ID)
}

func (r *SQLiteRegistry) DeleteNodePool(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM node_pools WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete node pool %q: %w", id, err)
	}
	return mustAffect(res, "node_pool", id)
}

func (r *SQLiteRegistry) AddNodeToPool(ctx context.Context, nodeID, poolID string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO node_pool_members (node_id, pool_id)
		VALUES (?, ?)
		ON CONFLICT(node_id) DO UPDATE SET pool_id=excluded.pool_id`,
		nodeID, poolID)
	if err != nil {
		return fmt.Errorf("registry: add node %q to pool %q: %w", nodeID, poolID, err)
	}
	return nil
}

func (r *SQLiteRegistry) RemoveNodeFromPool(ctx context.Context, nodeID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM node_pool_members WHERE node_id=?`, nodeID)
	if err != nil {
		return fmt.Errorf("registry: remove node %q from pool: %w", nodeID, err)
	}
	return mustAffect(res, "node_pool_member", nodeID)
}

func (r *SQLiteRegistry) GetNodePool_ByNode(ctx context.Context, nodeID string) (*NodePool, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+poolCols+` FROM node_pools
		 JOIN node_pool_members ON node_pools.id = node_pool_members.pool_id
		 WHERE node_pool_members.node_id = ?`, nodeID)
	p, err := scanPool(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get pool by node %q: %w", nodeID, err)
	}
	return p, nil
}

func (r *SQLiteRegistry) ListNodesInPool(ctx context.Context, poolID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT node_id FROM node_pool_members WHERE pool_id=? ORDER BY node_id`, poolID)
	if err != nil {
		return nil, fmt.Errorf("registry: list nodes in pool %q: %w", poolID, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("registry: list nodes in pool %q: %w", poolID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetAllowedNodeIDs returns all node IDs accessible to teamID:
//  1. Nodes in the exclusive pool owned by this team (owner_type='team', owner_id=teamID)
//  2. Nodes in shared pools where the team has a quota record
//
// Deduplication ensures no node appears twice.
func (r *SQLiteRegistry) GetAllowedNodeIDs(ctx context.Context, teamID string) ([]string, error) {
	// Query 1: nodes in the team's own exclusive pool
	ownRows, err := r.db.QueryContext(ctx, `
		SELECT npm.node_id
		FROM node_pool_members npm
		JOIN node_pools p ON p.id = npm.pool_id
		WHERE p.owner_type = 'team' AND p.owner_id = ? AND p.policy = 'exclusive'`,
		teamID)
	if err != nil {
		return nil, fmt.Errorf("registry: get allowed nodes (exclusive) for team %q: %w", teamID, err)
	}
	seen := make(map[string]struct{})
	var ids []string
	for ownRows.Next() {
		var id string
		if err := ownRows.Scan(&id); err != nil {
			ownRows.Close()
			return nil, fmt.Errorf("registry: get allowed nodes (exclusive) for team %q: %w", teamID, err)
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if err := ownRows.Err(); err != nil {
		ownRows.Close()
		return nil, fmt.Errorf("registry: get allowed nodes (exclusive) for team %q: %w", teamID, err)
	}
	ownRows.Close()

	// Query 2: nodes in shared pools where the team has a quota
	sharedRows, err := r.db.QueryContext(ctx, `
		SELECT npm.node_id
		FROM node_pool_members npm
		JOIN node_pools p ON p.id = npm.pool_id
		JOIN pool_team_quotas q ON q.pool_id = p.id
		WHERE p.policy = 'shared' AND q.team_id = ?`,
		teamID)
	if err != nil {
		return nil, fmt.Errorf("registry: get allowed nodes (shared) for team %q: %w", teamID, err)
	}
	defer sharedRows.Close()
	for sharedRows.Next() {
		var id string
		if err := sharedRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("registry: get allowed nodes (shared) for team %q: %w", teamID, err)
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if err := sharedRows.Err(); err != nil {
		return nil, fmt.Errorf("registry: get allowed nodes (shared) for team %q: %w", teamID, err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// ─── Pool team quotas ─────────────────────────────────────────────────────────

const quotaCols = `pool_id, team_id, max_deployments, max_gpu_nodes, priority, created_at, updated_at`

func scanQuota(s interface{ Scan(...any) error }) (*PoolTeamQuota, error) {
	var (
		q       PoolTeamQuota
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&q.PoolID, &q.TeamID, &q.MaxDeployments, &q.MaxGPUNodes,
		&q.Priority, &created, &updated); err != nil {
		return nil, err
	}
	q.CreatedAt = parseTime(created)
	q.UpdatedAt = parseTime(updated)
	return &q, nil
}

func (r *SQLiteRegistry) UpsertPoolTeamQuota(ctx context.Context, q *PoolTeamQuota) error {
	now := nowUTC()
	if q.CreatedAt.IsZero() {
		q.CreatedAt = now
	}
	q.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pool_team_quotas (pool_id, team_id, max_deployments, max_gpu_nodes, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pool_id, team_id) DO UPDATE SET
			max_deployments = excluded.max_deployments,
			max_gpu_nodes   = excluded.max_gpu_nodes,
			priority        = excluded.priority,
			updated_at      = excluded.updated_at`,
		q.PoolID, q.TeamID, q.MaxDeployments, q.MaxGPUNodes, q.Priority,
		fmtTime(q.CreatedAt), fmtTime(q.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: upsert pool team quota %q/%q: %w", q.PoolID, q.TeamID, err)
	}
	return nil
}

func (r *SQLiteRegistry) GetPoolTeamQuota(ctx context.Context, poolID, teamID string) (*PoolTeamQuota, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+quotaCols+` FROM pool_team_quotas WHERE pool_id=? AND team_id=?`, poolID, teamID)
	q, err := scanQuota(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get pool team quota %q/%q: %w", poolID, teamID, err)
	}
	return q, nil
}

func (r *SQLiteRegistry) ListPoolTeamQuotas(ctx context.Context, poolID string) ([]*PoolTeamQuota, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+quotaCols+` FROM pool_team_quotas WHERE pool_id=? ORDER BY priority, team_id`, poolID)
	if err != nil {
		return nil, fmt.Errorf("registry: list pool team quotas %q: %w", poolID, err)
	}
	defer rows.Close()
	var out []*PoolTeamQuota
	for rows.Next() {
		q, err := scanQuota(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list pool team quotas %q: %w", poolID, err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) DeletePoolTeamQuota(ctx context.Context, poolID, teamID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pool_team_quotas WHERE pool_id=? AND team_id=?`, poolID, teamID)
	if err != nil {
		return fmt.Errorf("registry: delete pool team quota %q/%q: %w", poolID, teamID, err)
	}
	return mustAffect(res, "pool_team_quota", poolID+"/"+teamID)
}

// ─── Effective permissions ────────────────────────────────────────────────────

// GetEffectivePermissions resolves the full permission set for userID in the
// context of teamID. It:
//  1. Looks up the team to get its org.
//  2. Checks whether the user is an org_admin (grants all org: + team: perms).
//  3. Reads the user's custom_role in the team.
//  4. Returns the union of org-admin + role permissions.
func (r *SQLiteRegistry) GetEffectivePermissions(ctx context.Context, userID, teamID string) (*EffectivePermissions, error) {
	// Resolve team → org
	team, err := r.GetTeam(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("registry: get effective permissions: %w", err)
	}

	ep := &EffectivePermissions{
		UserID: userID,
		TeamID: teamID,
		OrgID:  team.OrgID,
	}

	// Check org admin status
	orgMember, err := r.GetOrgMember(ctx, team.OrgID, userID)
	if err == nil && orgMember.Role == "org_admin" {
		ep.IsOrgAdmin = true
	}

	// Collect permissions from the user's team role
	permSet := make(map[string]struct{})

	if ep.IsOrgAdmin {
		// Org admins inherit all org: + team: permissions from the org_admin built-in
		orgAdminRole, err := r.GetCustomRole(ctx, "org_admin")
		if err == nil {
			for _, p := range orgAdminRole.Permissions {
				permSet[p] = struct{}{}
			}
		}
	}

	// Layer on the team-specific role (may differ from org_admin set)
	teamMember, err := r.GetTeamMember(ctx, teamID, userID)
	if err == nil && teamMember.RoleID != "" {
		role, err := r.GetCustomRole(ctx, teamMember.RoleID)
		if err == nil {
			for _, p := range role.Permissions {
				permSet[p] = struct{}{}
			}
		}
	}

	for p := range permSet {
		ep.Permissions = append(ep.Permissions, p)
	}
	// Sort for deterministic output
	sortStrings(ep.Permissions)
	return ep, nil
}

// sortStrings sorts a string slice in-place (avoids importing sort in a
// hot path by using a simple insertion sort for the small N typical here).
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		key := ss[i]
		j := i - 1
		for j >= 0 && ss[j] > key {
			ss[j+1] = ss[j]
			j--
		}
		ss[j+1] = key
	}
}

// hasPermission is a helper used by tests and callers to check membership.
func hasPermission(ep *EffectivePermissions, perm string) bool {
	for _, p := range ep.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// stringsContain reports whether needle is in haystack (used by tests).
func stringsContain(haystack []string, needle string) bool {
	return strings.Contains(strings.Join(haystack, "\n"), needle)
}
