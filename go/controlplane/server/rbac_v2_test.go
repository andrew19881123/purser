package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// seedKeyV2 creates an API key in reg and returns its plaintext token.
// The key's hash is sha256("psk_v2_" + id) so tokens are deterministic.
func seedKeyV2(t *testing.T, reg registry.Registry, id, role, tenant string) string {
	t.Helper()
	plaintext := "psk_v2_" + id
	sum := sha256.Sum256([]byte(plaintext))
	hash := hex.EncodeToString(sum[:])
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      id,
		Name:    "v2-test-" + id,
		KeyHash: hash,
		Tenant:  tenant,
		Role:    role,
		Enabled: true,
	}); err != nil {
		t.Fatalf("seedKeyV2 %q: %v", id, err)
	}
	return plaintext
}

// TestRBACv2_DeveloperCanDeploy verifies that an API key with a custom
// "developer" role (carrying team:models:deploy via team membership) is
// allowed to POST /api/v1/models/{id}/deploy. This exercises the v0.4
// team-context path in resolvePermissions.
func TestRBACv2_DeveloperCanDeploy(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Set up the platform entity hierarchy: org → team → custom role → key.
	if err := reg.CreateOrganization(ctx, &registry.Organization{
		ID: "org-dev", Name: "Dev Org", Slug: "dev-org",
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := reg.CreateTeam(ctx, &registry.Team{
		ID: "team-dev", OrgID: "org-dev", Name: "Dev Team", Slug: "dev-team",
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := reg.CreateCustomRole(ctx, &registry.CustomRole{
		ID:          "developer",
		OrgID:       "org-dev",
		Name:        "Developer",
		Permissions: []string{registry.PermTeamModelsDeploy, registry.PermTeamMetricsView},
	}); err != nil {
		t.Fatalf("create custom role: %v", err)
	}

	// Create the API key scoped to team-dev (role="viewer" as legacy fallback,
	// but the team membership overrides with the developer permissions).
	token := seedKeyV2(t, reg, "dev-key", "viewer", "team-dev")

	// Link the key (by its ID) to the developer role in team-dev.
	if err := reg.AddTeamMember(ctx, &registry.TeamMember{
		TeamID: "team-dev", UserSub: "dev-key", RoleID: "developer",
	}); err != nil {
		t.Fatalf("add team member: %v", err)
	}

	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/m-test/deploy",
		strings.NewReader(`{"model_id":"m-test"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("developer key on POST /models/{id}/deploy got 403; body=%s", rec.Body.String())
	}
}

// TestRBACv2_ViewerCannotDeploy verifies that a legacy viewer API key (with
// only team:metrics:view and team:approvals:view) is denied on
// POST /api/v1/models/{id}/deploy with 403 Forbidden.
func TestRBACv2_ViewerCannotDeploy(t *testing.T) {
	reg := newReg(t)
	// No tenant: falls back to legacy role permissions only.
	token := seedKeyV2(t, reg, "viewer-nodeploy", "viewer", "")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/m-any/deploy",
		strings.NewReader(`{"model_id":"m-any"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer key on POST /models/{id}/deploy got %d, want 403; body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestRBACv2_LegacyAdmin_BypassesAll verifies that a legacy admin API key
// passes the v0.4 permission check on every route (IsPlatformAdmin bypass).
func TestRBACv2_LegacyAdmin_BypassesAll(t *testing.T) {
	reg := newReg(t)
	token := seedKeyV2(t, reg, "admin-bypass", "admin", "")
	srv := server.New(reg, server.Config{})

	endpoints := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/models/m/deploy"},
		{http.MethodDelete, "/api/v1/nodes/n1"},
		{http.MethodPost, "/api/v1/platform/orgs"},
		{http.MethodDelete, "/api/v1/platform/orgs/org-x"},
	}
	for _, ep := range endpoints {
		var body *strings.Reader
		if ep.method != http.MethodDelete {
			body = strings.NewReader(`{}`)
		} else {
			body = strings.NewReader(``)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(ep.method, ep.path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("admin key on %s %s got 403, want bypass; body=%s",
				ep.method, ep.path, rec.Body.String())
		}
	}
}

// TestRBACv2_LegacyViewer_CanListModels verifies that the viewer legacy role
// carries team:metrics:view, which is sufficient for GET /api/v1/models.
func TestRBACv2_LegacyViewer_CanListModels(t *testing.T) {
	reg := newReg(t)
	token := seedKeyV2(t, reg, "viewer-listmodels", "viewer", "")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("viewer key on GET /api/v1/models got 403, want allowed; body=%s", rec.Body.String())
	}
}

// TestRBACv2_InferenceOnly_CannotListModels verifies that an inference-only
// API key (permissions: ["inference:call"]) is denied on GET /api/v1/models
// because it lacks team:metrics:view.
func TestRBACv2_InferenceOnly_CannotListModels(t *testing.T) {
	reg := newReg(t)
	token := seedKeyV2(t, reg, "inference-nolist", "inference", "")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("inference key on GET /api/v1/models got %d, want 403; body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestRBACv2_PlatformAdmin_CanCreateOrg verifies that a legacy admin key has
// platform:orgs:create (via IsPlatformAdmin bypass) and can reach the
// POST /api/v1/platform/orgs handler (non-403 response).
func TestRBACv2_PlatformAdmin_CanCreateOrg(t *testing.T) {
	reg := newReg(t)
	token := seedKeyV2(t, reg, "admin-createorg", "admin", "")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/orgs",
		strings.NewReader(`{"name":"new-org","slug":"new-org"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin key on POST /api/v1/platform/orgs got 403; body=%s", rec.Body.String())
	}
}

// TestRBACv2_InferenceOnly_CannotAccessManagement verifies that an inference
// key is denied on all management endpoints regardless of HTTP method,
// including audit-log and approvals list (GET-only endpoints).
func TestRBACv2_InferenceOnly_CannotAccessManagement(t *testing.T) {
	reg := newReg(t)
	token := seedKeyV2(t, reg, "inference-mgmt", "inference", "")
	srv := server.New(reg, server.Config{})

	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/approvals"},
		{http.MethodGet, "/api/v1/enterprise/audit-log"},
		{http.MethodGet, "/api/v1/deployments"},
	}
	for _, r := range routes {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(r.method, r.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("inference key on %s %s got %d, want 403; body=%s",
				r.method, r.path, rec.Code, rec.Body.String())
		}
	}
}
