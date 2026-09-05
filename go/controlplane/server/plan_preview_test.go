package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/planning"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// TestHandlePreviewPlan exercises the read-only dry-run endpoint
// POST /api/v1/models/{id}/plan: a feasible model returns 200 + feasible:true
// with an inline plan (and NOTHING persisted / no audit event), a model too big
// for the fleet returns 200 + feasible:false + reason (never a 4xx), and an
// unknown model returns 404.
func TestHandlePreviewPlan(t *testing.T) {
	tests := []struct {
		name       string
		requestID  string
		seed       func(t *testing.T, reg registry.Registry)
		wantStatus int
		validate   func(t *testing.T, reg registry.Registry, srv *server.Server, body []byte)
	}{
		{
			name:      "fits: 200 feasible with inline plan, nothing persisted",
			requestID: "llama-8b",
			seed: func(t *testing.T, reg registry.Registry) {
				seedModel(t, reg, fittingSpec())
				seedReadyNode(t, reg, "node-a", 64)
			},
			wantStatus: http.StatusOK,
			validate: func(t *testing.T, reg registry.Registry, srv *server.Server, body []byte) {
				// Top level uses the registry.Plan Go json tags (snake_case);
				// the nested "plan" blob is protojson (lowerCamelCase).
				var resp struct {
					Feasible     bool   `json:"feasible"`
					ID           string `json:"id"`
					ModelID      string `json:"model_id"`
					Quantization string `json:"quantization"`
					Plan         struct {
						ModelID     string `json:"modelId"`
						Assignments []struct {
							NodeID string `json:"nodeId"`
						} `json:"assignments"`
						PipelineOrder []string `json:"pipelineOrder"`
						Explanation   []string `json:"explanation"`
					} `json:"plan"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v; raw=%s", err, body)
				}
				if !resp.Feasible {
					t.Errorf("feasible = false, want true")
				}
				if resp.ModelID != "llama-8b" || resp.Plan.ModelID != "llama-8b" {
					t.Errorf("model_id = %q / plan.modelId = %q, want llama-8b", resp.ModelID, resp.Plan.ModelID)
				}
				if resp.Quantization == "" {
					t.Errorf("quantization is empty, want the chosen quant")
				}
				// The single fitting node yields one assignment and a
				// one-element pipeline order.
				if len(resp.Plan.Assignments) != 1 {
					t.Errorf("assignments = %d, want 1", len(resp.Plan.Assignments))
				}
				if len(resp.Plan.PipelineOrder) != 1 {
					t.Errorf("pipeline_order = %d, want 1", len(resp.Plan.PipelineOrder))
				}
				if len(resp.Plan.Explanation) == 0 {
					t.Errorf("expected a human-readable explanation, got none")
				}
				// Nothing persisted: the plans table stays empty and the
				// ephemeral id resolves to no stored plan.
				if plans, _ := reg.ListPlans(context.Background()); len(plans) != 0 {
					t.Errorf("preview must persist nothing, got %d plan(s)", len(plans))
				}
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plans/"+resp.ID, nil))
				if rec.Code != http.StatusNotFound {
					t.Errorf("GET /plans/%s = %d, want 404 (nothing persisted)", resp.ID, rec.Code)
				}
				// No audit event for a read-only preview.
				if rows, _ := reg.ListAudit(context.Background(), 10); len(rows) != 0 {
					t.Errorf("preview must emit no audit event, got %d entr(ies)", len(rows))
				}
			},
		},
		{
			name:      "too big: 200 feasible:false with reason, nothing persisted",
			requestID: "llama-huge",
			seed: func(t *testing.T, reg registry.Registry) {
				seedModel(t, reg, oversizedSpec())
				seedReadyNode(t, reg, "node-a", 64)
			},
			wantStatus: http.StatusOK,
			validate: func(t *testing.T, reg registry.Registry, srv *server.Server, body []byte) {
				var resp struct {
					Feasible bool   `json:"feasible"`
					Reason   string `json:"reason"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v; raw=%s", err, body)
				}
				if resp.Feasible {
					t.Errorf("feasible = true, want false for an oversized model")
				}
				if resp.Reason == "" {
					t.Errorf("expected a non-empty reason explaining infeasibility")
				}
				if plans, _ := reg.ListPlans(context.Background()); len(plans) != 0 {
					t.Errorf("infeasible preview must persist nothing, got %d plan(s)", len(plans))
				}
			},
		},
		{
			name:      "missing model: 404",
			requestID: "nope",
			seed: func(t *testing.T, reg registry.Registry) {
				seedReadyNode(t, reg, "node-a", 64)
			},
			wantStatus: http.StatusNotFound,
			validate:   func(t *testing.T, reg registry.Registry, srv *server.Server, body []byte) {},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := newReg(t)
			tc.seed(t, reg)
			srv := server.New(reg, server.Config{Deployer: &fakeDeployer{}, Planner: planning.New(reg)})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/models/"+tc.requestID+"/plan", nil)
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			tc.validate(t, reg, srv, rec.Body.Bytes())
		})
	}
}
