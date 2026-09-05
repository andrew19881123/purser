package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// multiTeardownDeployer records all Teardown calls, unlike the fakeDeployer
// which tracks only the most recent one.
type multiTeardownDeployer struct {
	tornDown []string
	err      error
}

func (m *multiTeardownDeployer) Apply(_ context.Context, _ *purserv1.DeploymentPlan) (string, error) {
	return "", m.err
}

func (m *multiTeardownDeployer) Teardown(_ context.Context, id string) error {
	m.tornDown = append(m.tornDown, id)
	return m.err
}

// activeDetailOnNode builds the Deployment.Detail JSON blob placing an engine
// on nodeID, in the shape orchestrator.DeploymentDetail uses.
func activeDetailOnNode(t *testing.T, nodeID string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"host_node_id": nodeID,
		"engines":      []map[string]any{{"node_id": nodeID, "role": "host"}},
	})
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	return b
}

// TestHandleRestartNode exercises POST /api/v1/nodes/{id}/restart with three
// table cases:
//   - node with an active deployment → 202 Accepted, Teardown called once;
//   - node with no active deployments → 409 Conflict, nothing torn down;
//   - unknown node → 404 Not Found.
func TestHandleRestartNode(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		nodeID       string
		setup        func(*testing.T, registry.Registry)
		wantStatus   int
		wantBody     string
		wantTornDown int
	}{
		{
			name:   "node with active deployment → 202",
			nodeID: "node-a",
			setup: func(t *testing.T, reg registry.Registry) {
				seedReadyNode(t, reg, "node-a", 64)
				if err := reg.CreateDeployment(ctx, &registry.Deployment{
					ID:     "dep-1",
					State:  "DEPLOYMENT_STATE_ACTIVE",
					Detail: activeDetailOnNode(t, "node-a"),
				}); err != nil {
					t.Fatalf("seed deployment: %v", err)
				}
			},
			wantStatus:   http.StatusAccepted,
			wantBody:     "dep-1",
			wantTornDown: 1,
		},
		{
			name:   "node with no active deployments → 409",
			nodeID: "node-b",
			setup: func(t *testing.T, reg registry.Registry) {
				seedReadyNode(t, reg, "node-b", 64)
				// Stopped deployment on this node does not count.
				if err := reg.CreateDeployment(ctx, &registry.Deployment{
					ID:     "dep-stopped",
					State:  "DEPLOYMENT_STATE_STOPPED",
					Detail: activeDetailOnNode(t, "node-b"),
				}); err != nil {
					t.Fatalf("seed stopped deployment: %v", err)
				}
			},
			wantStatus:   http.StatusConflict,
			wantBody:     "nothing_to_restart",
			wantTornDown: 0,
		},
		{
			name:         "unknown node → 404",
			nodeID:       "node-ghost",
			setup:        func(_ *testing.T, _ registry.Registry) {},
			wantStatus:   http.StatusNotFound,
			wantBody:     "not_found",
			wantTornDown: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := newReg(t)
			tc.setup(t, reg)
			deployer := &multiTeardownDeployer{}
			srv := server.New(reg, server.Config{Deployer: deployer})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPost, "/api/v1/nodes/"+tc.nodeID+"/restart", nil)
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want to contain %q",
					rec.Body.String(), tc.wantBody)
			}
			if len(deployer.tornDown) != tc.wantTornDown {
				t.Errorf("Teardown called %d time(s), want %d; ids = %v",
					len(deployer.tornDown), tc.wantTornDown, deployer.tornDown)
			}
		})
	}
}
