package server_test

// dataplane_test.go — HTTP-level integration tests for the DataPlane REST API.
//
// Test coverage:
//   - POST /api/v1/platform/dataplanes: 201 with join_token, missing name → 400
//   - GET  /api/v1/platform/dataplanes: 200 list
//   - GET  /api/v1/platform/dataplanes/{id}: 200 round-trip, unknown id → 404
//   - PUT  /api/v1/platform/dataplanes/{id}: 200 update, unknown id → 404
//   - DELETE /api/v1/platform/dataplanes/{id}: 204, unknown id → 404
//   - POST /api/v1/platform/dataplanes/{id}/heartbeat: 204, unknown id → 404
//   - GET  /api/v1/platform/dataplanes/{id}/config: 200 snapshot
//   - GET  /api/v1/platform/dataplanes/{id}/nodes: 200 list
//   - POST /api/v1/platform/dataplanes/{id}/nodes/{nodeId}: 204 assign
//   - DELETE /api/v1/platform/dataplanes/{id}/nodes/{nodeId}: 204 unassign

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func dpJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

// dpCreate POSTs to /api/v1/platform/dataplanes, expects 201, returns decoded body.
func dpCreate(t *testing.T, srv interface{ Handler() http.Handler }, body map[string]any) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/dataplanes", dpJSON(t, body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("dpCreate: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("dpCreate: decode: %v", err)
	}
	return out
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestHandleCreateDataPlane_HappyPath(t *testing.T) {
	srv, _ := newTestServer(t)

	out := dpCreate(t, srv, map[string]any{
		"name":        "prod-cluster",
		"tier":        "production",
		"gateway_url": "https://ai.example.com",
	})

	dp, ok := out["dataplane"].(map[string]any)
	if !ok {
		t.Fatalf("response missing 'dataplane' field: %v", out)
	}
	if dp["name"] != "prod-cluster" {
		t.Errorf("name want %q got %v", "prod-cluster", dp["name"])
	}
	if dp["id"] == "" || dp["id"] == nil {
		t.Error("id should be set")
	}
	tok, _ := out["join_token"].(string)
	if tok == "" {
		t.Error("join_token should be returned on create")
	}
	if len(tok) < 10 {
		t.Errorf("join_token too short: %q", tok)
	}
}

func TestHandleCreateDataPlane_MissingName(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/dataplanes",
		dpJSON(t, map[string]any{"tier": "production"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleListDataPlanes(t *testing.T) {
	srv, _ := newTestServer(t)
	dpCreate(t, srv, map[string]any{"name": "alpha"})
	dpCreate(t, srv, map[string]any{"name": "beta"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/dataplanes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	dps, _ := out["dataplanes"].([]any)
	if len(dps) != 2 {
		t.Errorf("want 2 dataplanes, got %d", len(dps))
	}
}

func TestHandleGetDataPlane(t *testing.T) {
	srv, _ := newTestServer(t)
	created := dpCreate(t, srv, map[string]any{"name": "get-dp"})
	dp := created["dataplane"].(map[string]any)
	id := dp["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/dataplanes/"+id, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "get-dp" {
		t.Errorf("name mismatch: %v", got["name"])
	}
}

func TestHandleGetDataPlane_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/dataplanes/ghost", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404 got %d", rec.Code)
	}
}

func TestHandleUpdateDataPlane(t *testing.T) {
	srv, _ := newTestServer(t)
	created := dpCreate(t, srv, map[string]any{"name": "up-dp", "tier": "production"})
	dp := created["dataplane"].(map[string]any)
	id := dp["id"].(string)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/platform/dataplanes/"+id,
		dpJSON(t, map[string]any{"status": "active", "tier": "staging"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["status"] != "active" {
		t.Errorf("status want 'active' got %v", got["status"])
	}
	if got["tier"] != "staging" {
		t.Errorf("tier want 'staging' got %v", got["tier"])
	}
}

func TestHandleUpdateDataPlane_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/platform/dataplanes/ghost",
		dpJSON(t, map[string]any{"status": "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404 got %d", rec.Code)
	}
}

func TestHandleDeleteDataPlane(t *testing.T) {
	srv, _ := newTestServer(t)
	created := dpCreate(t, srv, map[string]any{"name": "del-dp"})
	dp := created["dataplane"].(map[string]any)
	id := dp["id"].(string)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/platform/dataplanes/"+id, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204 got %d; body=%s", rec.Code, rec.Body.String())
	}

	// Verify gone.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/platform/dataplanes/"+id, nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("want 404 after delete, got %d", rec2.Code)
	}
}

func TestHandleDeleteDataPlane_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/platform/dataplanes/ghost", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404 got %d", rec.Code)
	}
}

func TestHandleDataPlaneHeartbeat(t *testing.T) {
	srv, _ := newTestServer(t)
	created := dpCreate(t, srv, map[string]any{"name": "hb-dp"})
	dp := created["dataplane"].(map[string]any)
	id := dp["id"].(string)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/platform/dataplanes/"+id+"/heartbeat",
		dpJSON(t, map[string]any{"status": "active", "node_count": 2}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("want 204 got %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDataPlaneHeartbeat_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/platform/dataplanes/ghost/heartbeat",
		dpJSON(t, map[string]any{"status": "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404 got %d", rec.Code)
	}
}

func TestHandleGetDataPlaneConfig(t *testing.T) {
	srv, _ := newTestServer(t)
	created := dpCreate(t, srv, map[string]any{"name": "cfg-dp"})
	dp := created["dataplane"].(map[string]any)
	id := dp["id"].(string)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/platform/dataplanes/"+id+"/config", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d; body=%s", rec.Code, rec.Body.String())
	}
	var snap map[string]any
	json.Unmarshal(rec.Body.Bytes(), &snap)
	if _, ok := snap["routing_table"]; !ok {
		t.Error("snapshot should have routing_table field")
	}
}

func TestHandleDataPlaneNodeAssignment(t *testing.T) {
	srv, reg := newTestServer(t)
	created := dpCreate(t, srv, map[string]any{"name": "node-dp"})
	dp := created["dataplane"].(map[string]any)
	dpID := dp["id"].(string)

	// Create a node.
	node := &registry.Node{
		ID:       "node-dp-1",
		Hostname: "gpu01.local",
		OS:       "linux",
		Arch:     "amd64",
		State:    "NODE_STATE_READY",
	}
	if err := reg.CreateNode(context.Background(), node); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Assign node to DP.
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/platform/dataplanes/"+dpID+"/nodes/"+node.ID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("assign: want 204 got %d; body=%s", rec.Code, rec.Body.String())
	}

	// List nodes.
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/v1/platform/dataplanes/"+dpID+"/nodes", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list: want 200 got %d", rec2.Code)
	}
	var out map[string]any
	json.Unmarshal(rec2.Body.Bytes(), &out)
	nodes, _ := out["nodes"].([]any)
	if len(nodes) != 1 {
		t.Errorf("want 1 node in DP, got %d", len(nodes))
	}

	// Unassign.
	req3 := httptest.NewRequest(http.MethodDelete,
		"/api/v1/platform/dataplanes/"+dpID+"/nodes/"+node.ID, nil)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNoContent {
		t.Fatalf("unassign: want 204 got %d", rec3.Code)
	}
}
