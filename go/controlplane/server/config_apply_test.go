package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/config"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// validPurserYAML is a minimal valid purser.yaml for tests.
const validPurserYAML = `apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: test-cluster
cluster:
  id: test
models:
  - id: test-model
    source:
      type: huggingface
      repo: TestOrg/TestModel
`

// invalidPurserYAML triggers a validation error (missing model id).
const invalidPurserYAML = `apiVersion: purser/v1
kind: ClusterConfig
models:
  - source:
      type: huggingface
`

// TestHandleConfigDiff_ValidYAML verifies that POST /api/v1/config/diff with a
// valid purser.yaml returns HTTP 200 with a JSON diff result.
func TestHandleConfigDiff_ValidYAML(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/diff",
		bytes.NewBufferString(validPurserYAML))
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	var body struct {
		ModelsToAdd      []any `json:"models_to_add"`
		ModelsToRemove   []any `json:"models_to_remove"`
		DeploymentsToAdd []any `json:"deployments_to_add"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	// New registry has no models, so test-model should appear in models_to_add.
	if len(body.ModelsToAdd) != 1 {
		t.Errorf("models_to_add len = %d, want 1; body=%s", len(body.ModelsToAdd), rec.Body.String())
	}
	if body.ModelsToRemove == nil {
		t.Error("models_to_remove must not be null (want [])")
	}
}

// TestHandleConfigDiff_InvalidYAML verifies that POST /api/v1/config/diff with
// malformed / invalid YAML returns HTTP 400 with an error message.
func TestHandleConfigDiff_InvalidYAML(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/diff",
		bytes.NewBufferString(invalidPurserYAML))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v; raw=%s", err, rec.Body.String())
	}
	if _, ok := body["error"]; !ok {
		t.Error("expected 'error' field in 400 response body")
	}
}

// TestHandleConfigApply_ViewerForbidden verifies that a viewer-role API key
// cannot call POST /api/v1/config/apply (write operation — 403 expected).
func TestHandleConfigApply_ViewerForbidden(t *testing.T) {
	reg := newReg(t)
	viewerToken := seedKeyWithRole(t, reg, "key-viewer", "viewer", "viewer")
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply",
		bytes.NewBufferString(validPurserYAML))
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer on POST /config/apply: status = %d, want 403; body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestHandleConfigApply_Admin verifies that an admin-role API key can call
// POST /api/v1/config/apply and that the response contains an "applied" object.
func TestHandleConfigApply_Admin(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "key-admin", "admin", "admin")
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply",
		bytes.NewBufferString(validPurserYAML))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin on POST /config/apply: status = %d, want 200; body=%s",
			rec.Code, rec.Body.String())
	}
	var body struct {
		Applied struct {
			ModelsAdded      int `json:"models_added"`
			DeploymentsAdded int `json:"deployments_added"`
		} `json:"applied"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	// One model declared in the YAML; registry was empty, so it should be added.
	if body.Applied.ModelsAdded != 1 {
		t.Errorf("applied.models_added = %d, want 1; body=%s", body.Applied.ModelsAdded, rec.Body.String())
	}

	// Verify the model was actually created in the registry.
	m, err := reg.GetModel(context.Background(), "test-model")
	if err != nil {
		t.Fatalf("GetModel after apply: %v", err)
	}
	if m.ID != "test-model" {
		t.Errorf("model ID = %q, want test-model", m.ID)
	}
}

// TestHandleConfigExport_OK verifies that GET /api/v1/config/export returns
// HTTP 200 with Content-Type application/yaml and a valid YAML body.
func TestHandleConfigExport_OK(t *testing.T) {
	reg := newReg(t)
	// Seed one model so the export is non-trivial.
	if err := reg.CreateModel(context.Background(), &registry.Model{
		ID:   "exported-model",
		Type: "llm",
	}); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/export", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/yaml" {
		t.Errorf("content-type = %q, want application/yaml", ct)
	}
	// The body must contain the known model id.
	body := rec.Body.String()
	if !strings.Contains(body, "exported-model") {
		t.Errorf("export body missing 'exported-model'; got:\n%s", body)
	}
	// Must declare apiVersion and kind so it round-trips through Load.
	if !strings.Contains(body, "purser/v1") {
		t.Errorf("export body missing apiVersion purser/v1; got:\n%s", body)
	}
}

// TestHandleConfigDiff_EmptyBody verifies that an empty body returns 400.
func TestHandleConfigDiff_EmptyBody(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/diff",
		bytes.NewBufferString(""))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleConfigApply_Idempotent verifies that applying the same YAML twice
// does not return an error on the second call (model already exists → skipped).
func TestHandleConfigApply_Idempotent(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "key-admin-idem", "admin-idem", "admin")
	srv := server.New(reg, server.Config{})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply",
			bytes.NewBufferString(validPurserYAML))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("iteration %d: status = %d, want 200; body=%s",
				i, rec.Code, rec.Body.String())
		}
	}
}

// orgOnlyYAML is a minimal valid purser.yaml that declares one org and one team.
const orgOnlyYAML = `apiVersion: purser/v1
kind: ClusterConfig
orgs:
  - id: acme
    name: "Acme Corp"
    slug: acme
    teams:
      - id: ml-team
        name: "ML Team"
        slug: ml-team
`

// nodePoolOnlyYAML is a minimal valid purser.yaml that declares one node pool.
const nodePoolOnlyYAML = `apiVersion: purser/v1
kind: ClusterConfig
node_pools:
  - id: gpu-lab
    name: "GPU Lab"
    owner_type: team
    owner_id: ml-team
    policy: exclusive
`

