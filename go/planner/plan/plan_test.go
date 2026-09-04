package plan

import (
	"errors"
	"testing"

	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// smallModel is a tiny dense model with two quantizations.
func smallModel() ModelSpec {
	return ModelSpec{
		ID:            "acme/small-7b",
		Layers:        4,
		ParamsTotalB:  7,
		ParamsActiveB: 7,
		HiddenSize:    4096,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q8", SizeGB: 14, Quality: 0.99},
			{Name: "q4", SizeGB: 4, Quality: 0.90},
		},
	}
}

// (a) Small model fits on one node → correct single-node plan.
func TestPlan_SingleNode(t *testing.T) {
	nodes := []Node{
		{ID: "node-a", RAMTotalGB: 64, RAMAvailableGB: 48, UnifiedMemory: true, MemBandwidthGBs: 400},
	}
	model := smallModel()

	dp, err := Plan(nodes, nil, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if dp == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(dp.Assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(dp.Assignments))
	}
	a := dp.Assignments[0]
	if a.NodeID != "node-a" {
		t.Errorf("assignment node = %q, want node-a", a.NodeID)
	}
	if a.Role != RoleHost {
		t.Errorf("role = %v, want HOST", a.Role)
	}
	if a.LayerStart != 0 || a.LayerEnd != model.Layers-1 {
		t.Errorf("layers = [%d,%d], want [0,%d]", a.LayerStart, a.LayerEnd, model.Layers-1)
	}
	if len(dp.PipelineOrder) != 1 || dp.PipelineOrder[0] != "node-a" {
		t.Errorf("pipeline order = %v, want [node-a]", dp.PipelineOrder)
	}
	// Highest-quality quant that fits should be chosen.
	if dp.Quantization != "q8" {
		t.Errorf("quant = %q, want q8", dp.Quantization)
	}
	if dp.Estimated.HeadroomGB <= 0 {
		t.Errorf("expected positive headroom, got %.2f", dp.Estimated.HeadroomGB)
	}
	if dp.Estimated.DecodeTokSMin <= 0 || dp.Estimated.DecodeTokSMax < dp.Estimated.DecodeTokSMin {
		t.Errorf("bad decode range: [%.2f, %.2f]", dp.Estimated.DecodeTokSMin, dp.Estimated.DecodeTokSMax)
	}
}

// (b) Model too large for the whole fleet → PlanError with positive deficit.
func TestPlan_TooLarge(t *testing.T) {
	nodes := []Node{
		{ID: "n1", RAMAvailableGB: 8, UnifiedMemory: true, MemBandwidthGBs: 50},
		{ID: "n2", RAMAvailableGB: 8, UnifiedMemory: true, MemBandwidthGBs: 50},
	}
	model := ModelSpec{
		ID:            "acme/huge-405b",
		Layers:        126,
		ParamsTotalB:  405,
		ParamsActiveB: 405,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    8192,
		Quantizations: []Quantization{
			{Name: "q4", SizeGB: 220, Quality: 0.90},
		},
	}

	dp, err := Plan(nodes, nil, model, Constraints{})
	if dp != nil {
		t.Fatalf("expected no plan, got %+v", dp)
	}
	var pe *PlanError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PlanError, got %T: %v", err, err)
	}
	if pe.DeficitGB <= 0 {
		t.Errorf("expected positive deficit, got %.2f", pe.DeficitGB)
	}
	if len(pe.Suggestions) == 0 {
		t.Error("expected suggestions on the error")
	}
}

// (c) Quantization selection picks the highest quality that fits.
func TestPlan_QuantizationSelection(t *testing.T) {
	// A single 40 GB node: q8 (60 GB) does not fit, q4 (20 GB) does.
	nodes := []Node{
		{ID: "solo", RAMTotalGB: 48, RAMAvailableGB: 40, UnifiedMemory: true, MemBandwidthGBs: 200},
	}
	model := ModelSpec{
		ID:            "acme/mid-30b",
		IsMoE:         true,
		Layers:        48,
		ParamsTotalB:  30,
		ParamsActiveB: 3,
		NKVHeads:      8,
		HeadDim:       128,
		AttentionType: AttentionGQA,
		ContextMax:    4096,
		Quantizations: []Quantization{
			{Name: "q8", SizeGB: 60, Quality: 0.99},
			{Name: "q4", SizeGB: 20, Quality: 0.95},
			{Name: "q2", SizeGB: 10, Quality: 0.80},
		},
	}

	dp, err := Plan(nodes, nil, model, Constraints{})
	if err != nil {
		t.Fatalf("expected a plan, got error: %v", err)
	}
	if dp.Quantization != "q4" {
		t.Fatalf("quant = %q, want q4 (highest quality that fits)", dp.Quantization)
	}
}

// force_quant overrides phase A when the forced quant fits.
func TestPlan_ForceQuant(t *testing.T) {
	nodes := []Node{{ID: "solo", RAMAvailableGB: 40, UnifiedMemory: true, MemBandwidthGBs: 200}}
	model := smallModel()
	forced := "q4"

	dp, err := Plan(nodes, nil, model, Constraints{ForceQuant: &forced})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dp.Quantization != "q4" {
		t.Errorf("quant = %q, want forced q4", dp.Quantization)
	}
}

