package plan

import (
	"math"
	"sort"
)

// This file implements phase D of the planner (design 08 §7): the pipeline
// ORDERING. Given a candidate node subset (already sized by phases B/C), it
// decides in which sequence the nodes form the pipeline and which one is the
// HOST (pipeline head, order[0]).
//
// The order is chosen to minimise the total activation-transfer cost along the
// pipeline: every hop ships one token's hidden-state activation to the next
// stage, so the pipeline is a minimum-cost Hamiltonian PATH over the directed
// edge costs (edgeCost below), starting at the host. We solve it EXACTLY with
// Held-Karp (O(2^n·n²)) for small fleets and fall back to a nearest-neighbour
// construction refined by 2-opt above HeldKarpMaxNodes nodes.
//
// This REPLACES the provisional "useful-capacity DESC, host = first" order the
// phase-B loop used before. The co-optimisation loop that alternates ordering
// and the phase-C partition DP lives in multiNodePlan (multinode.go).

const (
	// HeldKarpMaxNodes is the fleet size up to which orderNodes solves the
	// min-cost Hamiltonian path EXACTLY with Held-Karp. Beyond it the exact DP
	// (2^n·n² time, 2^n·n memory) is too expensive, so we use the
	// nearest-neighbour + 2-opt heuristic. 10 keeps the worst case at ~10k
	// states — cheap — and real Purser fleets rarely exceed a handful of nodes.
	HeldKarpMaxNodes = 10

	// missingLinkPenaltyMs is the edgeCost charged for a hop whose directed link
	// was never measured (design 08 §7). It is HIGH — so orderNodes routes
	// around unknown hops whenever a measured alternative exists — but FINITE, so
	// every Hamiltonian path stays comparable and an all-unknown fleet still gets
	// a deterministic order rather than a degenerate +Inf everywhere.
	missingLinkPenaltyMs = 1e6

	// twoOptMaxPasses bounds the 2-opt refinement sweeps (each accepted move
	// strictly lowers the path cost, so it converges well before this; the cap
	// only guards against float-rounding oscillation).
	twoOptMaxPasses = 64

	// twoOptImproveEps is the minimum path-cost improvement a 2-opt reversal must
	// yield to be accepted, so rounding noise cannot cause an infinite loop.
	twoOptImproveEps = 1e-9

	// coOptMaxRounds bounds the phase-D co-optimisation loop in multiNodePlan:
	// (order the nodes) → (re-run the partition DP) → stop at the first fixed
	// point. On a LAN the ordering is partition-independent (activation size is
	// fixed), so orderNodes is idempotent and the loop reaches its fixed point on
	// the first re-order; the cap only matters if a partition-aware ordering is
	// added later (design 08 §7, "accoppiamento debole").
	coOptMaxRounds = 3
)

// bytesPerMs converts a link bandwidth in GB/s into bytes per millisecond, so
// edgeCost stays in milliseconds (consistent with Link.RTTms). Zero/unknown
// bandwidth returns 0 and edgeCost then omits the transfer term (RTT only),
// mirroring commTime's bandwidth guard.
func bytesPerMs(bandwidthGBs float64) float64 {
	if bandwidthGBs <= 0 {
		return 0
	}
	return bandwidthGBs * 1e9 / 1000.0 // (bytes/s) / (ms/s)
}

// edgeCost is the phase-D ordering cost (in milliseconds) of placing node b
// immediately after node a in the pipeline (design 08 §7): the time to ship one
// token's activation across the directed a→b link — its RTT plus the
// serialization of activationBytes at the link bandwidth. Reuses the existing
// activationBytes and lookupLink. A missing link costs missingLinkPenaltyMs
// (high but finite); a link with unknown bandwidth is charged RTT only.
func edgeCost(a, b string, model ModelSpec, links []Link) float64 {
	link := lookupLink(links, a, b)
	if link == nil {
		return missingLinkPenaltyMs
	}
	cost := link.RTTms
	if bpm := bytesPerMs(link.BandwidthGBs); bpm > 0 {
		cost += activationBytes(model) / bpm
	}
	return cost
}

