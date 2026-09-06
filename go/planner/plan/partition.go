package plan

import (
	"fmt"
	"math"
	"sort"
)

// This file implements phases B and C of the planner (design 08 §5–§6):
//
//   - Phase B (candidateSubsets): rank nodes by useful capacity, find the
//     minimum node count k_min that makes the model fit, and emit the subsets
//     ranked[0:k] for k in k_min .. k_min+DELTA. The winner is decided later,
//     on the *final cost*, not a priori (rule G1 is a bias, not a hard rule).
//
//   - Phase C (dpPartition): the algorithmic core. A throughput-aware dynamic
//     program (à la PipeEdge, arXiv:2110.14895) that splits the layer chain
//     into CONTIGUOUS shards minimising the *bottleneck* stage time — the
//     slowest stage sets the pipeline throughput. Memory is a hard constraint:
//     a shard that does not fit a node yields stageTime = +Inf, so infeasible
//     partitions are pruned automatically ("water-filling", Parallax
//     arXiv:2509.26182). Complexity O(L²·M).
//
// Phases D (optimal ordering / Held-Karp), E (draft placement) and F (failover)
// are OUT OF SCOPE here — see the TODO(fase8b) markers in plan.go.

// Phase B / cost tuning constants. Deliberately coarse placeholders — the
// design (08 §10, §15) calls for calibration against real micro-benchmarks.
const (
	// planDelta is how many extra node-count candidates phase B explores
	// beyond k_min (design 08 §5, DELTA). k in k_min .. k_min+planDelta.
	planDelta = 2

	// Cost-function weights (design 08 §10, "da calibrare su benchmark").
	// cost = W1*hops + W2*linkCost + W3*imbalance + W4*qualityPenalty + W5*fp4.
	costW1Hops      = 10.0 // every pipeline hop is expensive
	costW2LinkCost  = 1.0  // per-edge network cost
	costW3Imbalance = 5.0  // variance of per-stage times (straggler penalty)
	costW4Quality   = 8.0  // 1 - quantization quality
	costW5FP4       = 6.0  // emulated-FP4 penalty
)

// candidateSubsets is phase B (design 08 §5): rank the nodes by useful capacity
// and return the candidate node subsets phase C will evaluate. Subsets are the
// prefixes ranked[0:k] for k in k_min .. k_min+DELTA, where k_min is the fewest
// nodes whose aggregate useful memory holds the weights + KV + per-node
// overhead. ForceNodeCount pins k to a single subset. Returns nil if the model
// does not fit even on all nodes.
//
// kv is the full-model KV-cache estimate (design 08 §12); it is charged in
// aggregate here (a conservative over-estimate, matching the doc's pseudocode).
func candidateSubsets(nodes []Node, model ModelSpec, quant Quantization, kv float64, c Constraints) [][]Node {
	ranked := append([]Node(nil), nodes...)
	sort.SliceStable(ranked, func(i, j int) bool {
		return usefulCapacity(ranked[i]) > usefulCapacity(ranked[j])
	})

	// ForceNodeCount: the operator fixes k (design 08 §14) — clamp to [1, len].
	if c.ForceNodeCount != nil {
		k := *c.ForceNodeCount
		if k < 1 {
			k = 1
		}
		if k > len(ranked) {
			k = len(ranked)
		}
		return [][]Node{ranked[:k]}
	}

	ramNeeded := quant.SizeGB + kv

	// k_min = smallest k with sum(usefulMemoryForFit(ranked[0:k])) >= ramNeeded +
	// OVERHEAD*k (design 08 §5, rule G1). usefulMemoryForFit includes any KV-SSD
	// offload contribution, so nodes with SSD offload expand the aggregate and can
	// reduce k_min, enabling single-node (or fewer-node) plans that would otherwise
	// fail the aggregate check.
	kMin := -1
	aggMem := 0.0
	for k := 1; k <= len(ranked); k++ {
		aggMem += usefulMemoryForFit(ranked[k-1])
		if aggMem >= ramNeeded+overheadOSRuntimeGB*float64(k) {
			kMin = k
			break
		}
	}
	if kMin == -1 {
		return nil // does not fit even on the whole fleet
	}

	subsets := make([][]Node, 0, planDelta+1)
	for k := kMin; k <= kMin+planDelta && k <= len(ranked); k++ {
		subsets = append(subsets, ranked[:k])
	}
	return subsets
}

