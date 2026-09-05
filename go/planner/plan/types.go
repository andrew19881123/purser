// Package plan is the brain of Purser: given the state of a heterogeneous
// fleet (nodes + network links) and a model to serve, it produces an optimal
// deployment plan (which layers run on which node, in which order, at which
// quantization).
//
// This file defines the *domain* types described in design document 08 §2.
// They are rich Go types (not the raw protobuf messages): the planner reasons
// over these, and convert.go bridges them to/from the generated
// github.com/purser/purser/go/gen/purser/v1 messages where a matching wire
// type exists.
//
// The full throughput-aware dynamic-programming algorithm (phases B–F of the
// design) is intentionally NOT implemented here — see plan.go, which ships a
// minimal single-node skeleton with clear hooks for the later phase.
//
// The package lives under go/planner/plan rather than go/planner/planner
// because the module root reserves the "planner" path segment for the compiled
// binary that `go build` drops there (see the repo .gitignore).
package plan

import "fmt"

// NodeID identifies a fleet node. It is a distinct string type so it can be
// used unambiguously as a map value (e.g. Constraints.Pinned).
type NodeID string

// AttentionType selects the KV-cache compression factor of a model
// (design 08 §12). The numeric values line up with purser.v1.AttentionType to
// keep conversion trivial.
type AttentionType int

const (
	// AttentionUnspecified is the zero value / unknown attention type.
	AttentionUnspecified AttentionType = iota
	// AttentionMHA is classic multi-head attention (KV factor 1.0).
	AttentionMHA
	// AttentionGQA is grouped-query attention (n_kv_heads already reduced).
	AttentionGQA
	// AttentionMLA is multi-head latent attention (~10x KV compression).
	AttentionMLA
	// AttentionLinear is linear attention (near-constant KV state).
	AttentionLinear
)

// String implements fmt.Stringer.
func (a AttentionType) String() string {
	switch a {
	case AttentionMHA:
		return "MHA"
	case AttentionGQA:
		return "GQA"
	case AttentionMLA:
		return "MLA"
	case AttentionLinear:
		return "LINEAR"
	default:
		return "UNSPECIFIED"
	}
}

// Mode is the operator override mode (design 08 §14).
type Mode int

const (
	// ModeAuto applies the computed plan immediately (default / zero value).
	ModeAuto Mode = iota
	// ModePropose computes a plan but waits for operator approval.
	ModePropose
	// ModeManual only honours the explicit operator constraints.
	ModeManual
)

// String implements fmt.Stringer.
func (m Mode) String() string {
	switch m {
	case ModePropose:
		return "PROPOSE"
	case ModeManual:
		return "MANUAL"
	default:
		return "AUTO"
	}
}

// Role is the role of a node inside a pipeline-parallel deployment. The numeric
// values line up with purser.v1.Role.
type Role int

const (
	// RoleUnspecified is the zero value / unknown role.
	RoleUnspecified Role = iota
	// RoleHost is the pipeline head: it faces clients and owns the first shard.
	RoleHost
	// RoleWorker is any non-head stage of the pipeline.
	RoleWorker
)

// String implements fmt.Stringer.
func (r Role) String() string {
	switch r {
	case RoleHost:
		return "HOST"
	case RoleWorker:
		return "WORKER"
	default:
		return "UNSPECIFIED"
	}
}

// LayerRange is an inclusive [Start, End] range of layer indices. It is a
// comparable struct so it can key Constraints.Pinned.
type LayerRange struct {
	Start int // inclusive
	End   int // inclusive
}

// Node is a fleet member and its resources (design 08 §2).
type Node struct {
	ID              string
	RAMTotalGB      float64
	RAMAvailableGB  float64 // net of OS and other workloads
	VRAMGB          float64 // 0 if CPU-only; == RAM if unified
	UnifiedMemory   bool    // Apple Silicon / GB10
	MemBandwidthGBs float64 // memory bandwidth (decode-speed proxy)
	Backends        []string
	FP4Native       bool // Blackwell / GB10
	EngineVersions  map[string]string

	// Engine-capability fields (P7 — fase2). These refine performance estimates
	// when the engine reports them; zero values reproduce the original behaviour.

	// DiskFreeGB is the free SSD capacity available for KV-cache offload,
	// populated from HardwareProfile.disk_free_gb. Used together with
	// KVSSDOffload to expand the effective KV-cache memory pool.
	DiskFreeGB float64

	// KVSSDOffload reports that the engine supports spilling cold KV-cache
	// blocks to SSD (Tutti-style offload), increasing the effective memory
	// available for the KV pool.
	// TODO: add kv_ssd_offload to NodeCapabilities proto (fase2 follow-up);
	// for now, callers set this field directly.
	KVSSDOffload bool

	// PrefixCachingFactor is the expected fraction of input tokens that will
	// be KV-cache hits (0–1). A non-zero value reduces the effective prefill
	// compute, raising reported prefill throughput.
	// TODO: add prefix_caching_factor to NodeCapabilities proto (fase2 follow-up);
	// for now, callers set this field directly.
	PrefixCachingFactor float64
}

