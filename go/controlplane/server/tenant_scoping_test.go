package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// seedTenantKey inserts an API key with the given tenant and role and returns
// its deterministic plaintext token ("psk_test_" + id). The hash is
// sha256("psk_test_" + id).
func seedTenantKey(t *testing.T, reg registry.Registry, id, name, tenant, role string) string {
	t.Helper()
	plaintext := "psk_test_" + id
	sum := sha256.Sum256([]byte(plaintext))
	hash := hex.EncodeToString(sum[:])
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      id,
		Name:    name,
		KeyHash: hash,
		Tenant:  tenant,
		Role:    role,
		Enabled: true,
	}); err != nil {
		t.Fatalf("seedTenantKey %q: %v", id, err)
	}
	return plaintext
}

// TestListAPIKeys_ViewerSeesOnlyOwnTenant verifies that a viewer API key with
// tenant "acme" only receives keys belonging to "acme" — keys from other
// tenants are excluded from the response.
func TestListAPIKeys_ViewerSeesOnlyOwnTenant(t *testing.T) {
	reg := newReg(t)

	// Viewer key in tenant "acme" — this is also the authenticating key.
	acmeToken := seedTenantKey(t, reg, "key-acme-viewer", "acme-viewer", "acme", "viewer")
	// A second key in a different tenant — must NOT appear in the response.
	seedTenantKey(t, reg, "key-other-viewer", "other-viewer", "other", "viewer")

	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+acmeToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	// Only the "acme" key should appear.
	if len(body.APIKeys) != 1 {
		t.Fatalf("apikeys len = %d, want 1; raw=%s", len(body.APIKeys), rec.Body.String())
	}
	if body.APIKeys[0].ID != "key-acme-viewer" {
		t.Errorf("returned key id = %q, want key-acme-viewer", body.APIKeys[0].ID)
	}
	if body.APIKeys[0].Tenant != "acme" {
		t.Errorf("returned key tenant = %q, want acme", body.APIKeys[0].Tenant)
	}
}

// TestListAPIKeys_AdminSeesAll verifies that an admin API key receives all
// API keys regardless of tenant.
func TestListAPIKeys_AdminSeesAll(t *testing.T) {
	reg := newReg(t)

	adminToken := seedTenantKey(t, reg, "key-admin-all", "admin-all", "acme", "admin")
	seedTenantKey(t, reg, "key-acme-v", "acme-v", "acme", "viewer")
	seedTenantKey(t, reg, "key-other-v", "other-v", "other", "viewer")

	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		APIKeys []*registry.APIKey `json:"apikeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	// Admin must see all 3 keys (admin + 2 viewers across 2 tenants).
	if len(body.APIKeys) != 3 {
		t.Fatalf("apikeys len = %d, want 3; raw=%s", len(body.APIKeys), rec.Body.String())
	}
}

// TestListDeployments_ViewerScoped verifies that a viewer API key with tenant
// "acme" only receives deployments whose Detail JSON contains "tenant":"acme".
// Deployments belonging to other tenants are excluded.
//
// Note: the deployments table has no dedicated tenant_id column — the tenant
// lives in the Detail JSON blob. A top-level tenant_id column is planned for
// v0.4 to make this a proper SQL filter.
func TestListDeployments_ViewerScoped(t *testing.T) {
	reg := newReg(t)

	// Viewer key in tenant "acme".
	acmeToken := seedTenantKey(t, reg, "key-dep-viewer", "dep-viewer", "acme", "viewer")

	// Deployment belonging to "acme" (tenant field in Detail JSON).
	if err := reg.CreateDeployment(context.Background(), &registry.Deployment{
		ID:      "dep-acme",
		ModelID: "model-a",
		State:   "DEPLOYMENT_STATE_ACTIVE",
		Detail:  json.RawMessage(`{"tenant":"acme"}`),
	}); err != nil {
		t.Fatalf("create acme deployment: %v", err)
	}
	// Deployment belonging to a different tenant.
	if err := reg.CreateDeployment(context.Background(), &registry.Deployment{
		ID:      "dep-other",
		ModelID: "model-b",
		State:   "DEPLOYMENT_STATE_ACTIVE",
		Detail:  json.RawMessage(`{"tenant":"other"}`),
	}); err != nil {
		t.Fatalf("create other deployment: %v", err)
	}

	srv := server.New(reg, server.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	req.Header.Set("Authorization", "Bearer "+acmeToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Deployments []*registry.Deployment `json:"deployments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	// Only the "acme" deployment should be visible to the "acme" viewer.
	if len(body.Deployments) != 1 {
		t.Fatalf("deployments len = %d, want 1; raw=%s", len(body.Deployments), rec.Body.String())
	}
	if body.Deployments[0].ID != "dep-acme" {
		t.Errorf("deployment id = %q, want dep-acme", body.Deployments[0].ID)
	}
}