// include/exclude filtering removes nodes before planning.
func TestPlan_ExcludeAllNodes(t *testing.T) {
	nodes := []Node{{ID: "only", RAMAvailableGB: 48, UnifiedMemory: true, MemBandwidthGBs: 400}}
	_, err := Plan(nodes, nil, smallModel(), Constraints{ExcludeNodes: []string{"only"}})
	var pe *PlanError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PlanError, got %T: %v", err, err)
	}
}

func TestEstimateKVCache_AttentionFactor(t *testing.T) {
	base := ModelSpec{Layers: 32, NKVHeads: 8, HeadDim: 128}
	ctx := 32768

	base.AttentionType = AttentionGQA
	gqa := estimateKVCache(base, ctx)
	base.AttentionType = AttentionMLA
	mla := estimateKVCache(base, ctx)
	base.AttentionType = AttentionLinear
	lin := estimateKVCache(base, ctx)

	if gqa <= 0 {
		t.Fatalf("GQA kv should be positive, got %.4f", gqa)
	}
	if !(mla < gqa) {
		t.Errorf("MLA (%.4f) should compress below GQA (%.4f)", mla, gqa)
	}
	if !(lin < mla) {
		t.Errorf("LINEAR (%.4f) should be below MLA (%.4f)", lin, mla)
	}
	// Zero/invalid context yields zero.
	if got := estimateKVCache(base, 0); got != 0 {
		t.Errorf("kv at context 0 = %.4f, want 0", got)
	}
}

// Round-trip a plan through the proto layer and back.
func TestDeploymentPlan_ProtoRoundTrip(t *testing.T) {
	nodes := []Node{{ID: "node-a", RAMAvailableGB: 48, UnifiedMemory: true, MemBandwidthGBs: 400}}
	model := smallModel()
	model.Draft = DraftInfo{Available: true, Type: "mtp", TailLayers: 2}

	dp, err := Plan(nodes, nil, model, Constraints{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pb := dp.ToProto()
	if pb.GetPlanId() != dp.PlanID || pb.GetModelId() != dp.ModelID {
		t.Fatalf("proto ids mismatch: %q/%q vs %q/%q", pb.GetPlanId(), pb.GetModelId(), dp.PlanID, dp.ModelID)
	}
	if len(pb.GetAssignments()) != 1 || pb.GetAssignments()[0].GetRole() != purserv1.Role_ROLE_HOST {
		t.Fatalf("proto assignment role wrong: %+v", pb.GetAssignments())
	}

	back := DeploymentPlanFromProto(pb)
	if back.PlanID != dp.PlanID || back.Quantization != dp.Quantization {
		t.Errorf("round-trip mismatch: %+v vs %+v", back, dp)
	}
	if len(back.Assignments) != 1 || back.Assignments[0].NodeID != "node-a" {
		t.Errorf("round-trip assignment wrong: %+v", back.Assignments)
	}
	if !back.Assignments[0].Draft {
		t.Error("expected draft flag preserved on the single-node assignment")
	}
}

func TestModelSpec_ProtoRoundTrip(t *testing.T) {
	m := smallModel()
	m.AttentionType = AttentionMLA
	m.IsMoE = true
	m.Draft = DraftInfo{Available: true, Type: "eagle", TailLayers: 3}

	got := ModelSpecFromProto(m.ToProto())
	if got.ID != m.ID || got.Layers != m.Layers || got.AttentionType != m.AttentionType {
		t.Errorf("scalar round-trip mismatch: %+v vs %+v", got, m)
	}
	if got.IsMoE != m.IsMoE || got.Draft != m.Draft {
		t.Errorf("flag/draft round-trip mismatch: %+v vs %+v", got, m)
	}
	if len(got.Quantizations) != len(m.Quantizations) {
		t.Errorf("quant count mismatch: %d vs %d", len(got.Quantizations), len(m.Quantizations))
	}
}

func TestNodeFromHardwareProfile(t *testing.T) {
	hp := &purserv1.HardwareProfile{
		NodeId:          "gpu-box",
		RamTotalGb:      128,
		RamAvailableGb:  100,
		MemBandwidthGbs: 900,
		Backends:        []purserv1.Backend{purserv1.Backend_BACKEND_CUDA, purserv1.Backend_BACKEND_CPU},
		Gpus: []*purserv1.GpuInfo{
			{Name: "gpu0", VramGb: 24, Count: 2, Fp4Native: true},
		},
	}
	n := NodeFromHardwareProfile(hp)
	if n.ID != "gpu-box" || n.RAMAvailableGB != 100 {
		t.Errorf("basic fields wrong: %+v", n)
	}
	if n.VRAMGB != 48 { // 24 * 2
		t.Errorf("vram = %.1f, want 48", n.VRAMGB)
	}
	if !n.FP4Native {
		t.Error("expected FP4Native true")
	}
	want := []string{"cuda", "cpu"}
	if len(n.Backends) != len(want) {
		t.Fatalf("backends = %v, want %v", n.Backends, want)
	}
	for i := range want {
		if n.Backends[i] != want[i] {
			t.Errorf("backend[%d] = %q, want %q", i, n.Backends[i], want[i])
		}
	}
}
