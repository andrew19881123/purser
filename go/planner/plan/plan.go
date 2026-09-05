package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Tuning constants. Every performance coefficient below is a NAMED, CALIBRATABLE
// knob: the values are defensible first-order estimates, but the design
// (08 §10–§12, §15) calls for tightening them against real micro-benchmarks —
// per-node decode profiles for the memory-bandwidth terms, and `tc netem`
// sweeps for the link (commTime) terms. None of them is a magic number buried in
// an expression; change them here and the whole planner re-calibrates.
const (
	// overheadOSRuntimeGB is the per-node memory reserved for the OS and the
	// inference runtime, added on top of weights + KV cache (design 08 §4).
	overheadOSRuntimeGB = 2.0

	// bytesFP16 is the element size assumed for an un-compressed KV entry.
	bytesFP16 = 2.0

	// defaultHeadroomFraction is the fraction of a node's useful memory kept
	// free as headroom (design 08 §6 uses HEADROOM in the stage-fit check).
	// Multi-node stages must fit WITH this margin; the single-node plan (rule G3)
	// only needs a non-negative fit. Enforced end-to-end by validatePlanMemory.
	defaultHeadroomFraction = 0.10

	// referenceMemBandwidthGBs normalises a node's memory bandwidth into a
	// unit-less weight for usefulCapacity ranking (design 08 §5, normalize()). It
	// also stands in as a NEUTRAL bandwidth when a node reports none, so an
	// unknown-bandwidth node still yields a finite (not zero) time estimate.
	referenceMemBandwidthGBs = 100.0

	// memBandwidthUtilFraction is the fraction of a node's PEAK memory bandwidth
	// actually realised while streaming weights at decode — the Model-Bandwidth
	// Utilisation (MBU). Real engines sustain only ~50–85% of the spec sheet
	// figure (kernel launch gaps, non-contiguous experts, partial cache lines),
	// so the raw peak-bandwidth model overestimates tok/s. 0.70 is a conservative
	// mid-range default; CALIBRATABLE per engine/backend with a decode benchmark.
	memBandwidthUtilFraction = 0.70

	// expectedAcceptedTokens is the speculative-decoding effective speed-up
	// applied to the decode rate when the model ships a draft: each verified
	// target step advances the sequence by roughly this many tokens (design 08
	// §11, ~4–5 accepted on coding-style traffic). CALIBRATABLE — the realised
	// speed-up depends on the draft/target acceptance rate for the workload.
	expectedAcceptedTokens = 4.5

	// perfBandFraction is the ±band applied around the point estimate to build
	// the min/max range shown in the UI. The point estimate rests on coarse,
	// yet-to-be-calibrated coefficients, so the band makes that uncertainty
	// explicit rather than implying false precision. CALIBRATABLE — narrow it as
	// the coefficients are pinned down by benchmarks (design 08 §11, §15).
	perfBandFraction = 0.30

	// prefillComputeMultiple is how much faster prefill runs than decode: decode
	// is memory-bound (streams weights once PER token) while prefill is
	// compute-bound and processes the whole prompt in large batched matmuls that
	// reuse each weight across many tokens. Applied as a multiple of the decode
	// rate to get a prefill rate (design 08 §11). CALIBRATABLE — the true ratio
	// depends on batch size, sequence length and the compute/bandwidth roofline.
	prefillComputeMultiple = 8.0

	// kvSsdOffloadFactor is the fraction of a node's free SSD space that
	// KV-cache SSD offload (Tutti-style) contributes to the effective KV-cache
	// memory pool. 0.50 is a conservative first estimate — SSD bandwidth is
	// substantially lower than VRAM bandwidth, so only a fraction of nominal
	// SSD capacity is practically usable at decode speed without stalling the
	// pipeline. CALIBRATABLE — measure with actual SSD-offload throughput
	// benchmarks and the latency penalty at decode (design 08 §10, fase2).
	kvSsdOffloadFactor = 0.50

	// kvSsdOffloadMaxMultiplier caps the total KV-cache SSD contribution: the
	// SSD-augmented effective memory cannot exceed this multiple of the node's
	// VRAM (or available RAM for unified/CPU nodes). Prevents unrealistically
	// high headroom estimates when a node has a very large (but slow) SSD.
	// CALIBRATABLE — raise only when measured SSD offload sustains high,
	// stable throughput at the chosen tier (design 08 §10, fase2).
	kvSsdOffloadMaxMultiplier = 2.0
)

