// tenant_isolation_test.go — documents and verifies the tenant isolation
// policy for every GET list endpoint.
//
// Policy summary:
//
//	Deployments  — tenant-scoped: non-admin keys see only their tenant's records.
//	APIKeys      — tenant-scoped: non-admin keys see only their tenant's records.
//	Models       — intentionally global: all authenticated users see the full catalog.
//	Nodes        — intentionally global: all authenticated users see the full fleet.
//
// For each scoped endpoint the test verifies three principals:
//  1. Admin key — sees all records across all tenants.
//  2. Non-admin "acme" key — sees only "acme" records.
//  3. Non-admin "beta" key — does NOT see "acme" records (and vice-versa).
//
// For each global endpoint the test verifies that a non-admin tenant key still
// receives all records, documenting the intentional design decision.
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// ---------------------------------------------------------------------------
// Tenant-scoped: Deployments
// ---------------------------------------------------------------------------

// TestTenantIsolation_Deployments verifies that GET /api/v1/deployments
// respects tenant boundaries.
//
//   - Admin key → sees all deployments (cross-tenant)
//   - Viewer key for "acme" → sees only "acme" deployments
//   - Viewer key for "beta" → does NOT see "acme" deployments
func TestTenantIsolation_Deployments(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Seed API keys.
	adminToken := seedTenantKey(t, reg, "ti-dep-admin", "admin", "acme", "admin")
	acmeToken := seedTenantKey(t, reg, "ti-dep-acme", "acme-viewer", "acme", "viewer")
	betaToken := seedTenantKey(t, reg, "ti-dep-beta", "beta-viewer", "beta", "viewer")

	// Seed deployments in two different tenants.
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-acme-1",
		ModelID: "m1",
		State:   "DEPLOYMENT_STATE_ACTIVE",
		Detail:  json.RawMessage(`{"tenant":"acme"}`),
	}); err != nil {
		t.Fatalf("create acme deployment: %v", err)
	}
	if err := reg.CreateDeployment(ctx, &registry.Deployment{
		ID:      "dep-beta-1",
		ModelID: "m2",
		State:   "DEPLOYMENT_STATE_ACTIVE",
		Detail:  json.RawMessage(`{"tenant":"beta"}`),
	}); err != nil {
		t.Fatalf("create beta deployment: %v", err)
	}

	srv := server.New(reg, server.Config{})

	t.Run("admin sees all deployments", func(t *testing.T) {
		body := getDeployments(t, srv, adminToken)
		if len(body.Deployments) != 2 {
			t.Errorf("admin: got %d deployments, want 2; raw=%v", len(body.Deployments), tiDeploymentIDs(body.Deployments))
		}
	})

	t.Run("acme viewer sees only acme deployments", func(t *testing.T) {
		body := getDeployments(t, srv, acmeToken)
		if len(body.Deployments) != 1 {
			t.Fatalf("acme: got %d deployments, want 1; raw=%v", len(body.Deployments), tiDeploymentIDs(body.Deployments))
		}
		if body.Deployments[0].ID != "dep-acme-1" {
			t.Errorf("acme: returned %q, want dep-acme-1", body.Deployments[0].ID)
		}
	})

	t.Run("beta viewer does not see acme deployments", func(t *testing.T) {
		body := getDeployments(t, srv, betaToken)
		if len(body.Deployments) != 1 {
			t.Fatalf("beta: got %d deployments, want 1; raw=%v", len(body.Deployments), tiDeploymentIDs(body.Deployments))
		}
		if body.Deployments[0].ID != "dep-beta-1" {
			t.Errorf("beta: returned %q, want dep-beta-1", body.Deployments[0].ID)
		}
		for _, d := range body.Deployments {
			if d.ID == "dep-acme-1" {
				t.Errorf("beta viewer must NOT see acme deployment dep-acme-1")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Tenant-scoped: APIKeys
// ---------------------------------------------------------------------------

// TestTenantIsolation_APIKeys verifies that GET /api/v1/apikeys respects
// tenant boundaries.
//
//   - Admin key → sees all API keys (cross-tenant)
//   - Viewer key for "acme" → sees only "acme" keys
//   - Viewer key for "beta" → does NOT see "acme" keys
func TestTenantIsolation_APIKeys(t *testing.T) {
	reg := newReg(t)

	// Seed keys across two tenants plus an admin.
	adminToken := seedTenantKey(t, reg, "ti-key-admin", "admin", "acme", "admin")
	acmeToken := seedTenantKey(t, reg, "ti-key-acme-v", "acme-viewer", "acme", "viewer")
	betaToken := seedTenantKey(t, reg, "ti-key-beta-v", "beta-viewer", "beta", "viewer")
	// Extra acme key to verify count.
	seedTenantKey(t, reg, "ti-key-acme-v2", "acme-viewer-2", "acme", "viewer")

	srv := server.New(reg, server.Config{})

	t.Run("admin sees all api keys", func(t *testing.T) {
		body := getAPIKeys(t, srv, adminToken)
		// 4 keys total: admin + acme-v + beta-v + acme-v2.
		if len(body.APIKeys) != 4 {
			t.Errorf("admin: got %d keys, want 4; ids=%v", len(body.APIKeys), tiAPIKeyIDs(body.APIKeys))
		}
	})

	t.Run("acme viewer sees only acme keys", func(t *testing.T) {
		body := getAPIKeys(t, srv, acmeToken)
		// acme-v + acme-v2 (admin key also belongs to "acme" tenant but has role=admin;
		// ListAPIKeysByTenant with tenant="acme" returns all enabled keys where tenant="acme").
		for _, k := range body.APIKeys {
			if k.Tenant != "acme" {
				t.Errorf("acme viewer got key with tenant=%q (id=%s); want acme only", k.Tenant, k.ID)
			}
		}
		if len(body.APIKeys) == 0 {
			t.Error("acme viewer: expected at least one key, got none")
		}
	})

	t.Run("beta viewer does not see acme keys", func(t *testing.T) {
		body := getAPIKeys(t, srv, betaToken)
		for _, k := range body.APIKeys {
			if k.Tenant == "acme" {
				t.Errorf("beta viewer must NOT see acme key %s", k.ID)
			}
		}
		// Beta viewer must see at least its own key.
		found := false
		for _, k := range body.APIKeys {
			if k.ID == "ti-key-beta-v" {
				found = true
			}
		}
		if !found {
			t.Errorf("beta viewer: own key ti-key-beta-v not in response; ids=%v", tiAPIKeyIDs(body.APIKeys))
		}
	})
}

// ---------------------------------------------------------------------------
// Intentionally global: Models (catalog)
// ---------------------------------------------------------------------------

// TestTenantIsolation_Models_GlobalCatalog documents that GET /api/v1/models
// is intentionally NOT tenant-scoped. Any authenticated user (regardless of
// tenant) receives the full model catalog. This is by design: tenants must
// be able to discover available models before deploying them.
func TestTenantIsolation_Models_GlobalCatalog(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Seed a viewer key in tenant "acme" — non-admin, so would be scoped if the
	// endpoint were tenant-filtered.
	acmeToken := seedTenantKey(t, reg, "ti-mod-acme", "acme-viewer", "acme", "viewer")

	// Seed two models.
	if err := reg.CreateModel(ctx, &registry.Model{
		ID:           "llama3-8b",
		Family:       "llama",
		Architecture: "transformer",
	}); err != nil {
		t.Fatalf("create model llama3-8b: %v", err)
	}
	if err := reg.CreateModel(ctx, &registry.Model{
		ID:           "mistral-7b",
		Family:       "mistral",
		Architecture: "transformer",
	}); err != nil {
		t.Fatalf("create model mistral-7b: %v", err)
	}

	srv := server.New(reg, server.Config{})

	t.Run("non-admin tenant viewer sees all models (global catalog)", func(t *testing.T) {
		body := getModels(t, srv, acmeToken)
		if len(body.Models) != 2 {
			t.Errorf("acme viewer: got %d models, want 2 (catalog is global); ids=%v",
				len(body.Models), tiModelIDs(body.Models))
		}
	})

	t.Run("unauthenticated user sees all models when no keys are provisioned", func(t *testing.T) {
		// Use a fresh registry with no API keys — server falls back to open mode.
		openReg := newReg(t)
		if err := openReg.CreateModel(ctx, &registry.Model{
			ID:     "llama3-8b",
			Family: "llama",
		}); err != nil {
			t.Fatalf("create model: %v", err)
		}
		openSrv := server.New(openReg, server.Config{})
		body := getModels(t, openSrv, "")
		if len(body.Models) != 1 {
			t.Errorf("open mode: got %d models, want 1", len(body.Models))
		}
	})
}

// ---------------------------------------------------------------------------
// Intentionally global: Nodes (fleet visibility)
// ---------------------------------------------------------------------------

// TestTenantIsolation_Nodes_GlobalRead documents that GET /api/v1/nodes is
// intentionally NOT tenant-scoped. All authenticated users see the full fleet.
// This is by design: operators and viewers both need cluster topology visibility.
func TestTenantIsolation_Nodes_GlobalRead(t *testing.T) {
	reg := newReg(t)
	ctx := context.Background()

	// Seed a non-admin viewer key in tenant "acme".
	acmeToken := seedTenantKey(t, reg, "ti-node-acme", "acme-viewer", "acme", "viewer")

	// Seed two nodes.
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       "node-gpu-01",
		Hostname: "gpu-01.internal",
		State:    "NODE_STATE_READY",
	}); err != nil {
		t.Fatalf("create node gpu-01: %v", err)
	}
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       "node-gpu-02",
		Hostname: "gpu-02.internal",
		State:    "NODE_STATE_READY",
	}); err != nil {
		t.Fatalf("create node gpu-02: %v", err)
	}

	srv := server.New(reg, server.Config{})

	t.Run("non-admin tenant viewer sees all nodes (global read)", func(t *testing.T) {
		body := getNodes(t, srv, acmeToken)
		if len(body.Nodes) != 2 {
			t.Errorf("acme viewer: got %d nodes, want 2 (node list is global); ids=%v",
				len(body.Nodes), tiNodeIDs(body.Nodes))
		}
	})

	t.Run("admin key sees all nodes", func(t *testing.T) {
		adminToken := seedTenantKey(t, reg, "ti-node-admin", "admin", "acme", "admin")
		body := getNodes(t, srv, adminToken)
		if len(body.Nodes) != 2 {
			t.Errorf("admin: got %d nodes, want 2", len(body.Nodes))
		}
	})
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func doGet(t *testing.T, srv interface{ Handler() http.Handler }, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func getDeployments(t *testing.T, srv interface{ Handler() http.Handler }, bearer string) struct {
	Deployments []*registry.Deployment `json:"deployments"`
} {
	t.Helper()
	rec := doGet(t, srv, "/api/v1/deployments", bearer)
	var body struct {
		Deployments []*registry.Deployment `json:"deployments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode deployments: %v; raw=%s", err, rec.Body.String())
	}
	return body
}

func getAPIKeys(t *testing.T, srv interface{ Handler() http.Handler }, bearer string) struct {
	APIKeys []*registry.APIKey `json:"apikeys"`
} {
	t.Helper()
	rec := doGet(t, srv, "/api/v1/apikeys", bearer)
	var body struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode apikeys: %v; raw=%s", err, rec.Body.String())
	}
	return body
}

func getModels(t *testing.T, srv interface{ Handler() http.Handler }, bearer string) struct {
	Models []*registry.Model `json:"models"`
} {
	t.Helper()
	rec := doGet(t, srv, "/api/v1/models", bearer)
	var body struct {
		Models []*registry.Model `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode models: %v; raw=%s", err, rec.Body.String())
	}
	return body
}

func getNodes(t *testing.T, srv interface{ Handler() http.Handler }, bearer string) struct {
	Nodes []*registry.Node `json:"nodes"`
} {
	t.Helper()
	rec := doGet(t, srv, "/api/v1/nodes", bearer)
	var body struct {
		Nodes []*registry.Node `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode nodes: %v; raw=%s", err, rec.Body.String())
	}
	return body
}

// ---------------------------------------------------------------------------
// ID extraction helpers (for error messages)
// ---------------------------------------------------------------------------

func tiDeploymentIDs(deps []*registry.Deployment) []string {
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.ID
	}
	return ids
}

func tiAPIKeyIDs(keys []*registry.APIKey) []string {
	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.ID
	}
	return ids
}

func tiModelIDs(models []*registry.Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

func tiNodeIDs(nodes []*registry.Node) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}
