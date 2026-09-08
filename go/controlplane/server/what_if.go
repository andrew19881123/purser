package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/purser/purser/go/controlplane/registry"
	plannerplan "github.com/purser/purser/go/planner/plan"
)

// whatIfRequest is the body of POST /api/v1/planner/what-if.
type whatIfRequest struct {
	ModelID              string             `json:"model_id"`
	HypotheticalNodes    []hypotheticalNode `json:"hypothetical_nodes"`
	IncludeExistingNodes bool               `json:"include_existing_nodes"`
}

// hypotheticalNode describes a virtual fleet node in the what-if scenario.
type hypotheticalNode struct {
	NodeID           string  `json:"node_id"`
	GPUVRAMgb        float64 `json:"gpu_vram_gb"`
	GPUCount         int     `json:"gpu_count"`
	CPUCores         int     `json:"cpu_cores"`
	RAMgb            float64 `json:"ram_gb"`
	NetBandwidthGbps float64 `json:"net_bandwidth_gbps"`
	SSDBandwidthGbps float64 `json:"ssd_bandwidth_gbps"`
}

// whatIfPlanInfo carries the planner's outcome for a hypothetical fleet.
type whatIfPlanInfo struct {
	Assignments            []whatIfAssignment `json:"assignments"`
	EstimatedDecodeTokSMin float64            `json:"estimated_decode_tok_s_min"`
	EstimatedDecodeTokSMax float64            `json:"estimated_decode_tok_s_max"`
	PipelineDepth          int                `json:"pipeline_depth"`
}

// whatIfAssignment places a layer shard on a specific node.
type whatIfAssignment struct {
	NodeID     string `json:"node_id"`
	LayerStart int    `json:"layer_start"`
	LayerEnd   int    `json:"layer_end"`
}

// whatIfCurrentPlan describes the outcome of the current (unmodified) fleet.
type whatIfCurrentPlan struct {
	Feasible      bool    `json:"feasible"`
	DeficitVRAMgb float64 `json:"deficit_vram_gb,omitempty"`
}

// whatIfImprovement quantifies the gain from the hypothetical fleet over the
// current one.
type whatIfImprovement struct {
	DecodeTokSDeltaMin float64 `json:"decode_tok_s_delta_min"`
	DecodeTokSDeltaMax float64 `json:"decode_tok_s_delta_max"`
	// FeasibilityChange is one of:
	//   "infeasible_to_feasible" — was not deployable, now is
	//   "feasible_improved"     — was deployable, throughput improved
	//   "feasible_unchanged"    — was deployable, no measurable improvement
	FeasibilityChange string `json:"feasibility_change"`
}

// whatIfResponse is the body of POST /api/v1/planner/what-if.
type whatIfResponse struct {
	Feasible    bool               `json:"feasible"`
	Plan        *whatIfPlanInfo    `json:"plan,omitempty"`
	CurrentPlan *whatIfCurrentPlan `json:"current_plan,omitempty"`
	Improvement *whatIfImprovement `json:"improvement,omitempty"`
}

