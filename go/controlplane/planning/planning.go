// Package planning is the control-plane adapter that connects the Registry to
// the domain Planner (github.com/purser/purser/go/planner/plan).
//
// It is the missing link between "the control plane knows how to APPLY a
// DeploymentPlan" (orchestrator) and "someone PRODUCES one": given a model ID
// it reads the current fleet state out of the Registry — the READY nodes'
// hardware profiles, the measured link matrix, and the catalog ModelSpec —
// converts them into the planner's rich domain types (via the planner's own
// convert.go helpers), invokes plan.Plan, and converts the resulting
// *plan.DeploymentPlan back into the wire purser.v1.DeploymentPlan the
// orchestrator and Registry consume.
//
// Two surfaces are exposed:
//   - Plan: produce a deployable plan for one model (used by POST .../deploy).
//   - Fit / FitAll: a lighter "does this model fit the current fleet?" verdict
//     that reuses the full planner pipeline (used by GET /models to drive the
//     UI badge — "Runs on N nodes, ~X-Y tok/s" vs "Doesn't fit: missing Z GB").
package planning

import (
	"context"
	"errors"
	"fmt"

	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"github.com/purser/purser/go/planner/plan"
	"google.golang.org/protobuf/encoding/protojson"
)

// readyStates are the node lifecycle states the planner is allowed to place
// work on. It mirrors the "ready" notion used by the cluster-health endpoint:
// a node is a candidate when it is approved and idle (READY) or already serving
// (RUNNING) — both are live and reachable.
var readyStates = map[string]bool{
	purserv1.NodeState_NODE_STATE_READY.String():   true,
	purserv1.NodeState_NODE_STATE_RUNNING.String(): true,
}

// FitError reports that a model cannot be deployed on the current fleet. It is
// the control-plane projection of *plan.PlanError, carrying the actionable
// deficit and suggestions without leaking the planner's domain types to HTTP
// callers. It implements error so handlers can errors.As it into a 4xx.
type FitError struct {
	Reason      string   `json:"reason"`
	DeficitGB   float64  `json:"deficit_gb,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// Error implements the error interface with an operator-facing message.
func (e *FitError) Error() string {
	if e.DeficitGB > 0 {
		return fmt.Sprintf("%s (missing %.1f GB)", e.Reason, e.DeficitGB)
	}
	return e.Reason
}

// EstimateRange is the decode/prefill throughput envelope of a plan, flattened
// for the JSON API (the planner reports a range because the coefficients are
// uncalibrated).
type EstimateRange struct {
	DecodeMinTokS  float64 `json:"decode_min_tok_s"`
	DecodeMaxTokS  float64 `json:"decode_max_tok_s"`
	PrefillMinTokS float64 `json:"prefill_min_tok_s"`
	PrefillMaxTokS float64 `json:"prefill_max_tok_s"`
	HeadroomGB     float64 `json:"headroom_gb"`
}

// Fit is the deployability verdict for one model against the current fleet. It
// feeds the UI badge: when Deployable it carries the node count, chosen
// quantization and estimated throughput range; otherwise the deficit/reason.
type Fit struct {
	ModelID      string         `json:"model_id"`
	Deployable   bool           `json:"deployable"`
	NodeCount    int            `json:"node_count,omitempty"`
	Quantization string         `json:"quantization,omitempty"`
	Estimated    *EstimateRange `json:"estimated,omitempty"`
	DeficitGB    float64        `json:"deficit_gb,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Suggestions  []string       `json:"suggestions,omitempty"`
}

// Planner adapts the Registry to the domain planner.
type Planner struct {
	reg registry.Registry
}

// New builds a Planner backed by reg.
func New(reg registry.Registry) *Planner { return &Planner{reg: reg} }

// Plan produces a wire DeploymentPlan for modelID against the current READY
// fleet. The returned plan carries the planner's deterministic PlanID; the
// caller assigns a persistence ID before storing/applying it.
//
// Errors:
//   - registry.ErrNotFound if the model is unknown;
//   - *FitError if the model does not fit the fleet (actionable deficit);
//   - a generic error on internal/decoding failures.
func (p *Planner) Plan(ctx context.Context, modelID string, c plan.Constraints) (*purserv1.DeploymentPlan, error) {
	model, err := p.reg.GetModel(ctx, modelID)
	if err != nil {
		return nil, err // includes registry.ErrNotFound
	}
	spec, err := modelSpec(model)
	if err != nil {
		return nil, err
	}
	nodes, links, err := p.loadFleet(ctx)
	if err != nil {
		return nil, err
	}
	dp, perr := plan.Plan(nodes, links, spec, c)
	if perr != nil {
		return nil, toFitError(perr)
	}
	return dp.ToProto(), nil
}

// Fit computes the deployability verdict for a single model.
func (p *Planner) Fit(ctx context.Context, modelID string) (Fit, error) {
	model, err := p.reg.GetModel(ctx, modelID)
	if err != nil {
		return Fit{}, err
	}
	nodes, links, err := p.loadFleet(ctx)
	if err != nil {
		return Fit{}, err
	}
	return p.fit(model, nodes, links), nil
}

