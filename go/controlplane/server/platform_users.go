// platform_users.go — REST handlers for platform users, custom roles, and
// effective-permissions discovery (v0.4+).
//
// Endpoints implemented here:
//
//	GET  /api/v1/platform/users            — list platform users  (admin only)
//	GET  /api/v1/platform/users/me         — current actor profile (any auth)
//	GET  /api/v1/platform/users/{id}       — user by ID            (admin or self)
//	POST /api/v1/platform/orgs/{orgId}/roles   — create custom role (org_admin)
//	GET  /api/v1/platform/orgs/{orgId}/roles   — list roles (any auth)
//	GET  /api/v1/platform/orgs/{orgId}/roles/{id} — get role
//	PUT  /api/v1/platform/orgs/{orgId}/roles/{id} — update role
//	DELETE /api/v1/platform/orgs/{orgId}/roles/{id} — delete role
//	GET  /api/v1/platform/permissions      — permission catalogue (any auth)
//	GET  /api/v1/platform/teams/{teamId}/my-permissions — effective perms
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/purser/purser/go/controlplane/permissions"
	"github.com/purser/purser/go/controlplane/registry"
)

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// handleListUsers returns a summary of all platform users, derived from the
// org_members table. Requires admin role.
//
// GET /api/v1/platform/users
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.isAdminActor(r) {
		s.writeError(w, http.StatusForbidden, "forbidden", "platform_admin role required")
		return
	}
	if s.reg == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"users": []any{}})
		return
	}
	// Aggregate unique user_subs from all org memberships.
	// A future Wave 3 release will add a proper users table backed by OIDC/LDAP.
	type userEntry struct {
		UserSub string `json:"user_sub"`
		OrgID   string `json:"org_id"`
		Role    string `json:"role"`
	}
	// For a quick listing we rely on GetOrgMembershipsByUser-in-reverse:
	// list a representative org and return its members.
	// Because there is no global "list all users" query yet, we use an
	// empty-org guard and return from org_members for the default org.
	var users []userEntry
	members, err := s.reg.ListOrgMembers(r.Context(), "default")
	if err != nil && !errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusInternalServerError, "list_users_failed", err.Error())
		return
	}
	for _, m := range members {
		users = append(users, userEntry{UserSub: m.UserSub, OrgID: m.OrgID, Role: m.Role})
	}
	if users == nil {
		users = []userEntry{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleGetMe returns the current actor's identity along with their org and
// team memberships. This is the "who am I?" endpoint that every client should
// call at login.
//
// GET /api/v1/platform/users/me
func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	actor := actorFromRequest(r)

	var orgMemberships []*registry.OrgMember
	var teamMemberships []*registry.TeamMember

	if s.reg != nil {
		var err error
		orgMemberships, err = s.reg.GetOrgMembershipsByUser(r.Context(), actor)
		if err != nil {
			orgMemberships = []*registry.OrgMember{}
		}
		teamMemberships, err = s.reg.GetTeamMembershipsByUser(r.Context(), actor)
		if err != nil {
			teamMemberships = []*registry.TeamMember{}
		}
	} else {
		orgMemberships = []*registry.OrgMember{}
		teamMemberships = []*registry.TeamMember{}
	}

	// Note: full user profile (name, email, avatar) requires OIDC/LDAP
	// integration (Wave 3). For now we expose what we know: the stable
	// actor string derived from the auth credential and the membership lists.
	s.writeJSON(w, http.StatusOK, map[string]any{
		"actor":                 actor,
		"orgs":                  orgMemberships,
		"teams":                 teamMemberships,
		"note":                  "full user profile requires OIDC/LDAP integration (Wave 3)",
		"service_accounts_note": "service_accounts are team-level credentials for machine-to-machine auth (LiteLLM, CI/CD)",
	})
}

// handleGetUser returns the profile and memberships of a specific user by ID.
// Requires admin role or the caller must be the same user (actor == id).
//
// GET /api/v1/platform/users/{id}
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	actor := actorFromRequest(r)
	if !s.isAdminActor(r) && actor != targetID {
		s.writeError(w, http.StatusForbidden, "forbidden", "platform_admin role or own-user access required")
		return
	}

	var orgMemberships []*registry.OrgMember
	var teamMemberships []*registry.TeamMember
	if s.reg != nil {
		var err error
		orgMemberships, err = s.reg.GetOrgMembershipsByUser(r.Context(), targetID)
		if err != nil {
			orgMemberships = []*registry.OrgMember{}
		}
		teamMemberships, err = s.reg.GetTeamMembershipsByUser(r.Context(), targetID)
		if err != nil {
			teamMemberships = []*registry.TeamMember{}
		}
	} else {
		orgMemberships = []*registry.OrgMember{}
		teamMemberships = []*registry.TeamMember{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"user_sub": targetID,
		"orgs":     orgMemberships,
		"teams":    teamMemberships,
	})
}

