// platform_orgs.go — REST handlers for Organizations, Teams, and membership.
//
// Endpoints:
//
//	POST   /api/v1/platform/orgs                              — create org (platform_admin)
//	GET    /api/v1/platform/orgs                              — list orgs
//	GET    /api/v1/platform/orgs/{id}                        — get org
//	PUT    /api/v1/platform/orgs/{id}                        — update org
//	DELETE /api/v1/platform/orgs/{id}                        — delete org (platform_admin)
//
//	POST   /api/v1/platform/orgs/{orgId}/teams               — create team in org
//	GET    /api/v1/platform/orgs/{orgId}/teams               — list teams in org
//	GET    /api/v1/platform/teams/{id}                       — get team
//	PUT    /api/v1/platform/teams/{id}                       — update team
//	DELETE /api/v1/platform/teams/{id}                       — delete team
//
//	POST   /api/v1/platform/orgs/{orgId}/members             — add org member
//	GET    /api/v1/platform/orgs/{orgId}/members             — list org members
//	PUT    /api/v1/platform/orgs/{orgId}/members/{userId}    — update org member role
//	DELETE /api/v1/platform/orgs/{orgId}/members/{userId}    — remove org member
//
//	POST   /api/v1/platform/teams/{teamId}/members           — add team member
//	GET    /api/v1/platform/teams/{teamId}/members           — list team members
//	PUT    /api/v1/platform/teams/{teamId}/members/{userId}  — update team member role
//	DELETE /api/v1/platform/teams/{teamId}/members/{userId}  — remove team member
//
// Authorization: destructive mutations require the platform "admin" API key role.
// Full org/team-scoped RBAC will be added in Wave 3.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// ---------------------------------------------------------------------------
// Authorization helpers
// ---------------------------------------------------------------------------

// isPlatformAdmin returns true when the request carries an API key with role
// "admin". This is the sole permission gate used in v0.4; full RBAC is Wave 3.
func (s *Server) isPlatformAdmin(r *http.Request) bool {
	key := apiKeyFromContext(r.Context())
	return key != nil && key.Role == "admin"
}

// isOrgAdminOrPlatformAdmin returns true when the caller is a platform admin.
// In v0.4 org-scoped admin checks are not yet implemented (Wave 3 adds them).
func (s *Server) isOrgAdminOrPlatformAdmin(r *http.Request, _ string) bool {
	return s.isPlatformAdmin(r)
}

// requireAdmin writes 403 and returns false when the caller is not a platform
// admin. Handlers call it like:
//
//	if !s.requireAdmin(w, r) { return }
func (s *Server) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.isPlatformAdmin(r) {
		return true
	}
	s.writeError(w, http.StatusForbidden, "forbidden", "platform_admin role required")
	return false
}

// ---------------------------------------------------------------------------
// Organizations
// ---------------------------------------------------------------------------

// handleCreateOrg creates a new organization.
// POST /api/v1/platform/orgs — platform_admin only.
func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatformAdmin(w, r) {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Slug == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "name and slug are required")
		return
	}

	// Check for slug conflict.
	if _, err := s.reg.GetOrganizationBySlug(r.Context(), req.Slug); err == nil {
		s.writeError(w, http.StatusConflict, "conflict", "organization slug already exists")
		return
	}

	org := &registry.Organization{
		ID:          "org-" + randHex(8),
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.reg.CreateOrganization(r.Context(), org); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.writeError(w, http.StatusConflict, "conflict", "organization already exists")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_org_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "org.created",
		Target: org.ID,
	})
	s.writeJSON(w, http.StatusCreated, org)
}

// handleListOrgs lists organizations.
// GET /api/v1/platform/orgs — platform_admin sees all; others see their own (future).
func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := s.reg.ListOrganizations(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_orgs_failed", err.Error())
		return
	}
	if orgs == nil {
		orgs = []*registry.Organization{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"organizations": orgs})
}

// handleGetOrg returns a single organization by ID.
// GET /api/v1/platform/orgs/{id}
func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	org, err := s.reg.GetOrganization(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "organization not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_org_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, org)
}

// handleUpdateOrg updates an organization's name and/or description.
// PUT /api/v1/platform/orgs/{id} — org_admin or platform_admin.
func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.isOrgAdminOrPlatformAdmin(r, id) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}

	org, err := s.reg.GetOrganization(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "organization not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_org_failed", err.Error())
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	if req.Name != nil {
		org.Name = *req.Name
	}
	if req.Description != nil {
		org.Description = *req.Description
	}

	if err := s.reg.UpdateOrganization(r.Context(), org); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "organization not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "update_org_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "org.updated",
		Target: org.ID,
	})
	s.writeJSON(w, http.StatusOK, org)
}

