package planning_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/planning"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	plannerplan "github.com/purser/purser/go/planner/plan"
	"google.golang.org/protobuf/encoding/protojson"
)

// --- fixtures --------------------------------------------------------------

func openReg(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

// fittingSpec is a small model that comfortably fits a single 64 GB node.
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

// oversizedSpec cannot fit any realistic single-machine fleet.
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
		t.Fatalf("marshal model spec: %v", err)
	}
	if err := reg.CreateModel(context.Background(), &registry.Model{
		ID:   spec.GetModelId(),
		Spec: blob,
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}
}

func seedNode(t *testing.T, reg registry.Registry, id string, ramGB float64, state string) {
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
		ID:              id,
		State:           state,
		RAMGB:           ramGB,
		VRAMGB:          ramGB + 16,
		HardwareProfile: blob,
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

// --- tests -----------------------------------------------------------------

func TestFit_Deployable(t *testing.T) {
	reg := openReg(t)
	seedModel(t, reg, fittingSpec())
	seedNode(t, reg, "node-a", 64, "NODE_STATE_READY")

	f, err := planning.New(reg).Fit(context.Background(), "llama-8b")
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if !f.Deployable {
		t.Fatalf("expected deployable, got reason=%q deficit=%.1f", f.Reason, f.DeficitGB)
	}
	if f.NodeCount != 1 {
		t.Errorf("node_count = %d, want 1", f.NodeCount)
	}
	if f.Quantization != "q4_k_m" {
		t.Errorf("quantization = %q, want q4_k_m", f.Quantization)
	}
	if f.Estimated == nil || f.Estimated.DecodeMaxTokS <= 0 {
		t.Errorf("expected a non-zero decode estimate, got %+v", f.Estimated)
	}
}

func TestFit_NotDeployable(t *testing.T) {
	reg := openReg(t)
	seedModel(t, reg, oversizedSpec())
	seedNode(t, reg, "node-a", 64, "NODE_STATE_READY")

	f, err := planning.New(reg).Fit(context.Background(), "llama-huge")
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f.Deployable {
		t.Fatal("expected NOT deployable")
	}
	if f.DeficitGB <= 0 {
		t.Errorf("expected positive deficit, got %.1f", f.DeficitGB)
	}
	if f.Reason == "" {
		t.Error("expected a non-empty reason")
	}
}

func TestFit_IgnoresNonReadyNodes(t *testing.T) {
	reg := openReg(t)
	seedModel(t, reg, fittingSpec())
	// A capable node, but only ENROLLED (not yet approved) → not a candidate.
	seedNode(t, reg, "node-a", 64, "NODE_STATE_ENROLLED")

	f, err := planning.New(reg).Fit(context.Background(), "llama-8b")
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f.Deployable {
		t.Fatal("model must not be deployable when there are no READY nodes")
	}
}

func TestFitAll(t *testing.T) {
	reg := openReg(t)
	seedModel(t, reg, fittingSpec())
	seedModel(t, reg, oversizedSpec())
	seedNode(t, reg, "node-a", 64, "NODE_STATE_READY")

	fits, err := planning.New(reg).FitAll(context.Background())
	if err != nil {
		t.Fatalf("FitAll: %v", err)
	}
	if len(fits) != 2 {
		t.Fatalf("expected 2 fits, got %d", len(fits))
	}
	byID := map[string]planning.Fit{}
	for _, f := range fits {
		byID[f.ModelID] = f
	}
	if !byID["llama-8b"].Deployable {
		t.Errorf("llama-8b should be deployable")
	}
	if byID["llama-huge"].Deployable {
		t.Errorf("llama-huge should not be deployable")
	}
}

func TestPlan_ProducesProtoPlan(t *testing.T) {
	reg := openReg(t)
	seedModel(t, reg, fittingSpec())
	seedNode(t, reg, "node-a", 64, "NODE_STATE_READY")

	dp, err := planning.New(reg).Plan(context.Background(), "llama-8b", plannerplan.Constraints{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if dp.GetModelId() != "llama-8b" {
		t.Errorf("model_id = %q", dp.GetModelId())
	}
	if len(dp.GetAssignments()) != 1 {
		t.Fatalf("assignments = %d, want 1 (single-node)", len(dp.GetAssignments()))
	}
	if dp.GetAssignments()[0].GetRole() != purserv1.Role_ROLE_HOST {
		t.Errorf("single-node assignment role = %v, want HOST", dp.GetAssignments()[0].GetRole())
	}
	if len(dp.GetExplanation()) == 0 {
		t.Error("expected a non-empty explanation for the UI")
	}
}

func TestPlan_FitError(t *testing.T) {
	reg := openReg(t)
	seedModel(t, reg, oversizedSpec())
	seedNode(t, reg, "node-a", 64, "NODE_STATE_READY")

	dp, err := planning.New(reg).Plan(context.Background(), "llama-huge", plannerplan.Constraints{})
	if dp != nil {
		t.Fatalf("expected nil plan, got %+v", dp)
	}
	var fe *planning.FitError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *planning.FitError, got %T: %v", err, err)
	}
	if fe.DeficitGB <= 0 {
		t.Errorf("expected positive deficit, got %.1f", fe.DeficitGB)
	}
}

// midSpec fits across two 32 GB nodes but not on one.
func midSpec() *purserv1.ModelSpec {
	s := fittingSpec()
	s.ModelId = "llama-mid"
	s.Quantizations = []*purserv1.Quantization{{Name: "q8", SizeGb: 40, Quality: 0.9}}
	return s
}

func TestPlan_MultiNodeUsesLinks(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()
	seedModel(t, reg, midSpec())
	seedNode(t, reg, "node-a", 32, "NODE_STATE_READY")
	seedNode(t, reg, "node-b", 32, "NODE_STATE_READY")
	// A measured link both ways so the pipeline-order search has edge costs.
	if err := reg.UpsertLink(ctx, &registry.Link{FromNode: "node-a", ToNode: "node-b", BandwidthGBs: 25, RTTMs: 0.2}); err != nil {
		t.Fatalf("upsert link: %v", err)
	}
	if err := reg.UpsertLink(ctx, &registry.Link{FromNode: "node-b", ToNode: "node-a", BandwidthGBs: 25, RTTMs: 0.2}); err != nil {
		t.Fatalf("upsert link: %v", err)
	}

	dp, err := planning.New(reg).Plan(ctx, "llama-mid", plannerplan.Constraints{})
	if err != nil {
		t.Fatalf("Plan (should fit across two nodes): %v", err)
	}
	if len(dp.GetPipelineOrder()) != 2 {
		t.Fatalf("pipeline order = %v, want 2 nodes", dp.GetPipelineOrder())
	}
	if len(dp.GetAssignments()) != 2 {
		t.Errorf("assignments = %d, want 2", len(dp.GetAssignments()))
	}
}

func TestPlan_ModelNotFound(t *testing.T) {
	reg := openReg(t)
	seedNode(t, reg, "node-a", 64, "NODE_STATE_READY")

	_, err := planning.New(reg).Plan(context.Background(), "nope", plannerplan.Constraints{})
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("expected registry.ErrNotFound, got %v", err)
	}
}