// ---------------------------------------------------------------------------
// Custom Roles
// ---------------------------------------------------------------------------

// handleCreateRole creates a new custom role within an org.
// Requires org_admin or platform admin role.
//
// POST /api/v1/platform/orgs/{orgId}/roles
func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if !s.isAdminActor(r) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}
	if s.reg == nil {
		s.writeError(w, http.StatusInternalServerError, "no_registry", "registry not configured")
		return
	}

	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "name required")
		return
	}
	if req.Permissions == nil {
		req.Permissions = []string{}
	}

	// Ensure the org exists (upsert a placeholder if it doesn't).
	_, err := s.reg.GetPlatformOrg(r.Context(), orgID)
	if errors.Is(err, registry.ErrNotFound) {
		if err2 := s.reg.UpsertPlatformOrg(r.Context(), &registry.PlatformOrg{
			ID:        orgID,
			Name:      orgID,
			CreatedAt: time.Now().UTC(),
		}); err2 != nil {
			s.writeError(w, http.StatusInternalServerError, "create_org_failed", err2.Error())
			return
		}
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_org_failed", err.Error())
		return
	}

	role := &registry.CustomRole{
		ID:          "role-" + randHex(8),
		OrgID:       orgID,
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
		IsSystem:    false,
	}
	if err := s.reg.CreateCustomRole(r.Context(), role); err != nil {
		if errors.Is(err, registry.ErrConflict) {
			s.writeError(w, http.StatusConflict, "role_name_conflict", "a role with that name already exists in this org")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_role_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "custom_role.created",
		Target: role.ID,
	})
	s.writeJSON(w, http.StatusCreated, role)
}

// handleListRoles returns all roles (system and custom) for an org.
//
// GET /api/v1/platform/orgs/{orgId}/roles
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if s.reg == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"roles": []*registry.CustomRole{}})
		return
	}
	roles, err := s.reg.ListCustomRoles(r.Context(), orgID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_roles_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

// handleGetRole returns a single custom role by (orgId, id).
//
// GET /api/v1/platform/orgs/{orgId}/roles/{id}
func (s *Server) handleGetRole(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	roleID := r.PathValue("id")
	if s.reg == nil {
		s.writeError(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	role, err := s.reg.GetCustomRole(r.Context(), orgID, roleID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_role_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, role)
}

// handleUpdateRole replaces the mutable fields of a custom role. System roles
// (is_system=true) cannot be modified.
//
// PUT /api/v1/platform/orgs/{orgId}/roles/{id}
func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	roleID := r.PathValue("id")
	if !s.isAdminActor(r) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}
	if s.reg == nil {
		s.writeError(w, http.StatusInternalServerError, "no_registry", "registry not configured")
		return
	}

	existing, err := s.reg.GetCustomRole(r.Context(), orgID, roleID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_role_failed", err.Error())
		return
	}
	if existing.IsSystem {
		s.writeError(w, http.StatusConflict, "system_role", "system roles cannot be modified")
		return
	}

	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Name == "" {
		req.Name = existing.Name
	}
	if req.Permissions == nil {
		req.Permissions = existing.Permissions
	}

	updated := &registry.CustomRole{
		ID:          roleID,
		OrgID:       orgID,
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
	}
	if err := s.reg.UpdateCustomRole(r.Context(), updated); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "role not found")
			return
		}
		if errors.Is(err, registry.ErrConflict) {
			s.writeError(w, http.StatusConflict, "role_name_conflict", "a role with that name already exists in this org")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "update_role_failed", err.Error())
		return
	}
	// Re-fetch to return the updated record with timestamps.
	role, _ := s.reg.GetCustomRole(r.Context(), orgID, roleID)
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "custom_role.updated",
		Target: roleID,
	})
	s.writeJSON(w, http.StatusOK, role)
}

