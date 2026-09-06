package plan

import (
	"strings"

	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// This file bridges the rich domain types (types.go) to and from the generated
// purser.v1 protobuf messages. Conversions are provided in the direction the
// planner actually needs them:
//
//   - Fleet inputs (Node, Link) are built FROM the wire messages the agents
//     report (HardwareProfile, LinkMetric): those are inbound-only, since the
//     planner consumes fleet state rather than producing it.
//   - Plan outputs (DeploymentPlan and its parts, ModelSpec) convert both ways,
//     because the planner both consumes a ModelSpec and emits a DeploymentPlan.
//
// Fields present on the wire but absent from the domain model (e.g.
// ModelSpec.Family/Architecture/Engine, HardwareProfile.Hostname/Os/Arch) are
// intentionally dropped. Fields present in the domain but absent on the wire
// (e.g. DeploymentPlan.FailoverAlt) are not serialised.

// ---- enums ---------------------------------------------------------------

// ToProto converts a domain AttentionType to the wire enum.
func (a AttentionType) ToProto() purserv1.AttentionType {
	switch a {
	case AttentionMHA:
		return purserv1.AttentionType_ATTENTION_TYPE_MHA
	case AttentionGQA:
		return purserv1.AttentionType_ATTENTION_TYPE_GQA
	case AttentionMLA:
		return purserv1.AttentionType_ATTENTION_TYPE_MLA
	case AttentionLinear:
		return purserv1.AttentionType_ATTENTION_TYPE_LINEAR
	default:
		return purserv1.AttentionType_ATTENTION_TYPE_UNSPECIFIED
	}
}

// AttentionTypeFromProto converts a wire AttentionType to the domain enum.
func AttentionTypeFromProto(p purserv1.AttentionType) AttentionType {
	switch p {
	case purserv1.AttentionType_ATTENTION_TYPE_MHA:
		return AttentionMHA
	case purserv1.AttentionType_ATTENTION_TYPE_GQA:
		return AttentionGQA
	case purserv1.AttentionType_ATTENTION_TYPE_MLA:
		return AttentionMLA
	case purserv1.AttentionType_ATTENTION_TYPE_LINEAR:
		return AttentionLinear
	default:
		return AttentionUnspecified
	}
}

// ToProto converts a domain Role to the wire enum.
func (r Role) ToProto() purserv1.Role {
	switch r {
	case RoleHost:
		return purserv1.Role_ROLE_HOST
	case RoleWorker:
		return purserv1.Role_ROLE_WORKER
	default:
		return purserv1.Role_ROLE_UNSPECIFIED
	}
}

// RoleFromProto converts a wire Role to the domain enum.
func RoleFromProto(p purserv1.Role) Role {
	switch p {
	case purserv1.Role_ROLE_HOST:
		return RoleHost
	case purserv1.Role_ROLE_WORKER:
		return RoleWorker
	default:
		return RoleUnspecified
	}
}

// ---- Quantization --------------------------------------------------------

// ToProto converts a domain Quantization to the wire message.
func (q Quantization) ToProto() *purserv1.Quantization {
	return &purserv1.Quantization{
		Name:        q.Name,
		SizeGb:      q.SizeGB,
		RequiresFp4: q.RequiresFP4,
		Quality:     q.Quality,
		EmulatedFp4: q.EmulatedFP4,
	}
}

// QuantizationFromProto converts a wire Quantization to the domain type.
func QuantizationFromProto(p *purserv1.Quantization) Quantization {
	if p == nil {
		return Quantization{}
	}
	return Quantization{
		Name:        p.GetName(),
		SizeGB:      p.GetSizeGb(),
		RequiresFP4: p.GetRequiresFp4(),
		Quality:     p.GetQuality(),
		EmulatedFP4: p.GetEmulatedFp4(),
	}
}

// ---- DraftInfo -----------------------------------------------------------

// ToProto converts a domain DraftInfo to the wire message.
func (d DraftInfo) ToProto() *purserv1.DraftInfo {
	return &purserv1.DraftInfo{
		Available:  d.Available,
		Type:       d.Type,
		TailLayers: u32(d.TailLayers),
	}
}

// DraftInfoFromProto converts a wire DraftInfo to the domain type.
func DraftInfoFromProto(p *purserv1.DraftInfo) DraftInfo {
	if p == nil {
		return DraftInfo{}
	}
	return DraftInfo{
		Available:  p.GetAvailable(),
		Type:       p.GetType(),
		TailLayers: int(p.GetTailLayers()),
	}
}

// ---- ModelSpec -----------------------------------------------------------

// ToProto converts a domain ModelSpec to the wire message. Family, Architecture
// and Engine have no domain counterpart and are left empty.
func (m ModelSpec) ToProto() *purserv1.ModelSpec {
	qs := make([]*purserv1.Quantization, 0, len(m.Quantizations))
	for _, q := range m.Quantizations {
		qs = append(qs, q.ToProto())
	}
	return &purserv1.ModelSpec{
		ModelId:       m.ID,
		ParamsTotalB:  m.ParamsTotalB,
		ParamsActiveB: m.ParamsActiveB,
		Layers:        u32(m.Layers),
		HiddenSize:    u32(m.HiddenSize),
		NKvHeads:      u32(m.NKVHeads),
		HeadDim:       u32(m.HeadDim),
		AttentionType: m.AttentionType.ToProto(),
		ContextMax:    u64(m.ContextMax),
		IsMoe:         m.IsMoE,
		Draft:         m.Draft.ToProto(),
		Quantizations: qs,
	}
}

// ModelSpecFromProto converts a wire ModelSpec to the domain type.
func ModelSpecFromProto(p *purserv1.ModelSpec) ModelSpec {
	if p == nil {
		return ModelSpec{}
	}
	qs := make([]Quantization, 0, len(p.GetQuantizations()))
	for _, q := range p.GetQuantizations() {
		qs = append(qs, QuantizationFromProto(q))
	}
	return ModelSpec{
		ID:            p.GetModelId(),
		IsMoE:         p.GetIsMoe(),
		Layers:        int(p.GetLayers()),
		ParamsTotalB:  p.GetParamsTotalB(),
		ParamsActiveB: p.GetParamsActiveB(),
		HiddenSize:    int(p.GetHiddenSize()),
		NKVHeads:      int(p.GetNKvHeads()),
		HeadDim:       int(p.GetHeadDim()),
		AttentionType: AttentionTypeFromProto(p.GetAttentionType()),
		ContextMax:    int(p.GetContextMax()),
		Quantizations: qs,
		Draft:         DraftInfoFromProto(p.GetDraft()),
	}
}

// ---- Assignment ----------------------------------------------------------

// ToProto converts a domain Assignment to the wire message.
func (a Assignment) ToProto() *purserv1.Assignment {
	return &purserv1.Assignment{
		NodeId:     a.NodeID,
		Role:       a.Role.ToProto(),
		LayerStart: u32(a.LayerStart),
		LayerEnd:   u32(a.LayerEnd),
		Draft:      a.Draft,
	}
}

// AssignmentFromProto converts a wire Assignment to the domain type.
func AssignmentFromProto(p *purserv1.Assignment) Assignment {
	if p == nil {
		return Assignment{}
	}
	return Assignment{
		NodeID:     p.GetNodeId(),
		Role:       RoleFromProto(p.GetRole()),
		LayerStart: int(p.GetLayerStart()),
		LayerEnd:   int(p.GetLayerEnd()),
		Draft:      p.GetDraft(),
	}
}

// ---- PerfEstimate --------------------------------------------------------

// ToProto converts a domain PerfEstimate to the wire message.
func (p PerfEstimate) ToProto() *purserv1.PerfEstimate {
	return &purserv1.PerfEstimate{
		DecodeTokSMin:  p.DecodeTokSMin,
		DecodeTokSMax:  p.DecodeTokSMax,
		PrefillTokSMin: p.PrefillTokSMin,
		PrefillTokSMax: p.PrefillTokSMax,
		HeadroomGb:     p.HeadroomGB,
	}
}

// PerfEstimateFromProto converts a wire PerfEstimate to the domain type.
func PerfEstimateFromProto(p *purserv1.PerfEstimate) PerfEstimate {
	if p == nil {
		return PerfEstimate{}
	}
	return PerfEstimate{
		DecodeTokSMin:  p.GetDecodeTokSMin(),
		DecodeTokSMax:  p.GetDecodeTokSMax(),
		PrefillTokSMin: p.GetPrefillTokSMin(),
		PrefillTokSMax: p.GetPrefillTokSMax(),
		HeadroomGB:     p.GetHeadroomGb(),
	}
}

// ---- DeploymentPlan ------------------------------------------------------

// ToProto converts a domain DeploymentPlan to the wire message. FailoverAlt has
// no wire counterpart and is not serialised.
func (d *DeploymentPlan) ToProto() *purserv1.DeploymentPlan {
	if d == nil {
		return nil
	}
	as := make([]*purserv1.Assignment, 0, len(d.Assignments))
	for _, a := range d.Assignments {
		as = append(as, a.ToProto())
	}
	return &purserv1.DeploymentPlan{
		PlanId:        d.PlanID,
		ModelId:       d.ModelID,
		Quantization:  d.Quantization,
		Assignments:   as,
		PipelineOrder: append([]string(nil), d.PipelineOrder...),
		Estimated:     d.Estimated.ToProto(),
		Cost:          d.Cost,
		Explanation:   append([]string(nil), d.Explanation...),
	}
}

// DeploymentPlanFromProto converts a wire DeploymentPlan to the domain type.
// FailoverAlt is initialised empty (not carried on the wire).
func DeploymentPlanFromProto(p *purserv1.DeploymentPlan) *DeploymentPlan {
	if p == nil {
		return nil
	}
	as := make([]Assignment, 0, len(p.GetAssignments()))
	for _, a := range p.GetAssignments() {
		as = append(as, AssignmentFromProto(a))
	}
	return &DeploymentPlan{
		PlanID:        p.GetPlanId(),
		ModelID:       p.GetModelId(),
		Quantization:  p.GetQuantization(),
		Assignments:   as,
		PipelineOrder: append([]string(nil), p.GetPipelineOrder()...),
		Estimated:     PerfEstimateFromProto(p.GetEstimated()),
		Cost:          p.GetCost(),
		Explanation:   append([]string(nil), p.GetExplanation()...),
		FailoverAlt:   map[string]*DeploymentPlan{},
	}
}

// ---- Node (inbound only) -------------------------------------------------

// NodeFromHardwareProfile builds a planner Node from the hardware profile an
// agent reports. VRAM is summed across GPUs (VramGb × Count); a node is treated
// as unified-memory and FP4-native if any of its GPUs report so.
func NodeFromHardwareProfile(p *purserv1.HardwareProfile) Node {
	if p == nil {
		return Node{}
	}
	var vram float64
	unified := false
	fp4 := false
	for _, g := range p.GetGpus() {
		count := g.GetCount()
		if count == 0 {
			count = 1
		}
		vram += g.GetVramGb() * float64(count)
		if g.GetUnified() {
			unified = true
		}
		if g.GetFp4Native() {
			fp4 = true
		}
	}

	backends := make([]string, 0, len(p.GetBackends()))
	for _, b := range p.GetBackends() {
		backends = append(backends, backendName(b))
	}

	var engineVersions map[string]string
	if ev := p.GetEngineVersions(); len(ev) > 0 {
		engineVersions = make(map[string]string, len(ev))
		for k, v := range ev {
			engineVersions[k] = v
		}
	}

	return Node{
		ID:              p.GetNodeId(),
		RAMTotalGB:      p.GetRamTotalGb(),
		RAMAvailableGB:  p.GetRamAvailableGb(),
		VRAMGB:          vram,
		UnifiedMemory:   unified,
		MemBandwidthGBs: p.GetMemBandwidthGbs(),
		Backends:        backends,
		FP4Native:       fp4,
		EngineVersions:  engineVersions,
		// DiskFreeGB is populated from the proto's disk_free_gb field; used by
		// estimatePerformance when KVSSDOffload is true.
		DiskFreeGB: p.GetDiskFreeGb(),
		// Engine-capability fields wired from the proto (I4, I6).
		PrefixCachingFactor: float64(p.GetPrefixCachingFactor()),
		KVSSDOffload:        p.GetKvSsdOffload(),
	}
}

// backendName renders a wire Backend enum as the short lowercase string used in
// Node.Backends ("cuda", "metal", "rocm", "cpu").
func backendName(b purserv1.Backend) string {
	name := purserv1.Backend_name[int32(b)]
	name = strings.TrimPrefix(name, "BACKEND_")
	return strings.ToLower(name)
}

// ---- Link ----------------------------------------------------------------

// LinkFromMetric builds a planner Link from a measured LinkMetric.
func LinkFromMetric(p *purserv1.LinkMetric) Link {
	if p == nil {
		return Link{}
	}
	return Link{
		From:         p.GetFromNode(),
		To:           p.GetToNode(),
		RTTms:        p.GetRttMs(),
		BandwidthGBs: p.GetBandwidthGbs(),
	}
}

// ToMetric converts a planner Link back to a LinkMetric (MeasuredAt is left nil).
func (l Link) ToMetric() *purserv1.LinkMetric {
	return &purserv1.LinkMetric{
		FromNode:     l.From,
		ToNode:       l.To,
		BandwidthGbs: l.BandwidthGBs,
		RttMs:        l.RTTms,
	}
}

// ---- small numeric helpers ----------------------------------------------

func u32(i int) uint32 {
	if i < 0 {
		return 0
	}
	return uint32(i)
}

func u64(i int) uint64 {
	if i < 0 {
		return 0
	}
	return uint64(i)
}