// activationBytes is the size in bytes of the hidden-state activation passed
// between pipeline stages for one token (design 08 §7/§11): hidden_size * 2 B.
func activationBytes(model ModelSpec) float64 {
	return float64(model.HiddenSize) * bytesFP16
}

// lookupLink finds the directed link from → to. Returns nil if none is known;
// callers treat a nil link as zero communication cost (a neutral placeholder —
// see commTime).
func lookupLink(links []Link, from, to string) *Link {
	for i := range links {
		if links[i].From == from && links[i].To == to {
			return &links[i]
		}
	}
	return nil
}

// commTime estimates the seconds spent receiving one token's activation from
// the previous pipeline stage over `link` (design 08 §6, commTime). A nil link
// (unknown / co-located / no measurement) costs nothing.
//
// CALIBRATABLE PLACEHOLDER: link.BandwidthGBs is treated here as GB/s (bytes).
// The doc's §11 perf model instead reads it as Gbps (bits, dividing by 8); the
// units must be pinned down and the coefficient calibrated in phase 3 (tc
// netem micro-benchmarks). Both the DP and the brute-force reference call this
// same function, so the correctness proof (DP == brute-force) is unaffected by
// the exact coefficient.
func commTime(model ModelSpec, link *Link) float64 {
	if link == nil {
		return 0
	}
	t := link.RTTms / 1000.0
	if link.BandwidthGBs > 0 {
		t += activationBytes(model) / (link.BandwidthGBs * 1e9)
	}
	return t
}

// computeTime estimates the seconds per decoded token the node spends on the
// shard layers[i:j) (design 08 §6, computeTime). Decode is MEMORY-BANDWIDTH
// bound: the engine streams the shard's weights once per token, so the per-token
// time is (bytes of active weights read) / (memory bandwidth):
//
//	bytes_per_token ≈ quant.SizeGB·1e9 · activeFraction · (j-i)/Layers
//	                  └─────────────── active params × bytes/weight for this shard
//	computeTime      = bytes_per_token / (MemBandwidthGBs · 1e9)
//
// quant.SizeGB is the measured quantized weight footprint, so SizeGB/Layers is
// the per-layer weight bytes and (SizeGB·1e9·activeFraction) is exactly
// params_active × bytes_per_weight — no separate bits-per-weight constant is
// needed. For MoE only the ACTIVE experts are read per token, so the bytes are
// scaled by ParamsActiveB/ParamsTotalB.
//
// This is the DP's stage-cost proxy and uses PEAK bandwidth on purpose: the DP
// only compares stages, so a common efficiency factor would not change its
// min-max choice, and keeping it peak-based leaves the compute/comm balance (and
// the DP == brute-force proof) exactly as the phase-C tests calibrated it. The
// realised-bandwidth (MBU) correction that turns this into a wall-clock tok/s
// figure is applied once, centrally, in estimatePerformance. A node that reports
// no bandwidth falls back to referenceMemBandwidthGBs — neutral, so its stage
// stays comparable rather than free.
//
// CALIBRATABLE: real per-node decode profiles (design 08 §15, "profili di
// computeTime") should replace this proxy at first deploy.
func computeTime(n Node, model ModelSpec, quant Quantization, i, j int) float64 {
	if model.Layers <= 0 {
		return 0
	}
	nLayers := float64(j - i)
	activeFraction := 1.0
	if model.ParamsTotalB > 0 && model.ParamsActiveB > 0 {
		activeFraction = model.ParamsActiveB / model.ParamsTotalB
	}
	activeWeightBytes := quant.SizeGB * 1e9 * activeFraction * nLayers / float64(model.Layers)
	bw := n.MemBandwidthGBs
	if bw <= 0 {
		bw = referenceMemBandwidthGBs // unknown bandwidth: neutral, not free
	}
	return activeWeightBytes / (bw * 1e9)
}

