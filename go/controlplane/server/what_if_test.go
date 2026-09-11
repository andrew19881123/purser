package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/planning"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// largeSpec returns a model whose single quantisation requires ~40 GB of
// (V)RAM. This exceeds the 20 GB useful memory of the "small" test node
// (seedReadyNode with ramGB=4 gives RAM=4 GB, VRAM=20 GB;
// usefulMemory = min(4,20) = 4 GB < 40+overhead → infeasible) but fits on
// the hypothetical A100 (RAM=256, VRAM=80 → min=80 > 42 GB → feasible).
func largeSpec() *purserv1.ModelSpec {
	return &purserv1.ModelSpec{
		ModelId:       "large-model",
		Layers:        32,
		ParamsTotalB:  20,
		NKvHeads:      8,
		HeadDim:       128,
		AttentionType: purserv1.AttentionType_ATTENTION_TYPE_GQA,
		ContextMax:    4096,
		Quantizations: []*purserv1.Quantization{{Name: "q4_k_m", SizeGb: 40, Quality: 0.8}},
	}
}

// TestWhatIfPlanner_AddsHypotheticalNode verifies that adding a virtual A100
// node (80 GB VRAM / 256 GB RAM) makes a 40 GB model feasible even when the
// real fleet is too small to hold it.
func TestWhatIfPlanner_AddsHypotheticalNode(t *testing.T) {
	reg := newReg(t)
	// Large model; existing fleet cannot deploy it alone.
	seedModel(t, reg, largeSpec())
	// Small node: RAM = 4 GB, VRAM = 20 GB → too small for the 40 GB model.
	seedReadyNode(t, reg, "tiny-node", 4)

	srv := server.New(reg, server.Config{Planner: planning.New(reg)})

	body, _ := json.Marshal(map[string]any{
		"model_id": "large-model",
		"hypothetical_nodes": []map[string]any{
			{
				"node_id":            "hyp-a100",
				"gpu_vram_gb":        80,
				"gpu_count":          1,
				"cpu_cores":          32,
				"ram_gb":             256,
				"net_bandwidth_gbps": 10,
			},
		},
		"include_existing_nodes": false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/planner/what-if", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Feasible bool `json:"feasible"`
		Plan     *struct {
			PipelineDepth int `json:"pipeline_depth"`
			Assignments   []struct {
				NodeID string `json:"node_id"`
			} `json:"assignments"`
			EstimatedDecodeTokSMin float64 `json:"estimated_decode_tok_s_min"`
			EstimatedDecodeTokSMax float64 `json:"estimated_decode_tok_s_max"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw = %s", err, rec.Body.String())
	}

	if !resp.Feasible {
		t.Fatalf("expected feasible=true with hyp-a100 (80 GB); body = %s", rec.Body.String())
	}
	if resp.Plan == nil {
		t.Fatal("plan must be present when feasible=true")
	}
	if resp.Plan.PipelineDepth < 1 {
		t.Errorf("pipeline_depth = %d, want >= 1", resp.Plan.PipelineDepth)
	}
	if len(resp.Plan.Assignments) == 0 {
		t.Error("assignments must be non-empty")
	}
	if resp.Plan.Assignments[0].NodeID != "hyp-a100" {
		t.Errorf("expected assignment on hyp-a100, got %q", resp.Plan.Assignments[0].NodeID)
	}
	// Performance estimate range must be positive.
	if resp.Plan.EstimatedDecodeTokSMax <= 0 {
		t.Errorf("estimated_decode_tok_s_max = %.2f, want > 0", resp.Plan.EstimatedDecodeTokSMax)
	}
}

// TestWhatIfPlanner_CurrentPlanComparison verifies the before/after comparison
// when include_existing_nodes is true: the existing fleet is infeasible; the
// hypothetical fleet (existing + A100) is feasible; the improvement block
// must report "infeasible_to_feasible".
func TestWhatIfPlanner_CurrentPlanComparison(t *testing.T) {
	reg := newReg(t)
	seedModel(t, reg, largeSpec())
	// Existing fleet: RAM = 4 GB → too small for the 40 GB model.
	seedReadyNode(t, reg, "existing-node", 4)

	srv := server.New(reg, server.Config{Planner: planning.New(reg)})

	body, _ := json.Marshal(map[string]any{
		"model_id": "large-model",
		"hypothetical_nodes": []map[string]any{
			{
				"node_id":            "hyp-a100",
				"gpu_vram_gb":        80,
				"gpu_count":          1,
				"cpu_cores":          32,
				"ram_gb":             256,
				"net_bandwidth_gbps": 10,
			},
		},
		"include_existing_nodes": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/planner/what-if", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Feasible    bool `json:"feasible"`
		CurrentPlan *struct {
			Feasible      bool    `json:"feasible"`
			DeficitVRAMgb float64 `json:"deficit_vram_gb"`
		} `json:"current_plan"`
		Improvement *struct {
			DecodeTokSDeltaMin float64 `json:"decode_tok_s_delta_min"`
			DecodeTokSDeltaMax float64 `json:"decode_tok_s_delta_max"`
			FeasibilityChange  string  `json:"feasibility_change"`
		} `json:"improvement"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw = %s", err, rec.Body.String())
	}

	if !resp.Feasible {
		t.Errorf("hypothetical fleet (existing + A100) should be feasible; body = %s",
			rec.Body.String())
	}
	if resp.CurrentPlan == nil {
		t.Fatal("current_plan must be present when include_existing_nodes=true")
	}
	if resp.CurrentPlan.Feasible {
		t.Error("current plan should be infeasible (4 GB RAM cannot hold 40 GB model)")
	}
	if resp.CurrentPlan.DeficitVRAMgb <= 0 {
		t.Errorf("deficit_vram_gb = %.1f, want > 0", resp.CurrentPlan.DeficitVRAMgb)
	}
	if resp.Improvement == nil {
		t.Fatal("improvement must be present when current_plan is included")
	}
	if resp.Improvement.FeasibilityChange != "infeasible_to_feasible" {
		t.Errorf("feasibility_change = %q, want \"infeasible_to_feasible\"",
			resp.Improvement.FeasibilityChange)
	}
	// With current plan infeasible (0 tok/s), delta should equal the hyp throughput.
	if resp.Improvement.DecodeTokSDeltaMin <= 0 {
		t.Errorf("decode_tok_s_delta_min = %.2f, want > 0", resp.Improvement.DecodeTokSDeltaMin)
	}
}

// TestWhatIfPlanner_NoModel verifies that the endpoint returns 404 when the
// requested model is not registered.
func TestWhatIfPlanner_NoModel(t *testing.T) {
	reg := newReg(t)
	seedReadyNode(t, reg, "node-a", 64)
	srv := server.New(reg, server.Config{Planner: planning.New(reg)})

	body, _ := json.Marshal(map[string]any{
		"model_id":               "no-such-model",
		"hypothetical_nodes":     []any{},
		"include_existing_nodes": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/planner/what-if", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not_found") {
		t.Errorf("expected not_found in body; got: %s", rec.Body.String())
	}
}

// TestWhatIfPlanner_EmptyHypotheticalNodes verifies that when hypothetical_nodes
// is empty the endpoint falls back to the real fleet and returns a valid plan
// result rather than an error.
func TestWhatIfPlanner_EmptyHypotheticalNodes(t *testing.T) {
	reg := newReg(t)
	// fittingSpec (from planning_test.go) is a 10 GB model; seedReadyNode with
	// ramGB=64 gives RAM=64, VRAM=80 → fits comfortably.
	seedModel(t, reg, fittingSpec())
	seedReadyNode(t, reg, "real-node", 64)

	srv := server.New(reg, server.Config{Planner: planning.New(reg)})

	body, _ := json.Marshal(map[string]any{
		"model_id":               "llama-8b",
		"hypothetical_nodes":     []any{},
		"include_existing_nodes": false, // empty nodes → falls back to real fleet
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/planner/what-if", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Feasible bool `json:"feasible"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; raw = %s", err, rec.Body.String())
	}
	// With a real node that fits the model, we expect feasible=true.
	if !resp.Feasible {
		t.Errorf("expected feasible=true (real fleet fits the model); body = %s", rec.Body.String())
	}
}

// TestWhatIfPlanner_NoPlannerConfigured verifies that the endpoint returns 501
// when no planner is wired up.
func TestWhatIfPlanner_NoPlannerConfigured(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{}) // no Planner configured

	body, _ := json.Marshal(map[string]any{"model_id": "any"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/planner/what-if", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body = %s", rec.Code, rec.Body.String())
	}
}

// TestWhatIfPlanner_MissingModelID verifies the endpoint returns 400 when
// model_id is missing from the request.
func TestWhatIfPlanner_MissingModelID(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{Planner: planning.New(reg)})

	body, _ := json.Marshal(map[string]any{"hypothetical_nodes": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/planner/what-if", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}