// handleWhatIfPlan handles POST /api/v1/planner/what-if.
//
// It runs the DP layer-split planner against a hypothetical fleet — the
// caller's virtual nodes, optionally merged with the real fleet — without
// writing anything to the registry. Both the hypothetical plan and the
// current-fleet plan (when include_existing_nodes is true) are returned so
// the caller can compare them before making hardware procurement decisions.
//
// Auth: any authenticated role (admin or viewer) — enforced by rbacMiddleware.
// Non-GET endpoints require at least admin or a role that allows writes; the
// existing rbacMiddleware passes POST requests for admin tokens. For viewer
// access over POST, callers may need to extend the RBAC config — this endpoint
// is read-only in effect (no registry mutations).
func (s *Server) handleWhatIfPlan(w http.ResponseWriter, r *http.Request) {
	if s.planner == nil {
		s.writeError(w, http.StatusNotImplemented, "no_planner", "planner not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB body limit
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
			"request body exceeds 1 MB limit")
		return
	}
	var req whatIfRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.ModelID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "model_id is required")
		return
	}

	ctx := r.Context()

	// Resolve model spec from the registry.
	spec, err := s.planner.ModelSpecFor(ctx, req.ModelID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "model not found: "+req.ModelID)
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "model_spec_failed", err.Error())
		return
	}

	// Load the current READY fleet once; used for both the current-plan
	// comparison and as the base for the merged hypothetical fleet.
	existingNodes, existingLinks, err := s.planner.LoadFleet(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "fleet_load_failed", err.Error())
		return
	}

	// Convert hypothetical nodes to planner domain types.
	hypNodes := convertHypotheticalNodes(req.HypotheticalNodes)

	// Build the fleet for the hypothetical scenario.
	// When hypothetical_nodes is empty, always include the real fleet so the
	// caller gets a meaningful result (fallback semantics).
	var allNodes []plannerplan.Node
	var allLinks []plannerplan.Link
	if req.IncludeExistingNodes || len(hypNodes) == 0 {
		allNodes = append(allNodes, existingNodes...)
		allLinks = append(allLinks, existingLinks...)
	}
	allNodes = append(allNodes, hypNodes...)

	// Synthetic links: connect each hypothetical node bidirectionally to every
	// other node in the fleet, and to every other hypothetical node. This
	// allows the planner to route pipeline stages across the hybrid fleet.
	for _, hn := range req.HypotheticalNodes {
		bw := hn.NetBandwidthGbps / 8 // Gbps → GB/s
		if bw <= 0 {
			bw = 1.25 // default: 10 Gbps NIC
		}
		if req.IncludeExistingNodes || len(hypNodes) == 0 {
			for _, en := range existingNodes {
				allLinks = append(allLinks,
					plannerplan.Link{From: hn.NodeID, To: en.ID, BandwidthGBs: bw, RTTms: 0.1},
					plannerplan.Link{From: en.ID, To: hn.NodeID, BandwidthGBs: bw, RTTms: 0.1},
				)
			}
		}
		for _, hn2 := range req.HypotheticalNodes {
			if hn.NodeID != hn2.NodeID {
				allLinks = append(allLinks,
					plannerplan.Link{From: hn.NodeID, To: hn2.NodeID, BandwidthGBs: bw, RTTms: 0.1},
				)
			}
		}
	}

	// Run the planner against the hypothetical fleet.
	hypDP, hypErr := plannerplan.Plan(ctx, allNodes, allLinks, spec, plannerplan.Constraints{})

	// Run the planner against the current fleet only (for comparison).
	var currentPlanResult *whatIfCurrentPlan
	var curDecodeMin, curDecodeMax float64
	if req.IncludeExistingNodes {
		curDP, curErr := plannerplan.Plan(ctx, existingNodes, existingLinks, spec, plannerplan.Constraints{})
		if curErr != nil {
			var pe *plannerplan.PlanError
			if errors.As(curErr, &pe) {
				currentPlanResult = &whatIfCurrentPlan{
					Feasible:      false,
					DeficitVRAMgb: pe.DeficitGB,
				}
			} else {
				currentPlanResult = &whatIfCurrentPlan{Feasible: false}
			}
		} else {
			curDecodeMin = curDP.Estimated.DecodeTokSMin
			curDecodeMax = curDP.Estimated.DecodeTokSMax
			currentPlanResult = &whatIfCurrentPlan{Feasible: true}
		}
	}

	// Build the response.
	if hypErr != nil {
		var pe *plannerplan.PlanError
		if errors.As(hypErr, &pe) {
			resp := whatIfResponse{
				Feasible:    false,
				CurrentPlan: currentPlanResult,
			}
			s.writeJSON(w, http.StatusOK, resp)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "plan_failed", hypErr.Error())
		return
	}

	planInfo := &whatIfPlanInfo{
		Assignments:            make([]whatIfAssignment, 0, len(hypDP.Assignments)),
		EstimatedDecodeTokSMin: hypDP.Estimated.DecodeTokSMin,
		EstimatedDecodeTokSMax: hypDP.Estimated.DecodeTokSMax,
		PipelineDepth:          len(hypDP.PipelineOrder),
	}
	for _, a := range hypDP.Assignments {
		planInfo.Assignments = append(planInfo.Assignments, whatIfAssignment{
			NodeID:     a.NodeID,
			LayerStart: a.LayerStart,
			LayerEnd:   a.LayerEnd,
		})
	}

	resp := whatIfResponse{
		Feasible:    true,
		Plan:        planInfo,
		CurrentPlan: currentPlanResult,
	}
	if currentPlanResult != nil {
		resp.Improvement = computeWhatIfImprovement(
			currentPlanResult.Feasible,
			curDecodeMin, curDecodeMax,
			hypDP.Estimated.DecodeTokSMin, hypDP.Estimated.DecodeTokSMax,
		)
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// convertHypotheticalNodes converts the JSON hypothetical node descriptions
// into planner Node domain types.
func convertHypotheticalNodes(hns []hypotheticalNode) []plannerplan.Node {
	nodes := make([]plannerplan.Node, 0, len(hns))
	for _, hn := range hns {
		n := plannerplan.Node{
			ID:             hn.NodeID,
			VRAMGB:         hn.GPUVRAMgb,
			RAMTotalGB:     hn.RAMgb,
			RAMAvailableGB: hn.RAMgb,
			// MemBandwidthGBs is GPU memory bandwidth; not supplied by the caller
			// so the planner falls back to the neutral reference bandwidth (100 GB/s),
			// producing a conservative performance estimate.
		}
		// Enable SSD offload when the caller reports SSD bandwidth > 0.
		// Use a conservative 1 TB free disk assumption for the KV-cache pool size.
		if hn.SSDBandwidthGbps > 0 {
			n.KVSSDOffload = true
			n.DiskFreeGB = 1000 // 1 TB default
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// computeWhatIfImprovement calculates the throughput delta between the current
// fleet plan and the hypothetical plan, and classifies the feasibility change.
func computeWhatIfImprovement(currentFeasible bool, curMin, curMax, hypMin, hypMax float64) *whatIfImprovement {
	imp := &whatIfImprovement{
		DecodeTokSDeltaMin: hypMin - curMin,
		DecodeTokSDeltaMax: hypMax - curMax,
	}
	switch {
	case !currentFeasible:
		imp.FeasibilityChange = "infeasible_to_feasible"
	case hypMin > curMin:
		imp.FeasibilityChange = "feasible_improved"
	default:
		imp.FeasibilityChange = "feasible_unchanged"
	}
	return imp
}
