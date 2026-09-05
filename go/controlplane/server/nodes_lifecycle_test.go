package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newFleet builds a real fleet.Manager (backed by reg plus an ephemeral internal
// CA) so the node-lifecycle endpoints exercise the real drain/decommission state
// transitions, cert revocation and audit writes rather than a stub double.
func newFleet(t *testing.T, reg registry.Registry) *fleet.Manager {
	t.Helper()
	ca, err := pki.New(context.Background(), reg, pki.Options{})
	if err != nil {
		t.Fatalf("pki.New: %v", err)
	}
	return fleet.NewWithSecret(reg, ca, []byte("test-secret-key"))
}

func nodeState(t *testing.T, reg registry.Registry, id string) string {
	t.Helper()
	n, err := reg.GetNode(context.Background(), id)
	if err != nil {
		t.Fatalf("GetNode(%q): %v", id, err)
	}
	return n.State
}

// detailOnNode builds a Deployment.Detail blob placing an engine (and optionally
// the host) on nodeID, matching orchestrator.DeploymentDetail's JSON shape.
func detailOnNode(t *testing.T, nodeID string, asHost bool) json.RawMessage {
	t.Helper()
	m := map[string]any{
		"engines": []map[string]any{{"node_id": nodeID, "role": "worker"}},
	}
	if asHost {
		m["host_node_id"] = nodeID
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	return b
}

// TestHandleDrainNode exercises POST /api/v1/nodes/{id}/drain: draining a known
// node cordons it (state -> DRAINING, 200), and an unknown node is 404.
func TestHandleDrainNode(t *testing.T) {
	t.Run("existing", func(t *testing.T) {
		reg := newReg(t)
		seedReadyNode(t, reg, "node-a", 64)
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/drain", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got := nodeState(t, reg, "node-a"); got != fleet.NodeStateDraining {
			t.Errorf("node state = %q, want %q", got, fleet.NodeStateDraining)
		}
	})

	t.Run("missing", func(t *testing.T) {
		reg := newReg(t)
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/nodes/nope/drain", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleDeleteNode exercises the guarded DELETE /api/v1/nodes/{id}: a clean
// decommission (204 + state DECOMMISSIONED), an unknown id (404), and a node
// still occupied by a non-terminal deployment (409 node_in_use). Decommission is
// a lifecycle transition, so the node remains in the registry as DECOMMISSIONED
// rather than being removed.
func TestHandleDeleteNode(t *testing.T) {
	ctx := context.Background()

	t.Run("existing, no hosted deployments", func(t *testing.T) {
		reg := newReg(t)
		seedReadyNode(t, reg, "node-a", 64)
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/node-a", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("204 body = %q, want empty", rec.Body.String())
		}
		if got := nodeState(t, reg, "node-a"); got != fleet.NodeStateDecommissioned {
			t.Errorf("node state = %q, want %q", got, fleet.NodeStateDecommissioned)
		}
	})

	t.Run("missing", func(t *testing.T) {
		reg := newReg(t)
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("hosts a non-terminal deployment", func(t *testing.T) {
		reg := newReg(t)
		seedReadyNode(t, reg, "node-busy", 64)
		if err := reg.CreateDeployment(ctx, &registry.Deployment{
			ID: "d-live", ModelID: "m1", State: "DEPLOYMENT_STATE_ACTIVE",
			Detail: detailOnNode(t, "node-busy", true),
		}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/node-busy", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "node_in_use") || !strings.Contains(body, "d-live") {
			t.Errorf("409 body missing type or blocking id: %s", body)
		}
		// The node must NOT have been decommissioned.
		if got := nodeState(t, reg, "node-busy"); got != "NODE_STATE_READY" {
			t.Errorf("node mutated despite 409: state = %q, want NODE_STATE_READY", got)
		}
	})

	t.Run("terminal deployment on node does not block", func(t *testing.T) {
		reg := newReg(t)
		seedReadyNode(t, reg, "node-c", 64)
		if err := reg.CreateDeployment(ctx, &registry.Deployment{
			ID: "d-stopped", ModelID: "m1", State: "DEPLOYMENT_STATE_STOPPED",
			Detail: detailOnNode(t, "node-c", true),
		}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/node-c", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("deployment on a different node does not block", func(t *testing.T) {
		reg := newReg(t)
		seedReadyNode(t, reg, "node-d", 64)
		if err := reg.CreateDeployment(ctx, &registry.Deployment{
			ID: "d-elsewhere", ModelID: "m1", State: "DEPLOYMENT_STATE_ACTIVE",
			Detail: detailOnNode(t, "node-other", true),
		}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
		srv := server.New(reg, server.Config{Fleet: newFleet(t, reg)})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/node-d", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
	})
}