// stageMemNeed is the memory in GB a node must hold to serve layers[i:j):
// its share of the weights + its share of the KV cache + per-node overhead
// (design 08 §6, memNeed). Unlike computeTime this uses the FULL weights (all
// experts are resident even for MoE) scaled by the layer fraction.
func stageMemNeed(model ModelSpec, quant Quantization, i, j, context int) float64 {
	if model.Layers <= 0 {
		return 0
	}
	nLayers := j - i
	weight := quant.SizeGB * float64(nLayers) / float64(model.Layers)
	kv := kvCachePerNode(model, nLayers, context)
	return weight + kv + overheadOSRuntimeGB
}

// stageFits reports whether layers[i:j) fit node n under the HEADROOM margin
// (design 08 §6). A false result makes the stage's time +Inf in the DP.
//
// The memory limit is derived from usefulMemoryForFit (not the bare usefulMemory)
// so that nodes with KV-SSD offload contribute their SSD-augmented effective
// memory to the feasibility check — consistent with the phase-B aggregate
// and the validatePlanMemory backstop.
func stageFits(n Node, model ModelSpec, quant Quantization, i, j, context int) bool {
	return stageMemNeed(model, quant, i, j, context) <= usefulMemoryForFit(n)*(1-defaultHeadroomFraction)
}

// stageTimeAt is the shared stage-time model used by BOTH the DP and the
// brute-force reference (design 08 §6, stageTime). It returns the seconds the
// m-th stage (1-based) in `order` spends on layers[i:j), including the cost of
// receiving the activation from stage m-1, or +Inf if the shard does not fit.
func stageTimeAt(order []Node, m, i, j int, links []Link, model ModelSpec, quant Quantization, context int) float64 {
	node := order[m-1]
	if !stageFits(node, model, quant, i, j, context) {
		return math.Inf(1)
	}
	ct := 0.0
	if m >= 2 {
		ct = commTime(model, lookupLink(links, order[m-2].ID, order[m-1].ID))
	}
	return computeTime(node, model, quant, i, j) + ct
}

// dpPartition is phase C (design 08 §6): the throughput-aware DP that assigns
// the L layers to a prefix of `order` as CONTIGUOUS shards, minimising the
// bottleneck (max) stage time. `order` must be the pipeline order (order[0] is
// the host). Returns the assignments and the achieved bottleneck time, or
// (nil, +Inf) if no feasible partition exists on this subset — in which case
// the caller (phase B loop) tries the next candidate subset.
//
//	dp[i][m]  = min achievable bottleneck assigning the first i layers to the
//	            first m nodes of `order`
//	cut[i][m] = the split point i' such that node m serves layers[i'..i)
//
// Complexity O(L²·M).
func dpPartition(order []Node, links []Link, model ModelSpec, quant Quantization, context int) ([]Assignment, float64) {
	L := model.Layers
	M := len(order)
	inf := math.Inf(1)

	dp := make([][]float64, L+1)
	cut := make([][]int, L+1)
	for i := range dp {
		dp[i] = make([]float64, M+1)
		cut[i] = make([]int, M+1)
		for m := range dp[i] {
			dp[i][m] = inf
			cut[i][m] = -1
		}
	}
	dp[0][0] = 0

	for m := 1; m <= M; m++ {
		for i := 0; i <= L; i++ {
			if math.IsInf(dp[i][m-1], 1) {
				continue
			}
			for j := i + 1; j <= L; j++ {
				st := stageTimeAt(order, m, i, j, links, model, quant, context)
				if math.IsInf(st, 1) {
					// memNeed grows monotonically with j: once a shard
					// starting at i overflows node m, every larger shard does
					// too, so no feasible j remains for this (i, m).
					break
				}
				bottleneck := math.Max(dp[i][m-1], st)
				if bottleneck < dp[j][m] {
					dp[j][m] = bottleneck
					cut[j][m] = i
				}
			}
		}
	}

	// Solution: cover all L layers with the fewest-cost prefix of nodes.
	bestM, best := -1, inf
	for m := 1; m <= M; m++ {
		if dp[L][m] < best {
			best, bestM = dp[L][m], m
		}
	}
	if bestM == -1 || math.IsInf(best, 1) {
		return nil, inf // INFEASIBLE → phase B tries the next subset
	}
	return reconstructCuts(cut, L, bestM, order), best
}