// handleDeleteOrg deletes an organization.
// DELETE /api/v1/platform/orgs/{id} — platform_admin only; 409 if org has teams.
func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatformAdmin(w, r) {
		return
	}
	id := r.PathValue("id")

	// Guard: refuse deletion if the org still has teams.
	teams, err := s.reg.ListTeamsByOrg(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_teams_failed", err.Error())
		return
	}
	if len(teams) > 0 {
		s.writeError(w, http.StatusConflict, "org_has_teams",
			"organization has teams; delete all teams first")
		return
	}

	if err := s.reg.DeleteOrganization(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "organization not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "delete_org_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "org.deleted",
		Target: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Teams
// ---------------------------------------------------------------------------

// handleCreateTeam creates a team within an organization.
// POST /api/v1/platform/orgs/{orgId}/teams — org_admin or platform_admin.
func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if !s.isOrgAdminOrPlatformAdmin(r, orgID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}

	// Verify org exists.
	if _, err := s.reg.GetOrganization(r.Context(), orgID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "organization not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "get_org_failed", err.Error())
		return
	}

	var req struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Slug == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "name and slug are required")
		return
	}

	// Check slug uniqueness within the org.
	if _, err := s.reg.GetTeamBySlug(r.Context(), orgID, req.Slug); err == nil {
		s.writeError(w, http.StatusConflict, "conflict", "team slug already exists in this organization")
		return
	}

	team := &registry.Team{
		ID:          "team-" + randHex(8),
		OrgID:       orgID,
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.reg.CreateTeam(r.Context(), team); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.writeError(w, http.StatusConflict, "conflict", "team slug already exists")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_team_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "team.created",
		Target: team.ID,
	})
	s.writeJSON(w, http.StatusCreated, team)
}

// handleListTeams lists all teams within an organization.
// GET /api/v1/platform/orgs/{orgId}/teams
func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	teams, err := s.reg.ListTeamsByOrg(r.Context(), orgID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_teams_failed", err.Error())
		return
	}
	if teams == nil {
		teams = []*registry.Team{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}

// handleGetTeam returns a single team by ID.
// GET /api/v1/platform/teams/{id}
func (s *Server) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	team, err := s.reg.GetTeam(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_team_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, team)
}

// handleUpdateTeam updates a team's name and/or description.
// PUT /api/v1/platform/teams/{id} — team_admin, org_admin, or platform_admin.
func (s *Server) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	team, err := s.reg.GetTeam(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_team_failed", err.Error())
		return
	}

	if !s.isOrgAdminOrPlatformAdmin(r, team.OrgID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	if req.Name != nil {
		team.Name = *req.Name
	}
	if req.Description != nil {
		team.Description = *req.Description
	}

	if err := s.reg.UpdateTeam(r.Context(), team); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "update_team_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "team.updated",
		Target: team.ID,
	})
	s.writeJSON(w, http.StatusOK, team)
}