// Plan is the planner entry point (design 08 §3): given the fleet state
// (nodes + links), a model, and operator constraints, it returns a valid
// DeploymentPlan or a *PlanError.
//
// It orchestrates the full pipeline A→F end-to-end:
//   - Phase A (§4, selectQuantization): highest-quality quantization whose
//     weights + KV cache + per-node overhead fit the aggregate useful memory.
//   - Phase B (§5, candidateSubsets): rank nodes by useful capacity, find
//     k_min, and emit the subsets ranked[0:k] for k in k_min .. k_min+DELTA.
//     k_min == 1 short-circuits to the single-node plan (rule G3).
//   - Phase C (§6, dpPartition): for each candidate subset, a throughput-aware
//     DP splits the layer chain into contiguous shards minimising the
//     bottleneck stage time; the cheapest feasible candidate wins.
//   - Phase D (§7, orderNodes): the pipeline ORDER is the min-cost Hamiltonian
//     path over the activation-transfer edge costs (Held-Karp exact for small
//     fleets, nearest-neighbour + 2-opt above HeldKarpMaxNodes), co-optimised
//     with the phase-C partition in multiNodePlan; host = ForceHost or the most
//     capable node.
//   - Phase E (§8, placeDraft, draft.go): if the model ships a speculative
//     draft, mark the pipeline TAIL node (the shard holding the last layers) as
//     the draft carrier — the MTP/draft heads live on the box of coda, adding
//     no extra hop.
//   - Phase F (§9, computeFailoverPlans, failover.go): for every node used by
//     the plan, pre-compute an alternative plan over the fleet minus that node.
//
// The public Plan drives failover; the alternatives are planned NON-recursively
// (see planInternal's computeFailover guard), so the recursion is exactly one
// level deep.
func Plan(nodes []Node, links []Link, model ModelSpec, c Constraints) (*DeploymentPlan, error) {
	return planInternal(nodes, links, model, c, true)
}