// reconstructCuts walks the cut table backwards to produce the contiguous
// shard assignments. The first stage is the HOST; the rest are WORKERS.
func reconstructCuts(cut [][]int, L, usedM int, order []Node) []Assignment {
	starts := make([]int, usedM+1)
	starts[usedM] = L
	j := L
	for m := usedM; m >= 1; m-- {
		i := cut[j][m]
		starts[m-1] = i
		j = i
	}

	assignments := make([]Assignment, 0, usedM)
	for m := 1; m <= usedM; m++ {
		start, end := starts[m-1], starts[m] // [start, end) exclusive
		if end <= start {
			continue // defensive: every used stage serves >= 1 layer
		}
		role := RoleWorker
		if m == 1 {
			role = RoleHost
		}
		assignments = append(assignments, Assignment{
			NodeID:     order[m-1].ID,
			Role:       role,
			LayerStart: start,
			LayerEnd:   end - 1, // inclusive
			// TODO(fase8b): Fase E draft — the draft heads touch the tail
			// layers, so the tail stage should be marked Draft here.
		})
	}
	return assignments
}

// applyPinnedRanges honours the operator's pinned layer→node overrides
// (design 08 §6/§14). Full DP-level pinning (forcing shard boundaries to align
// with a pin) is a refinement left for later; this best-effort pass reassigns
// any shard that fully contains a pinned range to the pinned node and records
// what it did (or why it could not) in `notes`, so the UI can surface it. A pin
// naming a node outside the chosen subset is reported and ignored rather than
// silently dropped.
func applyPinnedRanges(assignments []Assignment, order []Node, pinned map[LayerRange]NodeID) []string {
	if len(pinned) == 0 {
		return nil
	}
	inSubset := make(map[string]bool, len(order))
	for _, n := range order {
		inSubset[n.ID] = true
	}
	// Deterministic iteration for stable explanations/tests.
	ranges := make([]LayerRange, 0, len(pinned))
	for r := range pinned {
		ranges = append(ranges, r)
	}
	sort.Slice(ranges, func(a, b int) bool {
		if ranges[a].Start != ranges[b].Start {
			return ranges[a].Start < ranges[b].Start
		}
		return ranges[a].End < ranges[b].End
	})

	var notes []string
	for _, r := range ranges {
		target := string(pinned[r])
		if !inSubset[target] {
			notes = append(notes, fmt.Sprintf(
				"pin layers [%d,%d]→%q ignored: node not in the selected subset", r.Start, r.End, target))
			continue
		}
		applied := false
		for idx := range assignments {
			a := &assignments[idx]
			if a.LayerStart <= r.Start && r.End <= a.LayerEnd {
				if a.NodeID != target {
					notes = append(notes, fmt.Sprintf(
						"pin layers [%d,%d]→%q: moved shard [%d,%d] from %q to %q (may exceed the exact pin)",
						r.Start, r.End, target, a.LayerStart, a.LayerEnd, a.NodeID, target))
					a.NodeID = target
				}
				applied = true
				break
			}
		}
		if !applied {
			notes = append(notes, fmt.Sprintf(
				"pin layers [%d,%d]→%q not applied: no single shard fully contains the range", r.Start, r.End, target))
		}
	}
	return notes
}
