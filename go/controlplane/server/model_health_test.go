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

// TestHandleModelHealth exercises GET /api/v1/models/{id}/health with a
// table-driven suite covering the three health statuses and the 404 path.
func TestHandleModelHealth(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		deploymentState string // empty → no deployment
		wantStatus      int
		wantHealth      string // "healthy" | "degraded" | "unavailable" | ""
	}{
		{
			name:            "active deployment is healthy",
			deploymentState: "DEPLOYMENT_STATE_ACTIVE",
			wantStatus:      http.StatusOK,
			wantHealth:      "healthy",
		},
		{
			name:            "provisioning deployment is degraded",
			deploymentState: "DEPLOYMENT_STATE_PROVISIONING",
			wantStatus:      http.StatusOK,
			wantHealth:      "degraded",
		},
		{
			name:            "stopping deployment is degraded",
			deploymentState: "DEPLOYMENT_STATE_STOPPING",
			wantStatus:      http.StatusOK,
			wantHealth:      "degraded",
		},
		{
			name:            "failed deployment is unavailable",
			deploymentState: "DEPLOYMENT_STATE_FAILED",
			wantStatus:      http.StatusOK,
			wantHealth:      "unavailable",
		},
		{
			name:            "stopped deployment is unavailable",
			deploymentState: "DEPLOYMENT_STATE_STOPPED",
			wantStatus:      http.StatusOK,
			wantHealth:      "unavailable",
		},
		{
			name:            "no deployment is unavailable",
			deploymentState: "", // no deployment row created
			wantStatus:      http.StatusOK,
			wantHealth:      "unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := newReg(t)
			if err := reg.CreateModel(ctx, &registry.Model{ID: "m-health", Family: "llama"}); err != nil {
				t.Fatalf("seed model: %v", err)
			}
			if tc.deploymentState != "" {
				if err := reg.CreateDeployment(ctx, &registry.Deployment{
					ID:      "dep-health",
					ModelID: "m-health",
					State:   tc.deploymentState,
				}); err != nil {
					t.Fatalf("seed deployment: %v", err)
				}
			}

			srv := server.New(reg, server.Config{})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/m-health/health", nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("content-type = %q, want application/json", ct)
			}

			var body server.ModelHealth
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
			}
			if body.ModelID != "m-health" {
				t.Errorf("model_id = %q, want m-health", body.ModelID)
			}
			if body.Status != tc.wantHealth {
				t.Errorf("status = %q, want %q; full body=%s", body.Status, tc.wantHealth, rec.Body.String())
			}
			if tc.deploymentState != "" && body.DeploymentID == "" {
				t.Errorf("deployment_id empty but deployment was created")
			}
		})
	}
}

// TestHandleModelHealthUnknownModel verifies that a request for a model that
// does not exist in the catalog returns 404.
func TestHandleModelHealthUnknownModel(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/no-such-model/health", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
