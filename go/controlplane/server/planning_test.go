package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/planning"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// --- fixtures --------------------------------------------------------------

func fittingSpec() *purserv1.ModelSpec {
	return &purserv1.ModelSpec{
		ModelId:       "llama-8b",
		Layers:        32,
		ParamsTotalB:  8,
		NKvHeads:      8,
		HeadDim:       128,
		AttentionType: purserv1.AttentionType_ATTENTION_TYPE_GQA,
		ContextMax:    8192,
		Quantizations: []*purserv1.Quantization{{Name: "q4_k_m", SizeGb: 10, Quality: 0.8}},
	}
}

func oversizedSpec() *purserv1.ModelSpec {
	s := fittingSpec()
	s.ModelId = "llama-huge"
	s.Quantizations = []*purserv1.Quantization{{Name: "q8", SizeGb: 500, Quality: 0.95}}
	return s
}

func seedModel(t *testing.T, reg registry.Registry, spec *purserv1.ModelSpec) {
	t.Helper()
	blob, err := protojson.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if err := reg.CreateModel(context.Background(), &registry.Model{ID: spec.GetModelId(), Spec: blob}); err != nil {
		t.Fatalf("create model: %v", err)
	}
}

func seedReadyNode(t *testing.T, reg registry.Registry, id string, ramGB float64) {
	t.Helper()
	hw := &purserv1.HardwareProfile{
		NodeId:          id,
		RamTotalGb:      ramGB,
		RamAvailableGb:  ramGB,
		MemBandwidthGbs: 200,
		Gpus:            []*purserv1.GpuInfo{{Name: "gpu0", VramGb: ramGB + 16, Count: 1}},
	}
	blob, err := protojson.Marshal(hw)
	if err != nil {
		t.Fatalf("marshal hw: %v", err)
	}
	if err := reg.CreateNode(context.Background(), &registry.Node{
		ID: id, State: "NODE_STATE_READY", RAMGB: ramGB, VRAMGB: ramGB + 16, HardwareProfile: blob,
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

// --- deploy (planner path) -------------------------------------------------

func TestHandleDeployModel_PlannerPath(t *testing.T) {
	reg := newReg(t)
	seedModel(t, reg, fittingSpec())
	seedReadyNode(t, reg, "node-a", 64)

	fd := &fakeDeployer{id: "dep-xyz"}
	srv := server.New(reg, server.Config{Deployer: fd, Planner: planning.New(reg)})

	rec := httptest.NewRecorder()
	// No body: the server must produce a plan from the fleet.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/llama-8b/deploy", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		DeploymentID string `json:"deployment_id"`
		ModelID      string `json:"model_id"`
		PlanID       string `json:"plan_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DeploymentID != "dep-xyz" || resp.ModelID != "llama-8b" || resp.PlanID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// The orchestrator must have been invoked with the produced plan.
	if fd.applied == nil {
		t.Fatal("orchestrator.Apply was not invoked")
	}
	if fd.applied.GetModelId() != "llama-8b" || len(fd.applied.GetAssignments()) != 1 {
		t.Errorf("Apply got wrong plan: %+v", fd.applied)
	}

	// The plan must be persisted, with an explanation retrievable via GET.
	plans, err := reg.ListPlans(context.Background())
	if err != nil || len(plans) != 1 {
		t.Fatalf("expected 1 persisted plan, got %d (err=%v)", len(plans), err)
	}
	if plans[0].ID != resp.PlanID {
		t.Errorf("persisted plan id = %q, want %q", plans[0].ID, resp.PlanID)
	}
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/plans/"+resp.PlanID, nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "explanation") {
		t.Fatalf("GET plan must return the explanation: status=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleDeployModel_PlannerPath_DoesNotFit(t *testing.T) {
	reg := newReg(t)
	seedModel(t, reg, oversizedSpec())
	seedReadyNode(t, reg, "node-a", 64)

	fd := &fakeDeployer{id: "dep-xyz"}
	srv := server.New(reg, server.Config{Deployer: fd, Planner: planning.New(reg)})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/models/llama-huge/deploy", nil))

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status = %d, want a 4xx; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error     string  `json:"error"`
		DeficitGB float64 `json:"deficit_gb"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "model_does_not_fit" {
		t.Errorf("error = %q, want model_does_not_fit", resp.Error)
	}
	if resp.DeficitGB <= 0 {
		t.Errorf("expected positive deficit_gb, got %.1f", resp.DeficitGB)
	}
	// The orchestrator must NOT have been invoked, and nothing persisted.
	if fd.applied != nil {
		t.Error("orchestrator.Apply must not be invoked when the model does not fit")
	}
	if plans, _ := reg.ListPlans(context.Background()); len(plans) != 0 {
		t.Errorf("no plan should be persisted on a fit failure, got %d", len(plans))
	}
}

func TestHandleDeployModel_PlannerPath_ModelNotFound(t *testing.T) {
	reg := newReg(t)
	seedReadyNode(t, reg, "node-a", 64)
	srv := server.New(reg, server.Config{Deployer: &fakeDeployer{}, Planner: planning.New(reg)})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/models/nope/deploy", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// --- /models fit verdicts --------------------------------------------------

func TestHandleListModels_FitVerdicts(t *testing.T) {
	reg := newReg(t)
	seedModel(t, reg, fittingSpec())
	seedModel(t, reg, oversizedSpec())
	seedReadyNode(t, reg, "node-a", 64)

	srv := server.New(reg, server.Config{Planner: planning.New(reg)})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Models []struct {
			ID  string       `json:"id"`
			Fit planning.Fit `json:"fit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if len(body.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(body.Models))
	}
	fit := map[string]planning.Fit{}
	for _, m := range body.Models {
		fit[m.ID] = m.Fit
	}
	if !fit["llama-8b"].Deployable {
		t.Errorf("llama-8b: expected deployable, got %+v", fit["llama-8b"])
	}
	if fit["llama-8b"].NodeCount != 1 {
		t.Errorf("llama-8b: node_count = %d, want 1", fit["llama-8b"].NodeCount)
	}
	if fit["llama-huge"].Deployable {
		t.Errorf("llama-huge: expected NOT deployable")
	}
	if fit["llama-huge"].DeficitGB <= 0 {
		t.Errorf("llama-huge: expected positive deficit, got %.1f", fit["llama-huge"].DeficitGB)
	}
}