// orderNodes is phase D (design 08 §7): it returns the given nodes reordered
// into the pipeline sequence, with the HOST first (order[0]). The order is the
// minimum-cost Hamiltonian PATH from the host over the directed edgeCost graph —
// solved exactly with Held-Karp for len(nodes) <= HeldKarpMaxNodes, otherwise
// with nearest-neighbour + 2-opt.
//
// HOST selection (order[0]): if Constraints.ForceHost names a node in the set it
// is the host; otherwise the host is the MOST CAPABLE node — highest
// usefulCapacity (useful memory × normalized memory bandwidth), the same ranking
// phase B uses to shortlist nodes. Fixing the host and then minimising the path
// from it keeps the client-facing head on the strongest node while still
// minimising inter-stage transfer.
//
// NOTE (signature): the design sketch nominally typed this as
// orderNodes(nodeIDs) []NodeID. We take and return []Node instead so host
// selection can read node capacity (the primary "most capable" criterion) and so
// the result feeds dpPartition directly without an ID→Node round-trip. The
// function is deterministic in the node SET (independent of input order), which
// makes it idempotent — the property the co-optimisation fixed-point check in
// multiNodePlan relies on.
func orderNodes(nodes []Node, model ModelSpec, links []Link, c Constraints) []Node {
	n := len(nodes)
	ordered := append([]Node(nil), nodes...)
	if n <= 1 {
		return ordered
	}

	// Canonicalise by ID first so the result depends only on the node SET, not on
	// the input order. This makes orderNodes deterministic and idempotent even
	// when several equal-cost paths exist — the property the co-optimisation
	// fixed-point check relies on.
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	// Put the chosen host at index 0; both solvers keep index 0 fixed as the
	// path start, so order[0] is always the host on return.
	hostIdx := chooseHostIndex(ordered, c)
	ordered[0], ordered[hostIdx] = ordered[hostIdx], ordered[0]

	if n <= HeldKarpMaxNodes {
		return heldKarpPath(ordered, model, links)
	}
	return nearestNeighbor2Opt(ordered, model, links)
}

// chooseHostIndex returns the index (within nodes) of the pipeline host: the
// forced host if Constraints.ForceHost names a node present in the set, else the
// most capable node by usefulCapacity. Ties break to the earliest index.
func chooseHostIndex(nodes []Node, c Constraints) int {
	if c.ForceHost != nil {
		for i := range nodes {
			if nodes[i].ID == *c.ForceHost {
				return i
			}
		}
		// ForceHost not in this subset: fall through to capacity-based choice.
		// multiNodePlan reports the unhonoured pin in the plan explanation.
	}
	best, bestCap := 0, usefulCapacity(nodes[0])
	for i := 1; i < len(nodes); i++ {
		if cap := usefulCapacity(nodes[i]); cap > bestCap {
			best, bestCap = i, cap
		}
	}
	return best
}

// heldKarpPath returns the exact minimum-cost Hamiltonian path that starts at
// nodes[0] (the host) and visits every node, over the directed edgeCost graph.
// Standard Held-Karp with a fixed start:
//
//	dp[mask][j]  = min cost of a path starting at node 0, visiting exactly the
//	               set `mask` (which always contains 0 and j), and ending at j.
//
// Complexity O(2^n·n²) time, O(2^n·n) memory — used only for n <= HeldKarpMaxNodes.
func heldKarpPath(nodes []Node, model ModelSpec, links []Link) []Node {
	n := len(nodes)
	full := (1 << uint(n)) - 1
	inf := math.Inf(1)

	// Precompute directed edge costs once.
	cost := make([][]float64, n)
	for i := range cost {
		cost[i] = make([]float64, n)
		for j := range cost[i] {
			if i != j {
				cost[i][j] = edgeCost(nodes[i].ID, nodes[j].ID, model, links)
			}
		}
	}

	dp := make([][]float64, 1<<uint(n))
	parent := make([][]int, 1<<uint(n))
	for m := range dp {
		dp[m] = make([]float64, n)
		parent[m] = make([]int, n)
		for j := range dp[m] {
			dp[m][j] = inf
			parent[m][j] = -1
		}
	}
	dp[1<<0][0] = 0 // start at the host (index 0)

	for mask := 0; mask <= full; mask++ {
		if mask&1 == 0 {
			continue // every path starts at the host, so bit 0 must be set
		}
		for j := 0; j < n; j++ {
			if mask&(1<<uint(j)) == 0 || math.IsInf(dp[mask][j], 1) {
				continue
			}
			for k := 0; k < n; k++ {
				if mask&(1<<uint(k)) != 0 {
					continue
				}
				nm := mask | (1 << uint(k))
				if c := dp[mask][j] + cost[j][k]; c < dp[nm][k] {
					dp[nm][k] = c
					parent[nm][k] = j
				}
			}
		}
	}

	// Cheapest endpoint over the full set.
	bestEnd, best := 0, dp[full][0]
	for j := 1; j < n; j++ {
		if dp[full][j] < best {
			best, bestEnd = dp[full][j], j
		}
	}

	// Reconstruct the path back to the host.
	idxOrder := make([]int, n)
	mask, j := full, bestEnd
	for pos := n - 1; pos >= 0; pos-- {
		idxOrder[pos] = j
		pj := parent[mask][j]
		mask ^= 1 << uint(j)
		j = pj
	}

	out := make([]Node, n)
	for i, idx := range idxOrder {
		out[i] = nodes[idx]
	}
	return out
}