// handleDeleteTeam deletes a team.
// DELETE /api/v1/platform/teams/{id} — org_admin or platform_admin.
func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	team, err := s.reg.GetTeam(r.Context(), id)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_team_failed", err.Error())
		return
	}

	if !s.isOrgAdminOrPlatformAdmin(r, team.OrgID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "org_admin or platform_admin role required")
		return
	}

	if err := s.reg.DeleteTeam(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "delete_team_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "team.deleted",
		Target: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Organization members
// ---------------------------------------------------------------------------

// handleAddOrgMember adds a user to an organization.
// POST /api/v1/platform/orgs/{orgId}/members — platform_admin (Wave 3: org_admin).
func (s *Server) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if !s.requirePlatformAdmin(w, r) {
		return
	}

	var req struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "user_id is required")
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}

	m := &registry.OrgMember{
		OrgID:     orgID,
		UserID:    req.UserID,
		Role:      req.Role,
		InvitedBy: actorFromRequest(r),
	}
	if err := s.reg.AddOrgMember(r.Context(), m); err != nil {
		s.writeError(w, http.StatusInternalServerError, "add_org_member_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, m)
}

// handleListOrgMembers lists the members of an organization.
// GET /api/v1/platform/orgs/{orgId}/members
func (s *Server) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	members, err := s.reg.ListOrgMembers(r.Context(), orgID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_org_members_failed", err.Error())
		return
	}
	if members == nil {
		members = []*registry.OrgMember{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

// handleUpdateOrgMember changes the role of an existing org member.
// PUT /api/v1/platform/orgs/{orgId}/members/{userId} — platform_admin.
func (s *Server) handleUpdateOrgMember(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	userID := r.PathValue("userId")
	if !s.requirePlatformAdmin(w, r) {
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Role == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "role is required")
		return
	}

	// Verify membership exists.
	existing, err := s.reg.GetOrgMember(r.Context(), orgID, userID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "membership not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_org_member_failed", err.Error())
		return
	}

	// Remove and re-add with new role (AddOrgMember uses ON CONFLICT DO NOTHING,
	// so we must remove first to update the role).
	if err := s.reg.RemoveOrgMember(r.Context(), orgID, userID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_org_member_failed", err.Error())
		return
	}
	updated := &registry.OrgMember{
		OrgID:     orgID,
		UserID:    userID,
		Role:      req.Role,
		InvitedBy: existing.InvitedBy,
		CreatedAt: existing.CreatedAt,
	}
	if err := s.reg.AddOrgMember(r.Context(), updated); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_org_member_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, updated)
}

// handleRemoveOrgMember removes a user from an organization.
// DELETE /api/v1/platform/orgs/{orgId}/members/{userId} — platform_admin.
func (s *Server) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	userID := r.PathValue("userId")
	if !s.requirePlatformAdmin(w, r) {
		return
	}

	if err := s.reg.RemoveOrgMember(r.Context(), orgID, userID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "membership not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "remove_org_member_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Team members
// ---------------------------------------------------------------------------

// handleAddTeamMember adds a user to a team.
// POST /api/v1/platform/teams/{teamId}/members — platform_admin (Wave 3: team_admin, org_admin).
func (s *Server) handleAddTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("teamId")
	if !s.requirePlatformAdmin(w, r) {
		return
	}

	var req struct {
		UserID string `json:"user_id"`
		RoleID string `json:"role_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "user_id is required")
		return
	}
	if req.RoleID == "" {
		req.RoleID = "member"
	}

	m := &registry.TeamMember{
		TeamID:    teamID,
		UserID:    req.UserID,
		RoleID:    req.RoleID,
		InvitedBy: actorFromRequest(r),
	}
	if err := s.reg.AddTeamMember(r.Context(), m); err != nil {
		s.writeError(w, http.StatusInternalServerError, "add_team_member_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, m)
}

// handleListTeamMembers lists the members of a team.
// GET /api/v1/platform/teams/{teamId}/members
func (s *Server) handleListTeamMembers(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("teamId")
	members, err := s.reg.ListTeamMembers(r.Context(), teamID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_team_members_failed", err.Error())
		return
	}
	if members == nil {
		members = []*registry.TeamMember{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

// handleUpdateTeamMember changes the role_id of a team member.
// PUT /api/v1/platform/teams/{teamId}/members/{userId} — platform_admin.
func (s *Server) handleUpdateTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("teamId")
	userID := r.PathValue("userId")
	if !s.requirePlatformAdmin(w, r) {
		return
	}

	var req struct {
		RoleID string `json:"role_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoleID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "role_id is required")
		return
	}

	// Verify membership exists.
	existing, err := s.reg.GetTeamMember(r.Context(), teamID, userID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "membership not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_team_member_failed", err.Error())
		return
	}

	// AddTeamMember uses ON CONFLICT DO UPDATE SET role_id — upsert is safe here.
	updated := &registry.TeamMember{
		TeamID:    teamID,
		UserID:    userID,
		RoleID:    req.RoleID,
		InvitedBy: existing.InvitedBy,
		CreatedAt: existing.CreatedAt,
	}
	if err := s.reg.AddTeamMember(r.Context(), updated); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_team_member_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, updated)
}

// handleRemoveTeamMember removes a user from a team.
// DELETE /api/v1/platform/teams/{teamId}/members/{userId} — platform_admin.
func (s *Server) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("teamId")
	userID := r.PathValue("userId")
	if !s.requirePlatformAdmin(w, r) {
		return
	}

	if err := s.reg.RemoveTeamMember(r.Context(), teamID, userID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "membership not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "remove_team_member_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