// planInternal is the shared orchestrator behind Plan. computeFailover is the
// anti-recursion guard (design 08 §9): the public Plan calls it with true, and
// each per-node failover alternative (failover.go) is planned with false so the
// alternatives do NOT themselves spawn failover plans — their FailoverAlt stays
// empty. This bounds the failover recursion at a single level.
func planInternal(nodes []Node, links []Link, model ModelSpec, c Constraints, computeFailover bool) (*DeploymentPlan, error) {
	// Operator include/exclude filter (design 08 §14: acts on `nodes` first).
	nodes = applyNodeFilter(nodes, c)
	if len(nodes) == 0 {
		return nil, &PlanError{
			Reason:      "no nodes available after applying include/exclude constraints",
			Suggestions: []string{"widen include_nodes", "remove some exclude_nodes", "register more nodes"},
		}
	}
	if model.Layers <= 0 {
		return nil, &PlanError{
			Reason:      fmt.Sprintf("model %q declares no layers", model.ID),
			Suggestions: []string{"provide a ModelSpec with Layers > 0"},
		}
	}

	// KV cache is included in the fit and is often dominant on long contexts
	// (design 08 §4, §12).
	kv := estimateKVCache(model, model.ContextMax)

	// Phase A — quantization selection.
	quant, err := selectQuantization(nodes, model, kv, c)
	if err != nil {
		return nil, err
	}

	// Phase B — candidate node subsets (design 08 §5).
	subsets := candidateSubsets(nodes, model, quant, kv, c)
	if len(subsets) == 0 {
		// Passed phase A's aggregate-RAM check but the useful memory (bounded
		// by VRAM on discrete-GPU nodes) does not add up: still too large.
		aggUseful := 0.0
		for _, n := range nodes {
			aggUseful += usefulMemory(n)
		}
		return nil, &PlanError{
			Reason: fmt.Sprintf(
				"model %q at quant %q does not fit even across all %d nodes' useful memory",
				model.ID, quant.Name, len(nodes)),
			DeficitGB: (quant.SizeGB + kv + overheadOSRuntimeGB*float64(len(nodes))) - aggUseful,
			Suggestions: []string{
				"choose a smaller quantization",
				"reduce ContextMax to shrink the KV cache",
				"add nodes with more (V)RAM",
			},
		}
	}

	// One node index shared by the multi-node loop and the final fit check.
	nodeByID := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	var plan *DeploymentPlan
	if len(subsets[0]) == 1 {
		// Rule G3 (design 08 §5): if the model fits on one node, don't
		// distribute. candidateSubsets returns its smallest candidate first, so
		// a size-1 head means k_min == 1 (or ForceNodeCount == 1).
		plan = singleNodePlan(subsets[0][0], model, quant, kv, links, c)
	} else {
		// Phase C/D — evaluate each candidate subset and keep the cheapest
		// feasible plan (design 08 §5 selection loop, §6 partition DP).
		var best *DeploymentPlan
		for _, subset := range subsets {
			if len(subset) < 2 {
				continue
			}
			// multiNodePlan co-optimises the pipeline order (phase D) with the
			// partition DP (phase C) and scores the result; nil means no
			// feasible contiguous partition on this subset → try the next.
			cand := multiNodePlan(subset, nodeByID, model, quant, links, c)
			if cand == nil {
				continue
			}
			if best == nil || cand.Cost < best.Cost {
				best = cand
			}
		}

		if best == nil {
			return nil, &PlanError{
				Reason: fmt.Sprintf(
					"model %q at quant %q: no feasible contiguous partition on any of the %d candidate subsets (memory/headroom)",
					model.ID, quant.Name, len(subsets)),
				Suggestions: []string{
					"choose a smaller quantization",
					"reduce ContextMax to shrink the per-node KV cache",
					"add nodes with more (V)RAM to relieve the tightest stage",
				},
			}
		}
		plan = best
	}

	// Blindatura dei vincoli (design 08 §6/§14): the AUTO pipeline guarantees
	// every node fits its shard — phase B sizes the single-node case and the
	// phase-C DP enforces the HEADROOM margin per stage — but the operator
	// constraint passes can BYPASS those guarantees. applyPinnedRanges relocates
	// a whole shard onto the pinned node without re-checking its memory, and
	// ForceNodeCount == 1 short-circuits to a single-node plan that never verifies
	// the fit. Re-verify the assembled plan against every node's useful memory and
	// turn a constraint that makes it infeasible into a motivated *PlanError,
	// rather than emitting a plan that silently violates memory/headroom.
	if err := validatePlanMemory(plan, nodeByID, model, quant, c); err != nil {
		return nil, err
	}

	// Phase E — speculative-draft placement on the pipeline tail (design 08 §8).
	placeDraft(plan, model)

	// Phase F — per-node failover alternatives (design 08 §9). Guarded so the
	// alternatives are planned non-recursively (computeFailover == false).
	if computeFailover {
		computeFailoverPlans(plan, nodes, links, model, c)
	}

	return plan, nil
}

