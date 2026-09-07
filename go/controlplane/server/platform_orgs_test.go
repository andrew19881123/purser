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

// makeAdminSrv creates a test server with an admin API key seeded.
// Returns the server and the admin Bearer token.
func makeAdminSrv(t *testing.T) (*server.Server, string) {
	t.Helper()
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "admin-key-1", "admin-key", "admin")
	srv := server.New(reg, server.Config{})
	return srv, token
}

// makeViewerSrv creates a test server with a viewer API key seeded.
func makeViewerSrv(t *testing.T) (*server.Server, registry.Registry, string) {
	t.Helper()
	reg := newReg(t)
	token := seedKeyWithRole(t, reg, "viewer-key-1", "viewer-key", "viewer")
	srv := server.New(reg, server.Config{})
	return srv, reg, token
}

// postJSON sends a POST request with a JSON body and returns the recorder.
func postJSON(t *testing.T, srv *server.Server, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// getJSON sends a GET request and returns the recorder.
func getJSON(t *testing.T, srv *server.Server, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// deleteJSON sends a DELETE request and returns the recorder.
func deleteJSON(t *testing.T, srv *server.Server, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestCreateOrg_AdminCanCreate verifies that an admin key can create an org (201).
func TestCreateOrg_AdminCanCreate(t *testing.T) {
	srv, token := makeAdminSrv(t)
	rec := postJSON(t, srv, "/api/v1/platform/orgs",
		`{"name":"Acme Corp","slug":"acme","description":"test org"}`, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body=%s", rec.Code, rec.Body.String())
	}
	var org registry.Organization
	if err := json.Unmarshal(rec.Body.Bytes(), &org); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if org.Slug != "acme" {
		t.Errorf("slug = %q, want acme", org.Slug)
	}
	if org.ID == "" {
		t.Error("id should not be empty")
	}
}

// TestCreateOrg_ViewerCannotCreate verifies that a viewer key cannot create an org (403).
func TestCreateOrg_ViewerCannotCreate(t *testing.T) {
	srv, _, token := makeViewerSrv(t)
	// Viewer key has read-only role; POST to /api/v1/platform/orgs must be blocked
	// by rbacMiddleware (viewer cannot mutate) => 403.
	rec := postJSON(t, srv, "/api/v1/platform/orgs",
		`{"name":"Acme","slug":"acme"}`, token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreateOrg_DuplicateSlug verifies that creating an org with an existing slug returns 409.
func TestCreateOrg_DuplicateSlug(t *testing.T) {
	srv, token := makeAdminSrv(t)
	// Create first org.
	rec := postJSON(t, srv, "/api/v1/platform/orgs",
		`{"name":"First","slug":"dup-slug"}`, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d; body=%s", rec.Code, rec.Body.String())
	}
	// Attempt duplicate slug.
	rec2 := postJSON(t, srv, "/api/v1/platform/orgs",
		`{"name":"Second","slug":"dup-slug"}`, token)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate slug, got %d; body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestListOrgs_AdminSeesAll verifies that an admin sees all organizations.
func TestListOrgs_AdminSeesAll(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "admin-k", "admin", "admin")
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	// Pre-seed two orgs directly.
	_ = reg.CreateOrganization(ctx, &registry.Organization{ID: "o1", Name: "Org One", Slug: "org-one"})
	_ = reg.CreateOrganization(ctx, &registry.Organization{ID: "o2", Name: "Org Two", Slug: "org-two"})

	rec := getJSON(t, srv, "/api/v1/platform/orgs", adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("list orgs: got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Organizations []registry.Organization `json:"organizations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Organizations) < 2 {
		t.Errorf("expected >= 2 orgs, got %d", len(resp.Organizations))
	}
}

// TestGetOrg_NotFound verifies that fetching a non-existent org returns 404.
func TestGetOrg_NotFound(t *testing.T) {
	srv, token := makeAdminSrv(t)
	rec := getJSON(t, srv, "/api/v1/platform/orgs/does-not-exist", token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeleteOrg_WithTeams_Returns409 verifies that deleting an org with existing
// teams returns 409 Conflict.
func TestDeleteOrg_WithTeams_Returns409(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "admin-dt", "admin", "admin")
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	// Create org and a team inside it.
	_ = reg.CreateOrganization(ctx, &registry.Organization{ID: "org-del", Name: "Del Org", Slug: "del-org"})
	_ = reg.CreateTeam(ctx, &registry.Team{ID: "t1", OrgID: "org-del", Name: "Team A", Slug: "team-a"})

	rec := deleteJSON(t, srv, "/api/v1/platform/orgs/org-del", adminToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when org has teams, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAddOrgMember_AdminOnly verifies that viewers cannot add org members (403).
func TestAddOrgMember_AdminOnly(t *testing.T) {
	srv, _, viewerToken := makeViewerSrv(t)
	// Viewer key on POST → rbacMiddleware blocks before handler runs → 403.
	rec := postJSON(t, srv, "/api/v1/platform/orgs/org-x/members",
		`{"user_id":"u1","role":"member"}`, viewerToken)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer adding org member, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestListTeams_InOrg verifies that listing teams in an org returns the correct teams.
func TestListTeams_InOrg(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "admin-lt", "admin", "admin")
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	_ = reg.CreateOrganization(ctx, &registry.Organization{ID: "org-lt", Name: "List Teams Org", Slug: "lt-org"})
	_ = reg.CreateTeam(ctx, &registry.Team{ID: "t-lt-1", OrgID: "org-lt", Name: "Team 1", Slug: "t1"})
	_ = reg.CreateTeam(ctx, &registry.Team{ID: "t-lt-2", OrgID: "org-lt", Name: "Team 2", Slug: "t2"})

	rec := getJSON(t, srv, "/api/v1/platform/orgs/org-lt/teams", adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("list teams: got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Teams []registry.Team `json:"teams"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Teams) != 2 {
		t.Errorf("expected 2 teams, got %d", len(resp.Teams))
	}
}

// TestAddTeamMember_WithRoleID verifies that adding a team member with a role_id works.
func TestAddTeamMember_WithRoleID(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "admin-atm", "admin", "admin")
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	_ = reg.CreateOrganization(ctx, &registry.Organization{ID: "org-atm", Name: "ATM Org", Slug: "atm-org"})
	_ = reg.CreateTeam(ctx, &registry.Team{ID: "team-atm", OrgID: "org-atm", Name: "ATM Team", Slug: "atm-team"})
	// User must exist in the users table (FK constraint).
	_ = reg.UpsertPlatformUser(ctx, &registry.PlatformUser{ID: "user-123", Email: "user123@example.com", AuthMethod: "oidc"})

	rec := postJSON(t, srv, "/api/v1/platform/teams/team-atm/members",
		`{"user_id":"user-123","role_id":"developer"}`, adminToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add team member: got %d; body=%s", rec.Code, rec.Body.String())
	}
	var m registry.TeamMember
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.RoleID != "developer" {
		t.Errorf("role_id = %q, want developer", m.RoleID)
	}
	if m.UserID != "user-123" {
		t.Errorf("user_id = %q, want user-123", m.UserID)
	}
}

// TestListOrgMembers_AfterAdd verifies listing org members returns freshly added member.
func TestListOrgMembers_AfterAdd(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "admin-lom", "admin", "admin")
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	_ = reg.CreateOrganization(ctx, &registry.Organization{ID: "org-lom", Name: "LOM Org", Slug: "lom-org"})
	// User must exist in the users table (FK constraint).
	_ = reg.UpsertPlatformUser(ctx, &registry.PlatformUser{ID: "u-42", Email: "u42@example.com", AuthMethod: "oidc"})

	// Add member via the API.
	rec := postJSON(t, srv, "/api/v1/platform/orgs/org-lom/members",
		`{"user_id":"u-42","role":"member"}`, adminToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add member: got %d; body=%s", rec.Code, rec.Body.String())
	}

	// List members.
	recList := getJSON(t, srv, "/api/v1/platform/orgs/org-lom/members", adminToken)
	if recList.Code != http.StatusOK {
		t.Fatalf("list members: got %d; body=%s", recList.Code, recList.Body.String())
	}
	var resp struct {
		Members []registry.OrgMember `json:"members"`
	}
	if err := json.Unmarshal(recList.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Members) != 1 || resp.Members[0].UserID != "u-42" {
		t.Errorf("expected 1 member u-42, got %+v", resp.Members)
	}
}

// TestGetTeam_NotFound verifies that fetching a non-existent team returns 404.
func TestGetTeam_NotFound(t *testing.T) {
	srv, token := makeAdminSrv(t)
	rec := getJSON(t, srv, "/api/v1/platform/teams/no-such-team", token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d; body=%s", rec.Code, rec.Body.String())
	}
}
