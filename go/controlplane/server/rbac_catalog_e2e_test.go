package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/permissions"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// TestRBACv2_RoleBuiltFromCatalog_GrantsAccess is the critical regression test
// for the divergent-vocabulary bug. It builds a custom role from the EXACT set
// of permissions served by the catalog function (the same source
// GET /api/v1/platform/permissions serves to the UI), assigns it to a key via
// team membership, and asserts the holder is authorized for routes those
// catalog entries are supposed to gate.
//
// Before the fix the catalog served e.g. "team:apikeys:manage" and
// "team:models:delete" while the routes enforced "team:keys:create" and
// "team:models:undeploy" — so a role built from the catalog granted NOTHING and
// these requests returned 403. This test locks in catalog == enforced.
func TestRBACv2_RoleBuiltFromCatalog_GrantsAccess(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	if err := reg.CreateOrganization(ctx, &registry.Organization{
		ID: "org-cat", Name: "Catalog Org", Slug: "catalog-org",
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := reg.CreateTeam(ctx, &registry.Team{
		ID: "team-cat", OrgID: "org-cat", Name: "Catalog Team", Slug: "catalog-team",
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}

	// Build the role from the catalog EXACTLY as an operator would: take every
	// permission the discovery endpoint offers and grant it.
	var catalogPerms []string
	for _, d := range permissions.All() {
		catalogPerms = append(catalogPerms, d.Key)
	}
	if err := reg.CreateCustomRole(ctx, &registry.CustomRole{
		ID:          "from-catalog",
		OrgID:       "org-cat",
		Name:        "Built From Catalog",
		Permissions: catalogPerms,
	}); err != nil {
		t.Fatalf("create custom role: %v", err)
	}

	token := seedKeyV2(t, reg, "cat-key", "viewer", "team-cat")
	if err := reg.AddTeamMember(ctx, &registry.TeamMember{
		TeamID: "team-cat", UserSub: "cat-key", RoleID: "from-catalog",
	}); err != nil {
		t.Fatalf("add team member: %v", err)
	}

	srv := server.New(reg, server.Config{})

	// Each of these routes enforces a permission that historically diverged from
	// the served catalog. A role holding the entire catalog MUST satisfy them.
	cases := []struct {
		name, method, path, body string
	}{
		{"create-apikey (team:keys:create)", http.MethodPost, "/api/v1/apikeys", `{"name":"k","tenant":"team-cat","role":"inference"}`},
		{"undeploy-model (team:models:undeploy)", http.MethodDelete, "/api/v1/models/m-x", ``},
		{"delete-deployment (team:models:undeploy)", http.MethodDelete, "/api/v1/deployments/d-x", ``},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		} else {
			body = strings.NewReader(``)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code == http.StatusForbidden {
			t.Errorf("%s: role built from catalog got 403 on %s %s — catalog entry does not gate access; body=%s",
				c.name, c.method, c.path, rec.Body.String())
		}
	}
}