// applyNodeFilter applies the include/exclude operator constraints
// (design 08 §14). An empty IncludeNodes means "no restriction".
func applyNodeFilter(nodes []Node, c Constraints) []Node {
	include := toSet(c.IncludeNodes)
	exclude := toSet(c.ExcludeNodes)
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if _, bad := exclude[n.ID]; bad {
			continue
		}
		if len(include) > 0 {
			if _, ok := include[n.ID]; !ok {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// selectQuantization is phase A (design 08 §4): among the model's
// quantizations, pick the highest-quality one whose weights + KV cache +
// per-node overhead fit the aggregate available RAM. Honours Constraints.
// ForceQuant by short-circuiting the search.
func selectQuantization(nodes []Node, model ModelSpec, kv float64, c Constraints) (Quantization, error) {
	if len(model.Quantizations) == 0 {
		return Quantization{}, &PlanError{
			Reason:      fmt.Sprintf("model %q declares no quantizations", model.ID),
			Suggestions: []string{"provide at least one Quantization in the ModelSpec"},
		}
	}

	ramAggregate := 0.0
	for _, n := range nodes {
		ramAggregate += n.RAMAvailableGB
	}
	overhead := overheadOSRuntimeGB * float64(len(nodes))

	// Forced quantization: skip phase A but still validate the fit.
	if c.ForceQuant != nil {
		q, ok := lookupQuant(model.Quantizations, *c.ForceQuant)
		if !ok {
			return Quantization{}, &PlanError{
				Reason:      fmt.Sprintf("forced quantization %q is not offered by model %q", *c.ForceQuant, model.ID),
				Suggestions: []string{"pick one of the model's declared quantizations"},
			}
		}
		q.EmulatedFP4 = q.RequiresFP4 && !anyFP4(nodes)
		if ramAggregate < q.SizeGB+kv+overhead {
			return Quantization{}, &PlanError{
				Reason:      fmt.Sprintf("forced quantization %q does not fit the aggregate RAM", q.Name),
				DeficitGB:   (q.SizeGB + kv + overhead) - ramAggregate,
				Suggestions: []string{"drop force_quant to let phase A choose", "add memory"},
			}
		}
		return q, nil
	}

	// Evaluate quantizations from highest to lowest quality; the first that
	// fits wins (design 08 §4).
	ranked := append([]Quantization(nil), model.Quantizations...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Quality > ranked[j].Quality })

	for _, q := range ranked {
		if ramAggregate >= q.SizeGB+kv+overhead {
			// FP4 required but no node has it: emulable, but penalised
			// (design 08 §4). Flagged for the cost function / explanation.
			q.EmulatedFP4 = q.RequiresFP4 && !anyFP4(nodes)
			return q, nil
		}
	}

	// Nothing fits. Report the deficit against the smallest-footprint
	// quantization (the best-effort candidate).
	smallest := ranked[0]
	for _, q := range ranked {
		if q.SizeGB < smallest.SizeGB {
			smallest = q
		}
	}
	return Quantization{}, &PlanError{
		Reason:    fmt.Sprintf("model %q is too large: no quantization fits the aggregate RAM", model.ID),
		DeficitGB: (smallest.SizeGB + kv + overhead) - ramAggregate,
		Suggestions: []string{
			"add nodes or free memory",
			"reduce ContextMax to shrink the KV cache",
			"provide a smaller quantization",
		},
	}
}

// singleNodePlan builds the trivial plan that hosts every layer of the model on
// a single node (design 08 §3, rule G3: don't distribute when it fits on one).
func singleNodePlan(n Node, model ModelSpec, quant Quantization, kv float64, links []Link, c Constraints) *DeploymentPlan {
	assign := Assignment{
		NodeID:     n.ID,
		Role:       RoleHost,
		LayerStart: 0,
		LayerEnd:   model.Layers - 1,
		// Draft placement is phase E (placeDraft, draft.go), applied uniformly
		// by planInternal after the plan is assembled — on a single node the
		// tail is trivially this node.
	}

	required := quant.SizeGB + kv + overheadOSRuntimeGB
	headroom := usefulMemory(n) - required

	explanation := []string{
		fmt.Sprintf("selected quantization %q (quality %.2f)", quant.Name, quant.Quality),
		fmt.Sprintf("model fits on a single node %q: no pipeline split needed (rule G3)", n.ID),
		fmt.Sprintf("weights %.1f GB + KV cache %.1f GB + overhead %.1f GB = %.1f GB required; %.1f GB useful memory (%.1f GB headroom)",
			quant.SizeGB, kv, overheadOSRuntimeGB, required, usefulMemory(n), headroom),
	}
	if quant.EmulatedFP4 {
		explanation = append(explanation, "quantization requires FP4 but no node is FP4-native: running emulated (penalised)")
	}
	// Note operator constraints that are accepted but not yet acted on by the
	// minimal skeleton, so the UI can surface them (design 08 §14).
	if c.ForceHost != nil && *c.ForceHost != n.ID {
		explanation = append(explanation, fmt.Sprintf("note: force_host=%q ignored — single-node plan hosts on %q", *c.ForceHost, n.ID))
	}

	return &DeploymentPlan{
		PlanID:        newPlanID(model.ID, n.ID),
		ModelID:       model.ID,
		Quantization:  quant.Name,
		Assignments:   []Assignment{assign},
		PipelineOrder: []string{n.ID},
		Estimated:     estimateSingleNode(n, model, quant, headroom),
		Cost:          0, // single node → zero hops → baseline cost
		Explanation:   explanation,
		// Phase F (computeFailoverPlans, failover.go) fills this in for the
		// public Plan path; starts empty so failover alternatives (planned with
		// computeFailover == false) carry no nested plans.
		FailoverAlt: map[string]*DeploymentPlan{},
	}
}

// validatePlanMemory re-verifies, after all operator constraints have been
// applied, that every node in the assembled plan actually holds everything now
// assigned to it within its useful memory (design 08 §6/§14). It is the backstop
// for the two constraint passes that can bypass the AUTO pipeline's own fit
// guarantees:
//
//   - applyPinnedRanges relocates a whole shard onto the pinned node WITHOUT
//     re-checking that node's memory, so a pin can pile more than one shard onto
//     one node and blow past its capacity.
//   - ForceNodeCount == 1 short-circuits to singleNodePlan, which reports but
//     does not enforce the fit, so a model too large for the top node yields a
//     plan with NEGATIVE headroom.
//
// The need is aggregated PER NODE across every shard assigned to it — weights +
// KV share, plus a SINGLE per-node overhead (not one per shard) — and compared
// to that node's useful memory. A plan that spans more than one distinct node
// must additionally clear the design's HEADROOM margin (the same one the phase-C
// DP enforces); a single-node plan (rule G3) need only be non-negative. On a
// violation it returns a motivated *PlanError naming the offending node and the
// constraint at fault, instead of letting an invalid plan escape.
func validatePlanMemory(plan *DeploymentPlan, nodeByID map[string]Node, model ModelSpec, quant Quantization, c Constraints) error {
	if plan == nil || len(plan.Assignments) == 0 {
		return nil
	}
	ctx := model.ContextMax

	// Aggregate the layer count assigned to each distinct node (a pin can map two
	// shards to the same node); preserve first-seen order for a stable message.
	layersOn := make(map[string]int, len(plan.Assignments))
	order := make([]string, 0, len(plan.Assignments))
	for _, a := range plan.Assignments {
		if _, seen := layersOn[a.NodeID]; !seen {
			order = append(order, a.NodeID)
		}
		layersOn[a.NodeID] += a.LayerEnd - a.LayerStart + 1
	}
	multiNode := len(order) > 1

	for _, id := range order {
		n, ok := nodeByID[id]
		if !ok {
			return &PlanError{
				Reason:      fmt.Sprintf("plan assigns layers to unknown node %q", id),
				Suggestions: []string{"re-run planning against the current fleet"},
			}
		}
		nLayers := layersOn[id]
		weight := quant.SizeGB * float64(nLayers) / float64(model.Layers)
		required := weight + kvCachePerNode(model, nLayers, ctx) + overheadOSRuntimeGB
		useful := usefulMemory(n)

		// Single-node plans (rule G3) do not reserve the HEADROOM margin; multi-node
		// stages must, matching the phase-C DP's stage-fit check.
		limit := useful
		if multiNode {
			limit = useful * (1 - defaultHeadroomFraction)
		}
		if required > limit {
			return &PlanError{
				Reason: fmt.Sprintf(
					"%s forces %d of %d layer(s) onto node %q, needing %.1f GB but only %.1f GB is usable%s — the plan would violate memory/headroom",
					constraintCulprit(c), nLayers, model.Layers, id, required, limit, headroomSuffix(multiNode)),
				DeficitGB:   required - limit,
				Suggestions: constraintSuggestions(c),
			}
		}
	}
	return nil
}

// constraintCulprit names the operator override most likely responsible for an
// over-capacity node, for the validatePlanMemory error message.
func constraintCulprit(c Constraints) string {
	switch {
	case len(c.Pinned) > 0 && c.ForceNodeCount != nil:
		return "the pin/force-node-count constraints"
	case len(c.Pinned) > 0:
		return "a pinned layer range"
	case c.ForceNodeCount != nil:
		return fmt.Sprintf("force_node_count=%d", *c.ForceNodeCount)
	default:
		return "the plan"
	}
}

// headroomSuffix annotates whether the usable figure already discounts HEADROOM.
func headroomSuffix(multiNode bool) string {
	if multiNode {
		return fmt.Sprintf(" (after the %.0f%% headroom margin)", defaultHeadroomFraction*100)
	}
	return ""
}

// constraintSuggestions tailors the remediation hints to the offending override.
func constraintSuggestions(c Constraints) []string {
	switch {
	case len(c.Pinned) > 0 && c.ForceNodeCount != nil:
		return []string{"relax the pinned ranges", "drop or raise force_node_count", "add memory to the constrained node"}
	case len(c.Pinned) > 0:
		return []string{"pin the range to a node with more memory", "pin fewer layers", "drop the pin to let the DP balance the split"}
	case c.ForceNodeCount != nil:
		return []string{"raise force_node_count to spread the model across more nodes", "drop force_node_count to let phase B choose", "choose a smaller quantization"}
	default:
		return []string{"choose a smaller quantization", "add nodes with more (V)RAM"}
	}
}

// estimateSingleNode produces a memory-bound decode/prefill RANGE for a
// single-node deployment (design 08 §11). The whole model runs on one node, so
// the pipeline "bottleneck" is just that node's per-token weight-streaming time
// over ALL layers — exactly computeTime for the full shard [0, Layers). Reusing
// computeTime (rather than re-deriving the formula here) keeps the single-node
// and multi-node estimates consistent and inherits its memory-bandwidth model,
// the MBU factor, and the unknown-bandwidth fallback — so this never returns a
// zero rate for a node that merely failed to report its bandwidth.
//
// The node is passed through to estimatePerformance so that engine capability
// fields (KVSSDOffload, PrefixCachingFactor) can refine the estimate (P7).
func estimateSingleNode(n Node, model ModelSpec, quant Quantization, headroom float64) PerfEstimate {
	bottleneck := computeTime(n, model, quant, 0, model.Layers)
	return estimatePerformance(bottleneck, headroom, model, n)
}

// estimatePerformance turns a pipeline bottleneck (seconds per decoded token at
// PEAK bandwidth, already including any inter-stage communication) into a
// decode/prefill throughput RANGE (design 08 §11). The bottleneck — the slowest
// ("collo") stage — sets the pipeline pace, so decode ≈ 1 / bottleneck.
//
// The point estimate is built in three documented, calibratable steps:
//
//  1. MBU correction: computeTime uses PEAK memory bandwidth, but engines sustain
//     only memBandwidthUtilFraction of it at decode. Dividing the bottleneck by
//     that fraction converts the peak-bandwidth time into a realised wall-clock
//     time. (The correction is exact for the compute-bound bottleneck of a
//     single node; on a multi-node bottleneck it also scales the usually-small
//     comm term, which is conservative — it can only lower the reported rate.)
//  2. Speculative decoding: when the model ships a draft, each verified target
//     step advances the sequence by ~expectedAcceptedTokens tokens, so the
//     decode rate is scaled up by that effective speed-up (design 08 §11).
//  3. Prefill: compute-bound and batched, so far higher throughput than the
//     memory-bound decode — a prefillComputeMultiple multiple of the decode rate.
//
// The engine capabilities in n further refine the estimate (P7):
//   - n.PrefixCachingFactor > 0: a fraction of input tokens are KV-cache hits
//     and need no attention recompute; the effective prefill throughput scales by
//     1/(1-factor) — fewer tokens to process per request.
//   - n.KVSSDOffload && n.DiskFreeGB > 0: cold KV-cache blocks can spill to SSD,
//     expanding the effective memory available for the KV pool; HeadroomGB is
//     raised by kvSsdOffloadFactor × DiskFreeGB, capped at
//     kvSsdOffloadMaxMultiplier × VRAM (or available RAM for unified/CPU nodes).
//
// A zero-value Node (no capabilities) reproduces the original behaviour exactly.
//
// The result is deliberately a RANGE, never a single number: the coefficients
// are coarse and not yet calibrated, so the point estimate is widened by
// ±perfBandFraction to make that uncertainty explicit in the UI (design 08 §11,
// §15). Every coefficient above is a named, CALIBRATABLE constant.
func estimatePerformance(bottleneckSecPerTok, headroom float64, model ModelSpec, n Node) PerfEstimate {
	decode := 0.0
	if bottleneckSecPerTok > 0 && memBandwidthUtilFraction > 0 {
		// Realised time = peak-bandwidth time / MBU; decode rate is its inverse.
		realisedSecPerTok := bottleneckSecPerTok / memBandwidthUtilFraction
		decode = 1.0 / realisedSecPerTok
	}
	if model.Draft.Available {
		// Speculative decoding amortises the per-token round trip over the
		// tokens the target accepts in one verification step (design 08 §11).
		decode *= expectedAcceptedTokens
	}
	// Prefill is compute-bound and typically far higher throughput than the
	// memory-bound decode; a rough multiple as a placeholder point estimate.
	prefill := decode * prefillComputeMultiple

	// TODO: add prefix_caching_factor to NodeCapabilities proto (fase2 follow-up);
	// currently populated only when callers set Node.PrefixCachingFactor directly.
	//
	// Apply prefix-caching speedup: cache hits skip KV recompute, so only the
	// fraction (1-factor) of tokens require full attention at prefill. The
	// effective prefill throughput scales by 1/(1-factor). Guard: factor must be
	// strictly in (0, 1) to avoid division-by-zero and sign inversion.
	if n.PrefixCachingFactor > 0 && n.PrefixCachingFactor < 1 {
		prefill /= (1 - n.PrefixCachingFactor)
	}

	// TODO: add kv_ssd_offload to NodeCapabilities proto (fase2 follow-up);
	// currently populated only when callers set Node.KVSSDOffload directly.
	// Node.DiskFreeGB is already populated from HardwareProfile.disk_free_gb.
	//
	// Apply KV SSD offload: free disk space usable as overflow KV storage
	// expands the effective memory pool, raising reported headroom. The
	// contribution is capped at kvSsdOffloadMaxMultiplier × (V)RAM so that a
	// node with a very large (but slow) SSD does not produce misleadingly high
	// estimates.
	effectiveHeadroom := headroom
	if n.KVSSDOffload && n.DiskFreeGB > 0 {
		ssdContrib := kvSsdOffloadFactor * n.DiskFreeGB
		// Determine the cap base: discrete VRAM for GPU nodes; available RAM
		// for unified-memory or CPU-only nodes.
		capBase := n.VRAMGB
		if capBase <= 0 {
			capBase = n.RAMAvailableGB
		}
		if maxSSD := kvSsdOffloadMaxMultiplier * capBase; ssdContrib > maxSSD {
			ssdContrib = maxSSD
		}
		effectiveHeadroom += ssdContrib
	}

	return PerfEstimate{
		DecodeTokSMin:  decode * (1 - perfBandFraction),
		DecodeTokSMax:  decode * (1 + perfBandFraction),
		PrefillTokSMin: prefill * (1 - perfBandFraction),
		PrefillTokSMax: prefill * (1 + perfBandFraction),
		HeadroomGB:     effectiveHeadroom,
	}
}

// estimateKVCache estimates the KV-cache footprint in GB for a given context
// length (design 08 §12).
//
// STUB: this implements the architecture-driven compression factor only. The
// design also calls for engine-capability-driven levers — SSD offloading of the
// cold KV fraction (Tutti) and prefix caching (ContiguousKV) — which require
// querying the engine adapter's capabilities. Those are not modelled yet.
//
// TODO(fase2): read engineCaps (kv_ssd_offload, prefix caching) and apply
// kvCacheInMemory() so long contexts that only fit with offload are not
// falsely rejected.
func estimateKVCache(model ModelSpec, context int) float64 {
	if context <= 0 || model.Layers <= 0 || model.NKVHeads <= 0 || model.HeadDim <= 0 {
		return 0
	}
	// bytes = 2(K,V) * layers * n_kv_heads * head_dim * context * bytes_per_elem
	base := 2.0 *
		float64(model.Layers) *
		float64(model.NKVHeads) *
		float64(model.HeadDim) *
		float64(context) *
		bytesFP16

	factor := 1.0
	switch model.AttentionType {
	case AttentionMHA, AttentionGQA:
		factor = 1.0 // GQA: n_kv_heads already reduced in the metadata
	case AttentionMLA:
		factor = 0.10 // latent compression ~7–14x
	case AttentionLinear:
		factor = 0.01 // fixed-size state, ~independent of context (approx.)
	}
	return base * factor / 1e9
}

// kvCachePerNode returns the share of the KV cache that lives on a node holding
// layersOnNode of the model's layers (design 08 §12). Provided as a hook for
// phase C's per-stage memory fit.
func kvCachePerNode(model ModelSpec, layersOnNode, context int) float64 {
	if model.Layers <= 0 {
		return 0
	}
	return estimateKVCache(model, context) * (float64(layersOnNode) / float64(model.Layers))
}

// usefulMemory is the memory a node can actually devote to model weights + KV
// (design 08 §5). For unified-memory nodes it is the available RAM; for
// discrete-GPU nodes it is bounded by VRAM; for CPU-only nodes (no VRAM
// reported) it falls back to available RAM.
//
// NOTE: this refines the design's raw `min(ram, vram)` formula, which would
// zero out CPU-only nodes (vram == 0) — inconsistent with the doc's own worked
// example that counts a CPU box's RAM. The fallback below keeps CPU-only nodes
// usable.
func usefulMemory(n Node) float64 {
	switch {
	case n.UnifiedMemory:
		return n.RAMAvailableGB
	case n.VRAMGB > 0:
		return minFloat(n.RAMAvailableGB, n.VRAMGB)
	default: // CPU-only: no discrete VRAM reported
		return n.RAMAvailableGB
	}
}

// usefulCapacity ranks nodes by useful memory weighted by memory bandwidth
// (design 08 §5): more memory and faster memory both make a node a better host.
func usefulCapacity(n Node) float64 {
	return usefulMemory(n) * normalizeBandwidth(n.MemBandwidthGBs)
}

// normalizeBandwidth turns a memory bandwidth into a unit-less weight
// (design 08 §5, normalize()). Unknown bandwidth (<= 0) is treated as neutral
// so it neither boosts nor zeroes a node's ranking.
func normalizeBandwidth(gbs float64) float64 {
	if gbs <= 0 {
		return 1.0
	}
	return gbs / referenceMemBandwidthGBs
}

// bestNodeByCapacity returns the node with the highest usefulCapacity. Ties are
// broken by the first node in slice order. Callers must ensure len(nodes) > 0.
func bestNodeByCapacity(nodes []Node) Node {
	best := nodes[0]
	bestCap := usefulCapacity(best)
	for _, n := range nodes[1:] {
		if c := usefulCapacity(n); c > bestCap {
			best, bestCap = n, c
		}
	}
	return best
}

// anyFP4 reports whether any node is FP4-native (design 08 §4).
func anyFP4(nodes []Node) bool {
	for _, n := range nodes {
		if n.FP4Native {
			return true
		}
	}
	return false
}

// lookupQuant finds a quantization by name (design 08 §3, force_quant path).
func lookupQuant(qs []Quantization, name string) (Quantization, bool) {
	for _, q := range qs {
		if q.Name == name {
			return q, true
		}
	}
	return Quantization{}, false
}

// newPlanID builds a stable, deterministic plan identifier (no timestamps, so
// tests are reproducible).
func newPlanID(modelID, nodeID string) string {
	return "plan-" + sanitizeID(modelID) + "-" + sanitizeID(nodeID)
}

func sanitizeID(s string) string {
	r := strings.NewReplacer("/", "-", " ", "-", ":", "-")
	return r.Replace(s)
}

func toSet(ss []string) map[string]struct{} {
	if len(ss) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