// orgAndPoolYAML is a valid purser.yaml with one org and one node pool.
const orgAndPoolYAML = `apiVersion: purser/v1
kind: ClusterConfig
orgs:
  - id: acme-combined
    name: "Acme Combined"
    slug: acme-combined
    teams:
      - id: eng-team
        name: "Engineering"
        slug: eng-team
node_pools:
  - id: eng-gpu-pool
    name: "Engineering GPU Pool"
    owner_type: team
    owner_id: eng-team
    policy: exclusive
`

// TestApplyClusterConfig_CreatesOrg verifies that ApplyClusterConfig creates the
// declared org and its nested team when they do not already exist in the registry.
func TestApplyClusterConfig_CreatesOrg(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	cfg, err := config.Load([]byte(orgOnlyYAML))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	result, err := srv.ApplyClusterConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ApplyClusterConfig: %v", err)
	}
	if result.OrgsAdded != 1 {
		t.Errorf("orgs_added = %d, want 1", result.OrgsAdded)
	}

	// Verify the org was created with the correct name.
	org, err := reg.GetOrganizationBySlug(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrganizationBySlug(acme): %v", err)
	}
	if org.Name != "Acme Corp" {
		t.Errorf("org.Name = %q, want \"Acme Corp\"", org.Name)
	}

	// Verify the nested team was created.
	team, err := reg.GetTeam(ctx, "ml-team")
	if err != nil {
		t.Fatalf("GetTeam(ml-team): %v", err)
	}
	if team.OrgID != org.ID {
		t.Errorf("team.OrgID = %q, want %q", team.OrgID, org.ID)
	}
}

// TestApplyClusterConfig_CreatesNodePool verifies that ApplyClusterConfig creates
// the declared node pool when it does not already exist in the registry.
func TestApplyClusterConfig_CreatesNodePool(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	cfg, err := config.Load([]byte(nodePoolOnlyYAML))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	result, err := srv.ApplyClusterConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ApplyClusterConfig: %v", err)
	}
	if result.NodePoolsAdded != 1 {
		t.Errorf("node_pools_added = %d, want 1", result.NodePoolsAdded)
	}

	// Verify the pool was created with the correct attributes.
	pool, err := reg.GetNodePool(ctx, "gpu-lab")
	if err != nil {
		t.Fatalf("GetNodePool(gpu-lab): %v", err)
	}
	if pool.Name != "GPU Lab" {
		t.Errorf("pool.Name = %q, want \"GPU Lab\"", pool.Name)
	}
	if pool.Policy != "exclusive" {
		t.Errorf("pool.Policy = %q, want exclusive", pool.Policy)
	}
}

// TestApplyClusterConfig_IdempotentOrg verifies that applying the same YAML twice
// produces no error and no duplicate org on the second call.
func TestApplyClusterConfig_IdempotentOrg(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})
	ctx := context.Background()

	cfg, err := config.Load([]byte(orgOnlyYAML))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	// First apply: org should be created.
	r1, err := srv.ApplyClusterConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("first ApplyClusterConfig: %v", err)
	}
	if r1.OrgsAdded != 1 {
		t.Errorf("first apply: orgs_added = %d, want 1", r1.OrgsAdded)
	}

	// Second apply: org already exists — no error, orgs_added = 0.
	r2, err := srv.ApplyClusterConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("second ApplyClusterConfig: %v", err)
	}
	if r2.OrgsAdded != 0 {
		t.Errorf("second apply: orgs_added = %d, want 0 (idempotent)", r2.OrgsAdded)
	}

	// Only one org in the registry.
	orgs, err := reg.ListOrganizations(ctx)
	if err != nil {
		t.Fatalf("ListOrganizations: %v", err)
	}
	count := 0
	for _, o := range orgs {
		if o.Slug == "acme" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d orgs with slug=acme, want exactly 1", count)
	}
}

// TestHandleConfigApply_WithOrgsAndPools verifies that POST /api/v1/config/apply
// with an org and a node pool in the YAML returns HTTP 200 with the correct
// orgs_added=1 and node_pools_added=1 counters in the response.
func TestHandleConfigApply_WithOrgsAndPools(t *testing.T) {
	reg := newReg(t)
	adminToken := seedKeyWithRole(t, reg, "key-admin-orgpool", "admin-orgpool", "admin")
	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply",
		bytes.NewBufferString(orgAndPoolYAML))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Applied struct {
			ModelsAdded    int `json:"models_added"`
			OrgsAdded      int `json:"orgs_added"`
			NodePoolsAdded int `json:"node_pools_added"`
		} `json:"applied"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if body.Applied.OrgsAdded != 1 {
		t.Errorf("applied.orgs_added = %d, want 1; body=%s", body.Applied.OrgsAdded, rec.Body.String())
	}
	if body.Applied.NodePoolsAdded != 1 {
		t.Errorf("applied.node_pools_added = %d, want 1; body=%s", body.Applied.NodePoolsAdded, rec.Body.String())
	}
	if body.Applied.ModelsAdded != 0 {
		t.Errorf("applied.models_added = %d, want 0; body=%s", body.Applied.ModelsAdded, rec.Body.String())
	}

	// Verify org and pool are actually in the registry.
	ctx := context.Background()
	org, err := reg.GetOrganizationBySlug(ctx, "acme-combined")
	if err != nil {
		t.Fatalf("GetOrganizationBySlug(acme-combined) after apply: %v", err)
	}
	if org.Name != "Acme Combined" {
		t.Errorf("org.Name = %q, want \"Acme Combined\"", org.Name)
	}
	pool, err := reg.GetNodePool(ctx, "eng-gpu-pool")
	if err != nil {
		t.Fatalf("GetNodePool(eng-gpu-pool) after apply: %v", err)
	}
	if pool.Policy != "exclusive" {
		t.Errorf("pool.Policy = %q, want exclusive", pool.Policy)
	}
}