// FitAll computes the deployability verdict for every model in the catalog,
// loading the fleet snapshot once and re-running the planner per model.
func (p *Planner) FitAll(ctx context.Context) ([]Fit, error) {
	models, err := p.reg.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	nodes, links, err := p.loadFleet(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Fit, 0, len(models))
	for _, m := range models {
		out = append(out, p.fit(m, nodes, links))
	}
	return out, nil
}

// fit runs the planner for one model against an already-loaded fleet snapshot
// and projects the outcome into a Fit verdict.
func (p *Planner) fit(model *registry.Model, nodes []plan.Node, links []plan.Link) Fit {
	f := Fit{ModelID: model.ID}
	spec, err := modelSpec(model)
	if err != nil {
		f.Reason = err.Error()
		return f
	}
	dp, perr := plan.Plan(nodes, links, spec, plan.Constraints{})
	if perr != nil {
		var pe *plan.PlanError
		if errors.As(perr, &pe) {
			f.Reason = pe.Reason
			f.DeficitGB = pe.DeficitGB
			f.Suggestions = pe.Suggestions
		} else {
			f.Reason = perr.Error()
		}
		return f
	}
	f.Deployable = true
	f.NodeCount = len(dp.PipelineOrder)
	f.Quantization = dp.Quantization
	f.Estimated = &EstimateRange{
		DecodeMinTokS:  dp.Estimated.DecodeTokSMin,
		DecodeMaxTokS:  dp.Estimated.DecodeTokSMax,
		PrefillMinTokS: dp.Estimated.PrefillTokSMin,
		PrefillMaxTokS: dp.Estimated.PrefillTokSMax,
		HeadroomGB:     dp.Estimated.HeadroomGB,
	}
	return f
}

// loadFleet reads the READY nodes and the full link matrix from the Registry
// and converts them into the planner's domain types.
func (p *Planner) loadFleet(ctx context.Context) ([]plan.Node, []plan.Link, error) {
	regNodes, err := p.reg.ListNodes(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("planning: list nodes: %w", err)
	}
	nodes := make([]plan.Node, 0, len(regNodes))
	for _, rn := range regNodes {
		if !readyStates[rn.State] {
			continue
		}
		n, err := plannerNode(rn)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, n)
	}

	regLinks, err := p.reg.ListLinks(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("planning: list links: %w", err)
	}
	links := make([]plan.Link, 0, len(regLinks))
	for _, rl := range regLinks {
		links = append(links, plan.LinkFromMetric(&purserv1.LinkMetric{
			FromNode:     rl.FromNode,
			ToNode:       rl.ToNode,
			BandwidthGbs: rl.BandwidthGBs,
			RttMs:        rl.RTTMs,
		}))
	}
	return nodes, links, nil
}

// plannerNode converts a registry Node into a planner Node. The rich hardware
// profile (stored as protojson by the fleet Join handler) is the source of
// truth; the promoted RAM column is a fallback so nodes enrolled with only a
// coarse profile still get a non-zero useful-memory figure.
func plannerNode(rn *registry.Node) (plan.Node, error) {
	hw := &purserv1.HardwareProfile{}
	if hasJSON(rn.HardwareProfile) {
		if err := protojson.Unmarshal(rn.HardwareProfile, hw); err != nil {
			return plan.Node{}, fmt.Errorf("planning: decode hardware profile for node %q: %w", rn.ID, err)
		}
	}
	n := plan.NodeFromHardwareProfile(hw)
	if n.ID == "" {
		n.ID = rn.ID
	}
	// Fallbacks for profiles that omit the memory figures the planner needs.
	if n.RAMTotalGB == 0 {
		n.RAMTotalGB = rn.RAMGB
	}
	if n.RAMAvailableGB == 0 {
		if n.RAMTotalGB > 0 {
			n.RAMAvailableGB = n.RAMTotalGB
		} else {
			n.RAMAvailableGB = rn.RAMGB
		}
	}
	if n.VRAMGB == 0 {
		n.VRAMGB = rn.VRAMGB
	}
	return n, nil
}

// modelSpec decodes the catalog ModelSpec (protojson) into the planner type.
func modelSpec(m *registry.Model) (plan.ModelSpec, error) {
	spec := &purserv1.ModelSpec{}
	if hasJSON(m.Spec) {
		if err := protojson.Unmarshal(m.Spec, spec); err != nil {
			return plan.ModelSpec{}, fmt.Errorf("planning: decode model spec for %q: %w", m.ID, err)
		}
	}
	ms := plan.ModelSpecFromProto(spec)
	if ms.ID == "" {
		ms.ID = m.ID
	}
	return ms, nil
}

// toFitError projects a planner error into the control-plane FitError when it
// is a *plan.PlanError; other errors pass through unchanged.
func toFitError(err error) error {
	var pe *plan.PlanError
	if errors.As(err, &pe) {
		return &FitError{Reason: pe.Reason, DeficitGB: pe.DeficitGB, Suggestions: pe.Suggestions}
	}
	return err
}

// hasJSON reports whether b holds a non-empty JSON object worth decoding.
func hasJSON(b []byte) bool {
	return len(b) > 0 && string(b) != "{}" && string(b) != "null"
}