// handleDeleteRole removes a custom role. Returns 409 when the role is a
// system role or is currently assigned to at least one team member.
//
// DELETE /api/v1/platform/orgs/{orgId}/roles/{id}
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	roleID := r.PathValue("id")
	if !s.isAdminActor(r) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}
	if s.reg == nil {
		s.writeError(w, http.StatusInternalServerError, "no_registry", "registry not configured")
		return
	}

	existing, err := s.reg.GetCustomRole(r.Context(), orgID, roleID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_role_failed", err.Error())
		return
	}
	if existing.IsSystem {
		s.writeError(w, http.StatusConflict, "system_role", "system roles cannot be deleted")
		return
	}
	inUse, err := s.reg.IsCustomRoleInUse(r.Context(), roleID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "check_role_usage_failed", err.Error())
		return
	}
	if inUse {
		s.writeError(w, http.StatusConflict, "role_in_use", "role is assigned to one or more team members and cannot be deleted")
		return
	}
	if err := s.reg.DeleteCustomRole(r.Context(), orgID, roleID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete_role_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "custom_role.deleted",
		Target: roleID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Permissions discovery
// ---------------------------------------------------------------------------

// handleListPermissions returns all known fine-grained permission strings with
// descriptions and scope tags. This endpoint is the canonical reference for
// operators building custom roles.
//
// GET /api/v1/platform/permissions
func (s *Server) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"permissions": permissions.All(),
	})
}

// ---------------------------------------------------------------------------
// Effective Permissions
// ---------------------------------------------------------------------------

// handleGetMyPermissions returns the effective permissions the current actor
// has within the given team, derived from their assigned custom role.
//
// GET /api/v1/platform/teams/{teamId}/my-permissions
func (s *Server) handleGetMyPermissions(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("teamId")
	actor := actorFromRequest(r)

	if s.reg == nil {
		s.writeJSON(w, http.StatusOK, &registry.EffectivePermissions{
			TeamID:      teamID,
			UserSub:     actor,
			Permissions: []string{},
		})
		return
	}

	member, err := s.reg.GetTeamMember(r.Context(), teamID, actor)
	if errors.Is(err, registry.ErrNotFound) {
		// Not a member: return empty permissions rather than 404 so callers
		// can always query without needing to know team membership first.
		s.writeJSON(w, http.StatusOK, &registry.EffectivePermissions{
			TeamID:      teamID,
			UserSub:     actor,
			Permissions: []string{},
		})
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_team_member_failed", err.Error())
		return
	}

	ep := &registry.EffectivePermissions{
		TeamID:      teamID,
		UserSub:     actor,
		RoleID:      member.RoleID,
		Permissions: []string{},
	}

	if member.RoleID != "" {
		// Resolve the role — we need the org_id for GetCustomRole. Look up the
		// team first to get org_id.
		team, teamErr := s.reg.GetPlatformTeam(r.Context(), teamID)
		if teamErr == nil {
			role, roleErr := s.reg.GetCustomRole(r.Context(), team.OrgID, member.RoleID)
			if roleErr == nil {
				ep.RoleName = role.Name
				ep.Permissions = role.Permissions
				if ep.Permissions == nil {
					ep.Permissions = []string{}
				}
			}
		}
	}

	s.writeJSON(w, http.StatusOK, ep)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// isAdminActor returns true when the request carries admin credentials:
// an API key with role "admin", an OIDC-group-mapped "admin" role, or a
// service account JWT with role "admin".
func (s *Server) isAdminActor(r *http.Request) bool {
	// API key path — rbacMiddleware injects the validated key into context.
	if key := apiKeyFromContext(r.Context()); key != nil {
		return key.Role == "admin"
	}
	// OIDC group-mapped role.
	if oidcRole, ok := r.Context().Value(ctxKeyOIDCRole).(string); ok && oidcRole == "admin" {
		return true
	}
	// Service account JWT path — no token present when role is not admin.
	// We check the bearer token's parsed claims inline.
	if tok := bearerToken(r); tok != "" {
		if claims, ok := s.parseServiceAccountToken(tok); ok && claims.Role == "admin" {
			return true
		}
	}
	// No authentication at all in dev/bootstrap mode — treat as admin so
	// the endpoints work out of the box without any keys configured.
	if s.reg == nil {
		return true
	}
	has, err := s.reg.HasAnyAPIKey(r.Context())
	if err != nil || !has {
		// No keys in system → dev mode, allow everything.
		return true
	}
	return false
}
