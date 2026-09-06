package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// ---------------------------------------------------------------------------
// TestGetMe_WithAPIKey
// ---------------------------------------------------------------------------

// TestGetMe_WithAPIKey verifies that /users/me returns the current actor when
// authenticated with an API key.
func TestGetMe_WithAPIKey(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-me", "me-key", "admin")
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	actor, ok := body["actor"].(string)
	if !ok || actor == "" {
		t.Errorf("missing or empty actor field; body=%s", rec.Body.String())
	}
	if _, ok := body["orgs"]; !ok {
		t.Errorf("missing orgs field; body=%s", rec.Body.String())
	}
	if _, ok := body["teams"]; !ok {
		t.Errorf("missing teams field; body=%s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestListPermissions_Returns_AllConstants
// ---------------------------------------------------------------------------

// TestListPermissions_Returns_AllConstants verifies that the permissions
// endpoint returns at least 20 distinct permission strings.
func TestListPermissions_Returns_AllConstants(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/permissions", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Permissions []struct {
			Key         string `json:"key"`
			Description string `json:"description"`
			Scope       string `json:"scope"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Permissions) < 20 {
		t.Errorf("got %d permissions, want at least 20", len(body.Permissions))
	}
	// Spot-check a known permission.
	found := false
	for _, p := range body.Permissions {
		if p.Key == "team:models:deploy" {
			found = true
			if p.Scope != "team" {
				t.Errorf("team:models:deploy scope = %q, want team", p.Scope)
			}
		}
	}
	if !found {
		t.Error("permission team:models:deploy not found in response")
	}
}

// ---------------------------------------------------------------------------
// TestCreateRole_OrgAdmin
// ---------------------------------------------------------------------------

// TestCreateRole_OrgAdmin verifies that an admin key can create a custom role
// and receive 201 Created with the full role JSON.
func TestCreateRole_OrgAdmin(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-ca", "admin-key", "admin")
	srv := server.New(reg, server.Config{})

	body := map[string]any{
		"name":        "ml_engineer",
		"description": "ML engineer role",
		"permissions": []string{"team:models:deploy", "team:metrics:view", "inference:call"},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/orgs/org-1/roles", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp registry.CustomRole
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Name != "ml_engineer" {
		t.Errorf("name = %q, want ml_engineer", resp.Name)
	}
	if resp.ID == "" {
		t.Error("missing role ID in response")
	}
	if len(resp.Permissions) != 3 {
		t.Errorf("permissions count = %d, want 3", len(resp.Permissions))
	}
}

// ---------------------------------------------------------------------------
// TestCreateRole_DuplicateName
// ---------------------------------------------------------------------------

// TestCreateRole_DuplicateName verifies that creating two roles with the same
// name in the same org returns 409 Conflict on the second request.
func TestCreateRole_DuplicateName(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-dup", "admin-dup", "admin")
	srv := server.New(reg, server.Config{})

	body := map[string]any{"name": "reader", "permissions": []string{}}
	b, _ := json.Marshal(body)

	// First creation should succeed.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/platform/orgs/org-2/roles", bytes.NewReader(b))
	req1.Header.Set("Authorization", "Bearer "+token)
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want 201; body=%s", rec1.Code, rec1.Body.String())
	}

	// Second creation with the same name should return 409.
	b2, _ := json.Marshal(body)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/platform/orgs/org-2/roles", bytes.NewReader(b2))
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("duplicate create: status = %d, want 409; body=%s", rec2.Code, rec2.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestUpdateRole_SystemRole_Returns409
// ---------------------------------------------------------------------------

// TestUpdateRole_SystemRole_Returns409 verifies that a PUT on a system role
// returns 409 Conflict.
func TestUpdateRole_SystemRole_Returns409(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-sys", "admin-sys", "admin")
	srv := server.New(reg, server.Config{})

	// Seed a system role directly via the registry.
	sysRole := &registry.CustomRole{
		ID:          "role-sys-1",
		OrgID:       "org-sys",
		Name:        "developer",
		Permissions: []string{"team:models:deploy"},
		IsSystem:    true,
	}
	if err := s_createSystemRole(t, reg, sysRole); err != nil {
		t.Fatalf("seed system role: %v", err)
	}

	body := map[string]any{"name": "developer-renamed", "permissions": []string{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/platform/orgs/org-sys/roles/role-sys-1", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestDeleteRole_InUse_Returns409
// ---------------------------------------------------------------------------

// TestDeleteRole_InUse_Returns409 verifies that deleting a custom role that is
// assigned to a team member returns 409 Conflict.
func TestDeleteRole_InUse_Returns409(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-del", "admin-del", "admin")
	srv := server.New(reg, server.Config{})

	// Create a custom role.
	role := &registry.CustomRole{
		ID:          "role-inuse-1",
		OrgID:       "org-del",
		Name:        "my-role",
		Permissions: []string{},
		IsSystem:    false,
	}
	if err := s_createSystemRole(t, reg, role); err != nil {
		t.Fatalf("seed role: %v", err)
	}

	// Assign the role to a team member.
	if err := reg.UpsertTeamMember(context.Background(), &registry.TeamMember{
		TeamID:  "team-del",
		UserSub: "oidc:alice",
		RoleID:  "role-inuse-1",
	}); err != nil {
		t.Fatalf("seed team member: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/platform/orgs/org-del/roles/role-inuse-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestGetMyPermissions_DeveloperRole
// ---------------------------------------------------------------------------

// TestGetMyPermissions_DeveloperRole verifies that /my-permissions returns the
// correct permission list when the actor is assigned a custom role in a team.
func TestGetMyPermissions_DeveloperRole(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-perm", "perm-key", "admin")
	srv := server.New(reg, server.Config{})

	// Ensure org and team exist.
	if err := reg.UpsertPlatformOrg(context.Background(), &registry.PlatformOrg{
		ID:   "org-perm",
		Name: "org-perm",
	}); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := reg.UpsertPlatformTeam(context.Background(), &registry.PlatformTeam{
		ID:    "team-perm",
		OrgID: "org-perm",
		Name:  "team-perm",
	}); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	// Create a custom role.
	if err := reg.CreateCustomRole(context.Background(), &registry.CustomRole{
		ID:          "role-dev",
		OrgID:       "org-perm",
		Name:        "developer",
		Permissions: []string{"team:models:deploy", "team:metrics:view"},
	}); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	// The actor for "key-perm" is "apikey:" + first 8 hex chars of sha256("psk_test_key-perm").
	// The endpoint looks up the current actor, so we need to derive it.
	// Rather than hard-code the hash, we make a /users/me call to learn the actor string,
	// then assign it as a team member.
	req0 := httptest.NewRequest(http.MethodGet, "/api/v1/platform/users/me", nil)
	req0.Header.Set("Authorization", "Bearer "+token)
	rec0 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec0, req0)
	var me map[string]any
	if err := json.Unmarshal(rec0.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode /users/me: %v", err)
	}
	actor, _ := me["actor"].(string)
	if actor == "" {
		t.Fatal("could not determine actor from /users/me")
	}

	// Assign the actor as a team member with the developer role.
	if err := reg.UpsertTeamMember(context.Background(), &registry.TeamMember{
		TeamID:  "team-perm",
		UserSub: actor,
		RoleID:  "role-dev",
	}); err != nil {
		t.Fatalf("seed team member: %v", err)
	}

	// Now call /my-permissions.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/teams/team-perm/my-permissions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var ep registry.EffectivePermissions
	if err := json.Unmarshal(rec.Body.Bytes(), &ep); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if ep.TeamID != "team-perm" {
		t.Errorf("team_id = %q, want team-perm", ep.TeamID)
	}
	if len(ep.Permissions) != 2 {
		t.Errorf("permissions = %v, want 2 items", ep.Permissions)
	}
	if ep.RoleName != "developer" {
		t.Errorf("role_name = %q, want developer", ep.RoleName)
	}
}

// ---------------------------------------------------------------------------
// TestGetMyPermissions_NotAMember
// ---------------------------------------------------------------------------

// TestGetMyPermissions_NotAMember verifies that a user who is not a team member
// receives an empty permissions list (not a 404).
func TestGetMyPermissions_NotAMember(t *testing.T) {
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "key-nopm", "nopm-key", "viewer")
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/teams/nonexistent-team/my-permissions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var ep registry.EffectivePermissions
	if err := json.Unmarshal(rec.Body.Bytes(), &ep); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(ep.Permissions) != 0 {
		t.Errorf("expected 0 permissions for non-member, got %v", ep.Permissions)
	}
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

// s_createSystemRole is a test-only helper that seeds a CustomRole (including
// system roles) directly via the registry, bypassing the server's is_system
// guard (the server would reject such a payload). Used to set up test fixtures
// for the "cannot modify/delete system role" test cases.
func s_createSystemRole(t *testing.T, reg registry.Registry, role *registry.CustomRole) error {
	t.Helper()
	// Ensure the org exists.
	_ = reg.UpsertPlatformOrg(context.Background(), &registry.PlatformOrg{
		ID:   role.OrgID,
		Name: role.OrgID,
	})
	return reg.CreateCustomRole(context.Background(), role)
}
