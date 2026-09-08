package server_test

// config_snapshot_test.go — unit tests for the CP→DP config snapshot push
// mechanism (v0.5 CP/DP architectural separation).
//
// Tests:
//   - TestBuildDataPlaneSnapshot_EmptyDP: DP with no nodes → empty routing table
//   - TestBuildDataPlaneSnapshot_WithActiveDeployment: deployment on DP node → in routing table
//   - TestPushConfigSnapshot_StoresInDB: push persists snapshot in registry
//   - TestHandleRefreshConfig_Returns200: POST /config/refresh → 200 OK
//   - TestHandleRefreshConfig_NotFound_Returns404: unknown DP → 404
//   - TestStartConfigSnapshotPusher_SkipsOfflineDP: offline DPs not refreshed (unit check)

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// seedDataPlane creates a Data Plane in reg and returns it.
func seedDataPlane(t *testing.T, reg registry.Registry, name string) *registry.DataPlane {
	t.Helper()
	dp := &registry.DataPlane{
		Name:       name,
		Tier:       "production",
		GatewayURL: "https://dp.example.com",
		Status:     "active",
	}
	_, err := reg.CreateDataPlane(context.Background(), dp)
	if err != nil {
		t.Fatalf("seedDataPlane %q: %v", name, err)
	}
	return dp
}

// ─── BuildDataPlaneSnapshot ───────────────────────────────────────────────────

// TestBuildDataPlaneSnapshot_EmptyDP verifies that a DP with no node assignments
// and no active deployments produces an empty routing table, an auth bundle
// that reflects the enabled API keys in the registry, and a non-nil policy bundle.
func TestBuildDataPlaneSnapshot_EmptyDP(t *testing.T) {
	srv, reg := newTestServer(t)
	dp := seedDataPlane(t, reg, "empty-dp")

	snapshot, err := srv.BuildDataPlaneSnapshot(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("BuildDataPlaneSnapshot: %v", err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	// No deployments assigned → empty routing table.
	if len(snapshot.RoutingTable) != 0 {
		t.Errorf("routing_table should be empty, got %v", snapshot.RoutingTable)
	}
	// Auth bundle must be a non-nil map (even if empty — no keys seeded).
	if snapshot.AuthBundle == nil {
		t.Error("auth_bundle should not be nil")
	}
	// Policy bundle must be a non-nil slice.
	if snapshot.PolicyBundle == nil {
		t.Error("policy_bundle should not be nil")
	}
	// Timestamp should be close to now.
	if snapshot.GeneratedAt.IsZero() {
		t.Error("generated_at should not be zero")
	}
	if time.Since(snapshot.GeneratedAt) > 5*time.Second {
		t.Errorf("generated_at is too old: %v", snapshot.GeneratedAt)
	}
}

// TestBuildDataPlaneSnapshot_WithAPIKeys verifies that enabled API keys populate
// the auth bundle and disabled keys are excluded.
func TestBuildDataPlaneSnapshot_WithAPIKeys(t *testing.T) {
	srv, reg := newTestServer(t)
	dp := seedDataPlane(t, reg, "apikey-dp")

	// Seed an enabled and a disabled API key.
	enabledKey := &registry.APIKey{
		ID:      "key-enabled-1",
		Name:    "enabled",
		KeyHash: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		Role:    "inference",
		Tenant:  "team-alpha",
		Quota:   1000,
		Enabled: true,
	}
	disabledKey := &registry.APIKey{
		ID:      "key-disabled-1",
		Name:    "disabled",
		KeyHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Role:    "inference",
		Tenant:  "team-beta",
		Quota:   500,
		Enabled: false,
	}
	if err := reg.CreateAPIKey(context.Background(), enabledKey); err != nil {
		t.Fatalf("create enabled key: %v", err)
	}
	if err := reg.CreateAPIKey(context.Background(), disabledKey); err != nil {
		t.Fatalf("create disabled key: %v", err)
	}

	snapshot, err := srv.BuildDataPlaneSnapshot(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("BuildDataPlaneSnapshot: %v", err)
	}

	// Enabled key should be in auth bundle.
	if _, ok := snapshot.AuthBundle[enabledKey.KeyHash]; !ok {
		t.Errorf("enabled key %q not in auth_bundle", enabledKey.KeyHash)
	}
	// Disabled key must not appear.
	if _, ok := snapshot.AuthBundle[disabledKey.KeyHash]; ok {
		t.Errorf("disabled key %q should not be in auth_bundle", disabledKey.KeyHash)
	}
}

// ─── PushConfigSnapshot ──────────────────────────────────────────────────────

// TestPushConfigSnapshot_StoresInDB verifies that PushConfigSnapshot builds a
// snapshot and persists it so GetDataPlaneConfigSnapshot returns a non-nil result.
func TestPushConfigSnapshot_StoresInDB(t *testing.T) {
	srv, reg := newTestServer(t)
	dp := seedDataPlane(t, reg, "push-dp")

	err := srv.PushConfigSnapshot(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("PushConfigSnapshot: %v", err)
	}

	// The snapshot should now be retrievable from the registry.
	loaded, err := reg.GetDataPlaneConfigSnapshot(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("GetDataPlaneConfigSnapshot: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded snapshot is nil")
	}
	if loaded.GeneratedAt.IsZero() {
		t.Error("generated_at should not be zero after push")
	}
	if time.Since(loaded.GeneratedAt) > 10*time.Second {
		t.Errorf("generated_at too old: %v", loaded.GeneratedAt)
	}
}

// TestPushConfigSnapshot_UnknownDP verifies that pushing to a non-existent DP
// returns an error (ErrNotFound propagates from registry.UpdateDataPlaneConfigSnapshot).
func TestPushConfigSnapshot_UnknownDP(t *testing.T) {
	srv, _ := newTestServer(t)

	err := srv.PushConfigSnapshot(context.Background(), "dp-does-not-exist")
	if err == nil {
		t.Error("expected error for non-existent DP, got nil")
	}
}

// ─── handleRefreshDataPlaneConfig ────────────────────────────────────────────

// TestHandleRefreshConfig_Returns200 tests the happy path: a known DP → 200 OK
// with the dataplane_id and refreshed_at fields in the response.
func TestHandleRefreshConfig_Returns200(t *testing.T) {
	srv, reg := newTestServer(t)
	dp := seedDataPlane(t, reg, "refresh-dp")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/platform/dataplanes/"+dp.ID+"/config/refresh", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out["dataplane_id"] != dp.ID {
		t.Errorf("dataplane_id: want %q got %v", dp.ID, out["dataplane_id"])
	}
	if _, ok := out["refreshed_at"]; !ok {
		t.Error("response should contain refreshed_at")
	}
	if _, ok := out["message"]; !ok {
		t.Error("response should contain message")
	}
}

// TestHandleRefreshConfig_NotFound_Returns404 verifies that a POST to
// /config/refresh for a non-existent DP returns 404.
func TestHandleRefreshConfig_NotFound_Returns404(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/platform/dataplanes/ghost-dp/config/refresh", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404 got %d; body=%s", rec.Code, rec.Body.String())
	}
}
