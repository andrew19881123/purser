package server_test

// platform_status_test.go — tests for GET /api/v1/platform/status and
// GET /api/v1/platform/health.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
)

// TestPlatformStatus_AdminCanAccess verifies that GET /api/v1/platform/status
// returns 200 with the expected top-level keys.
func TestPlatformStatus_AdminCanAccess(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	for _, key := range []string{"platform_version", "organizations", "teams", "node_pools", "platform_users", "features"} {
		if _, ok := body[key]; !ok {
			t.Errorf("response missing key %q; got %v", key, body)
		}
	}
	if v, _ := body["platform_version"].(string); v != "v0.4" {
		t.Errorf("platform_version = %q, want v0.4", v)
	}
}

// TestPlatformStatus_CountsOrgsAndPools creates 2 orgs, 1 team, and 1 pool
// then verifies the counts returned by /api/v1/platform/status are correct.
func TestPlatformStatus_CountsOrgsAndPools(t *testing.T) {
	srv, reg := newTestServer(t)
	ctx := context.Background()

	// Seed 2 organisations.
	if err := reg.CreateOrganization(ctx, &registry.Organization{
		ID: "org-1", Name: "Org One", Slug: "org-one",
	}); err != nil {
		t.Fatalf("create org 1: %v", err)
	}
	if err := reg.CreateOrganization(ctx, &registry.Organization{
		ID: "org-2", Name: "Org Two", Slug: "org-two",
	}); err != nil {
		t.Fatalf("create org 2: %v", err)
	}

	// Seed 1 team inside org-1.
	if err := reg.CreateTeam(ctx, &registry.Team{
		ID: "team-1", OrgID: "org-1", Name: "Alpha", Slug: "alpha",
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}

	// Seed 1 node pool.
	if err := reg.CreateNodePool(ctx, &registry.NodePool{
		ID: "pool-1", Name: "GPU Lab", OwnerType: "team", OwnerID: "team-1", Policy: "exclusive",
	}); err != nil {
		t.Fatalf("create pool: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	checkCount := func(field string, want float64) {
		t.Helper()
		v, ok := body[field].(float64)
		if !ok {
			t.Errorf("%s: expected numeric value, got %T(%v)", field, body[field], body[field])
			return
		}
		if v != want {
			t.Errorf("%s = %v, want %v", field, v, want)
		}
	}

	checkCount("organizations", 2)
	checkCount("teams", 1)
	checkCount("node_pools", 1)
}

// TestPlatformHealth_NoAuth_Returns200 verifies that GET /api/v1/platform/health
// is accessible without any authentication and returns 200 with status "ok".
func TestPlatformHealth_NoAuth_Returns200(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/health", nil)
	// No Authorization header — must succeed without credentials.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if v, _ := body["status"].(string); v != "ok" {
		t.Errorf("status = %q, want ok", v)
	}
	if v, _ := body["version"].(string); v != "v0.4" {
		t.Errorf("version = %q, want v0.4", v)
	}
}

// TestPlatformHealth_DBDown_Returns503 verifies that when the backing store
// is closed, GET /api/v1/platform/health returns 503 Service Unavailable.
func TestPlatformHealth_DBDown_Returns503(t *testing.T) {
	srv, reg := newTestServer(t)

	// Close the registry to simulate a database outage.
	_ = reg.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if v, _ := body["status"].(string); v != "unhealthy" {
		t.Errorf("status = %q, want unhealthy", v)
	}
	if _, ok := body["error"]; !ok {
		t.Error("response missing 'error' field")
	}
}