// nearestNeighbor2Opt is the heuristic ordering used above HeldKarpMaxNodes: a
// nearest-neighbour path from the fixed host (nodes[0]) refined by 2-opt.
func nearestNeighbor2Opt(nodes []Node, model ModelSpec, links []Link) []Node {
	return twoOptImprove(nearestNeighborOrder(nodes, model, links), model, links)
}

// nearestNeighborOrder builds the greedy nearest-neighbour path from the fixed
// host (nodes[0]): repeatedly hop to the cheapest unvisited node by edgeCost.
func nearestNeighborOrder(nodes []Node, model ModelSpec, links []Link) []Node {
	n := len(nodes)
	visited := make([]bool, n)
	route := make([]Node, 0, n)
	visited[0] = true
	route = append(route, nodes[0])
	cur := 0
	for len(route) < n {
		next, bestC := -1, math.Inf(1)
		for k := 0; k < n; k++ {
			if visited[k] {
				continue
			}
			if c := edgeCost(nodes[cur].ID, nodes[k].ID, model, links); c < bestC {
				bestC, next = c, k
			}
		}
		visited[next] = true
		route = append(route, nodes[next])
		cur = next
	}
	return route
}

// twoOptImprove refines a pipeline path with 2-opt, keeping the host (route[0])
// fixed. Edge costs are directed and asymmetric, so a segment reversal can
// change the cost either way; we recompute the path cost and keep any
// strictly-improving reversal until a full sweep finds none. The returned path
// costs no more than the input.
func twoOptImprove(route []Node, model ModelSpec, links []Link) []Node {
	n := len(route)
	best := append([]Node(nil), route...)
	if n < 4 {
		return best // no interior segment to reverse without moving the host
	}
	bestCost := orderCost(best, model, links)
	for pass := 0; pass < twoOptMaxPasses; pass++ {
		improved := false
		for i := 1; i < n-1; i++ {
			for k := i + 1; k < n; k++ {
				cand := append([]Node(nil), best...)
				for l, r := i, k; l < r; l, r = l+1, r-1 {
					cand[l], cand[r] = cand[r], cand[l]
				}
				if c := orderCost(cand, model, links); c < bestCost-twoOptImproveEps {
					best, bestCost, improved = cand, c, true
				}
			}
		}
		if !improved {
			break
		}
	}
	return best
}

// orderCost is the total activation-transfer cost of a pipeline path: the sum of
// edgeCost over consecutive hops.
func orderCost(order []Node, model ModelSpec, links []Link) float64 {
	total := 0.0
	for i := 0; i+1 < len(order); i++ {
		total += edgeCost(order[i].ID, order[i+1].ID, model, links)
	}
	return total
}

// sameOrder reports whether two node slices are the same pipeline sequence (same
// IDs in the same positions). Used by the co-optimisation fixed-point check.
func sameOrder(a, b []Node) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