// Link is a directed network edge between two nodes (design 08 §2).
type Link struct {
	From         string
	To           string
	RTTms        float64
	BandwidthGBs float64
}

// Quantization is a weight-quantization variant of a model (design 08 §2).
type Quantization struct {
	Name        string  // nvfp4, q4_k_m, q8, q2_asym, ...
	SizeGB      float64 // model weight size
	RequiresFP4 bool
	Quality     float64 // 0..1 relative-quality proxy
	EmulatedFP4 bool    // set by phase A when FP4 is required but emulated
}

// DraftInfo describes an optional speculative-decoding draft model.
type DraftInfo struct {
	Available  bool
	Type       string
	TailLayers int // draft heads touch the last TailLayers layers
}

// ModelSpec is the static description of a model to serve (design 08 §2).
type ModelSpec struct {
	ID            string
	IsMoE         bool
	Layers        int
	ParamsTotalB  float64
	ParamsActiveB float64
	HiddenSize    int
	NKVHeads      int
	HeadDim       int
	AttentionType AttentionType
	ContextMax    int
	Quantizations []Quantization
	Draft         DraftInfo
}

// Constraints are the operator overrides that steer the planner (design 08 §14).
// The zero value is a valid "no constraints, AUTO mode" input.
type Constraints struct {
	Pinned            map[LayerRange]NodeID // fixed layer→node shards
	IncludeNodes      []string              // if non-empty, restrict to these
	ExcludeNodes      []string              // always removed from consideration
	ForceNodeCount    *int                  // nil = free
	ForceQuant        *string               // nil = run phase A
	ForceHost         *string               // nil = planner chooses the host
	QualityWeightBias float64               // shifts W4 in the cost function
	Mode              Mode
}

// Assignment places a contiguous shard of layers [LayerStart, LayerEnd]
// (inclusive) on a node (design 08 §2).
type Assignment struct {
	NodeID     string
	Role       Role
	LayerStart int
	LayerEnd   int
	Draft      bool
}

// PerfEstimate is the estimated performance envelope of a plan, shown in the UI
// as a range because the coefficients need calibration (design 08 §11).
type PerfEstimate struct {
	DecodeTokSMin  float64
	DecodeTokSMax  float64
	PrefillTokSMin float64
	PrefillTokSMax float64
	HeadroomGB     float64
}

// DeploymentPlan is a concrete plan produced by the planner (design 08 §2).
type DeploymentPlan struct {
	PlanID        string
	ModelID       string
	Quantization  string
	Assignments   []Assignment
	PipelineOrder []string
	Estimated     PerfEstimate
	Cost          float64
	Explanation   []string                   // human-readable, for UI explainability
	FailoverAlt   map[string]*DeploymentPlan // node-id → plan to use if it dies (phase F)
}

// PlanError is returned when no valid plan exists. It implements error.
type PlanError struct {
	Reason      string
	DeficitGB   float64 // how much memory is missing (0 if not a memory problem)
	Suggestions []string
}

// Error implements the error interface.
func (e *PlanError) Error() string {
	if e.DeficitGB > 0 {
		return fmt.Sprintf("planner: %s (deficit %.2f GB)", e.Reason, e.DeficitGB)
	}
	return fmt.Sprintf("planner: %s", e.Reason)
}

// PlanResult mirrors the design's sum type "PlanResult = DeploymentPlan |
// PlanError". Idiomatic Go surfaces it as the (*DeploymentPlan, error) pair
// returned by Plan — exactly one side is non-nil, and any non-nil error is
// always a *PlanError. This struct is offered for callers that prefer the
// explicit union shape; Plan itself returns the (*DeploymentPlan, error) pair.
type PlanResult struct {
	Plan *DeploymentPlan
	Err  *PlanError
}
