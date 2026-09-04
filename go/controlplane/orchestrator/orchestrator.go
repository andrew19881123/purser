// Package orchestrator is the Orchestration Controller: it translates a
// planner-produced DeploymentPlan into concrete actions on the Agents.
//
// The choreography is order-sensitive — the pipeline has ordering
// dependencies: every WORKER node's engine must reach READY before the HOST
// (coordinator) engine is started, because the host connects to already-ready
// workers. See docs/04_Control_Plane.html §2.
//
// Apply drives the sequence, enforces per-step READY timeouts, rolls back
// partial progress on failure, records the resulting deployment in the Registry
// and (best-effort) publishes the model→host route to the Gateway. Teardown
// reverses it. All Agent I/O goes through the injectable AgentClient so the
// logic is testable against simulated agents.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// Deployment lifecycle states, mirrored from the DeploymentState proto enum so
// the wire and the Registry use identical string values.
var (
	StateProvisioning = purserv1.DeploymentState_DEPLOYMENT_STATE_PROVISIONING.String()
	StateActive       = purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String()
	StateRebalancing  = purserv1.DeploymentState_DEPLOYMENT_STATE_REBALANCING.String()
	StateStopping     = purserv1.DeploymentState_DEPLOYMENT_STATE_STOPPING.String()
	StateStopped      = purserv1.DeploymentState_DEPLOYMENT_STATE_STOPPED.String()
	StateFailed       = purserv1.DeploymentState_DEPLOYMENT_STATE_FAILED.String()
)

// DefaultStepTimeout bounds how long a single engine may take to reach READY.
const DefaultStepTimeout = 2 * time.Minute

// Config tunes the orchestrator.
type Config struct {
	// StepTimeout is the per-engine READY deadline.
	StepTimeout time.Duration
	// Logger; a default is used if nil.
	Logger *slog.Logger
}

// Deps are the injectable collaborators of an Orchestrator.
type Deps struct {
	Agents   AgentClient
	Resolver Resolver
	Gateway  GatewaySync
	Config   Config
}

// Orchestrator applies deployment plans by commanding Agents and records the
// resulting state in the Registry.
type Orchestrator struct {
	reg      registry.Registry
	agents   AgentClient
	resolver Resolver
	gateway  GatewaySync
	stepTO   time.Duration
	log      *slog.Logger
}

// EngineRef records a started engine so it can be stopped later (teardown /
// rollback). It is persisted inside DeploymentDetail.
type EngineRef struct {
	NodeID    string `json:"node_id"`
	AgentAddr string `json:"agent_addr"`
	Role      string `json:"role"`
	Handle    string `json:"handle"`
}

// DeploymentDetail is the orchestrator's view persisted in Deployment.Detail. It
// is the source of truth the reconciler and Teardown read back.
type DeploymentDetail struct {
	ModelID      string      `json:"model_id"`
	Quantization string      `json:"quantization"`
	HostNodeID   string      `json:"host_node_id"`
	Endpoint     string      `json:"endpoint"` // http://<host_ip>:<port>
	Engines      []EngineRef `json:"engines"`
	// FailoverPlanID references an alternate plan the reconciler may apply on
	// node loss; empty means no automatic failover plan is available.
	FailoverPlanID string `json:"failover_plan_id,omitempty"`
	// Error carries the failure reason when the deployment is FAILED.
	Error string `json:"error,omitempty"`
}

// New returns an Orchestrator bound to a registry and its collaborators.
func New(reg registry.Registry, d Deps) *Orchestrator {
	log := d.Config.Logger
	if log == nil {
		log = slog.Default()
	}
	to := d.Config.StepTimeout
	if to <= 0 {
		to = DefaultStepTimeout
	}
	gw := d.Gateway
	if gw == nil {
		gw = NopGatewaySync{}
	}
	res := d.Resolver
	if res == nil {
		res = NewRegistryResolver(reg, 0, 0)
	}
	return &Orchestrator{
		reg:      reg,
		agents:   d.Agents,
		resolver: res,
		gateway:  gw,
		stepTO:   to,
		log:      log,
	}
}

// Apply realizes a DeploymentPlan on the fleet, returning the deployment ID.
//
// Order (mandatory): every WORKER engine is started and must reach READY before
// the single HOST engine is started with the workers as peers. On any failure
// or timeout the partial progress is rolled back (StopEngine in reverse) and the
// deployment is marked FAILED. On success the deployment is ACTIVE and the route
// is published to the Gateway (best-effort).
func (o *Orchestrator) Apply(ctx context.Context, plan *purserv1.DeploymentPlan) (string, error) {
	if o.agents == nil {
		return "", errors.New("orchestrator: no AgentClient configured")
	}
	if plan == nil {
		return "", errors.New("orchestrator: nil plan")
	}
	workers, host, err := partition(plan)
	if err != nil {
		return "", err
	}

	depID := newID("dep")
	dep := &registry.Deployment{
		ID:      depID,
		ModelID: plan.GetModelId(),
		PlanID:  plan.GetPlanId(),
		State:   StateProvisioning,
	}
	detail := &DeploymentDetail{ModelID: plan.GetModelId(), Quantization: plan.GetQuantization()}
	dep.Detail = mustJSON(detail)
	if err := o.reg.CreateDeployment(ctx, dep); err != nil {
		return "", fmt.Errorf("orchestrator: create deployment: %w", err)
	}
	o.audit(ctx, "deployment.provisioning", depID, map[string]any{"model_id": plan.GetModelId(), "plan_id": plan.GetPlanId()})

	var started []EngineRef
	var peerAddrs []string

	fail := func(reason string, cause error) (string, error) {
		o.log.Error("deployment failed, rolling back", "deployment", depID, "reason", reason, "err", cause)
		o.rollback(started)
		detail.Error = reason
		detail.Engines = started
		o.persist(ctx, dep, StateFailed, detail)
		o.audit(ctx, "deployment.failed", depID, map[string]any{"reason": reason})
		return depID, fmt.Errorf("orchestrator: deployment %s failed: %s: %w", depID, reason, cause)
	}

	// 1. Workers first.
	for _, w := range workers {
		ep, err := o.resolver.Resolve(ctx, w.GetNodeId())
		if err != nil {
			return fail("resolve worker "+w.GetNodeId(), err)
		}
		req := &purserv1.StartEngineRequest{
			ModelRef:     plan.GetModelId(),
			Role:         purserv1.Role_ROLE_WORKER,
			LayerStart:   w.GetLayerStart(),
			LayerEnd:     w.GetLayerEnd(),
			Quantization: plan.GetQuantization(),
			Draft:        w.GetDraft(),
		}
		handle, err := o.startAndWaitReady(ctx, ep.AgentAddr, req)
		if err != nil {
			return fail("worker "+w.GetNodeId()+" not ready", err)
		}
		started = append(started, EngineRef{NodeID: w.GetNodeId(), AgentAddr: ep.AgentAddr, Role: "worker", Handle: handle})
		peerAddrs = append(peerAddrs, ep.InferenceAddr)
		o.log.Info("worker engine ready", "deployment", depID, "node", w.GetNodeId(), "addr", ep.InferenceAddr)
	}

	// 2. Host last, with the workers as peers.
	ep, err := o.resolver.Resolve(ctx, host.GetNodeId())
	if err != nil {
		return fail("resolve host "+host.GetNodeId(), err)
	}
	hostReq := &purserv1.StartEngineRequest{
		ModelRef:     plan.GetModelId(),
		Role:         purserv1.Role_ROLE_HOST,
		LayerStart:   host.GetLayerStart(),
		LayerEnd:     host.GetLayerEnd(),
		Peers:        peerAddrs,
		Quantization: plan.GetQuantization(),
		Draft:        host.GetDraft(),
	}
	hostHandle, err := o.startAndWaitReady(ctx, ep.AgentAddr, hostReq)
	if err != nil {
		return fail("host "+host.GetNodeId()+" not ready", err)
	}
	started = append(started, EngineRef{NodeID: host.GetNodeId(), AgentAddr: ep.AgentAddr, Role: "host", Handle: hostHandle})

	// 3. Mark ACTIVE.
	detail.HostNodeID = host.GetNodeId()
	detail.Endpoint = "http://" + ep.InferenceAddr
	detail.Engines = started
	o.persist(ctx, dep, StateActive, detail)
	o.audit(ctx, "deployment.active", depID, map[string]any{"host": host.GetNodeId(), "endpoint": detail.Endpoint})
	o.log.Info("deployment active", "deployment", depID, "endpoint", detail.Endpoint)

	// 4. Publish route to the Gateway (best-effort; never fails the deployment).
	o.notifyGatewayUp(ctx, depID, detail)

	return depID, nil
}

// Teardown stops the engines of a deployment in reverse order (host→workers),
// removes its Gateway route and marks it STOPPED.
func (o *Orchestrator) Teardown(ctx context.Context, deploymentID string) error {
	dep, err := o.reg.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("orchestrator: teardown: %w", err)
	}
	detail := &DeploymentDetail{}
	if len(dep.Detail) > 0 {
		_ = json.Unmarshal(dep.Detail, detail)
	}
	o.persist(ctx, dep, StateStopping, detail)
	o.audit(ctx, "deployment.stopping", deploymentID, nil)

	// Stop engines in reverse order (host is last in Engines, so reverse puts it
	// first).
	o.rollback(detail.Engines)

	// Remove the Gateway route (best-effort).
	if detail.ModelID != "" {
		if err := o.gateway.DeleteRoute(ctx, detail.ModelID); err != nil {
			o.log.Warn("gateway route delete failed (best-effort)", "deployment", deploymentID, "model", detail.ModelID, "err", err)
		}
	}

	o.persist(ctx, dep, StateStopped, detail)
	o.audit(ctx, "deployment.stopped", deploymentID, nil)
	return nil
}

// RestartEngine re-issues StartEngine for a single node of an active deployment
// (used by the reconciler for autonomous local restarts). It waits for READY
// and updates the stored handle.
func (o *Orchestrator) RestartEngine(ctx context.Context, deploymentID, nodeID string) error {
	dep, err := o.reg.GetDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("orchestrator: restart: %w", err)
	}
	detail := &DeploymentDetail{}
	if len(dep.Detail) > 0 {
		_ = json.Unmarshal(dep.Detail, detail)
	}
	idx := -1
	for i := range detail.Engines {
		if detail.Engines[i].NodeID == nodeID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("orchestrator: node %q not part of deployment %q", nodeID, deploymentID)
	}
	ref := detail.Engines[idx]
	role := purserv1.Role_ROLE_WORKER
	if ref.Role == "host" {
		role = purserv1.Role_ROLE_HOST
	}
	req := &purserv1.StartEngineRequest{
		ModelRef:     detail.ModelID,
		Role:         role,
		Quantization: detail.Quantization,
	}
	handle, err := o.startAndWaitReady(ctx, ref.AgentAddr, req)
	if err != nil {
		return fmt.Errorf("orchestrator: restart node %q: %w", nodeID, err)
	}
	detail.Engines[idx].Handle = handle
	o.persist(ctx, dep, StateActive, detail)
	o.audit(ctx, "engine.restarted", deploymentID, map[string]any{"node": nodeID})
	return nil
}

// startAndWaitReady opens StartEngine and reads events until READY (returning
// the engine handle from the event Detail), ERROR, or the step timeout.
func (o *Orchestrator) startAndWaitReady(ctx context.Context, addr string, req *purserv1.StartEngineRequest) (string, error) {
	stepCtx, cancel := context.WithTimeout(ctx, o.stepTO)
	defer cancel()

	stream, err := o.agents.StartEngine(stepCtx, addr, req)
	if err != nil {
		return "", fmt.Errorf("start engine on %s: %w", addr, err)
	}

	type result struct {
		handle string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					done <- result{"", errors.New("engine stream closed before READY")}
					return
				}
				done <- result{"", fmt.Errorf("engine stream error: %w", err)}
				return
			}
			switch ev.GetKind() {
			case purserv1.EngineEventKind_ENGINE_EVENT_KIND_READY:
				done <- result{ev.GetDetail(), nil}
				return
			case purserv1.EngineEventKind_ENGINE_EVENT_KIND_ERROR:
				done <- result{"", fmt.Errorf("engine reported error: %s", ev.GetDetail())}
				return
			default:
				// LOADING / METRICS / UNSPECIFIED: keep waiting.
			}
		}
	}()

	select {
	case <-stepCtx.Done():
		return "", fmt.Errorf("timed out waiting for READY from %s: %w", addr, stepCtx.Err())
	case r := <-done:
		return r.handle, r.err
	}
}

// rollback stops the given engines in reverse order, best-effort.
func (o *Orchestrator) rollback(engines []EngineRef) {
	for i := len(engines) - 1; i >= 0; i-- {
		e := engines[i]
		sctx, cancel := context.WithTimeout(context.Background(), o.stepTO)
		if _, err := o.agents.StopEngine(sctx, e.AgentAddr, &purserv1.StopEngineRequest{Handle: e.Handle}); err != nil {
			o.log.Warn("stop engine failed during rollback/teardown", "node", e.NodeID, "handle", e.Handle, "err", err)
		}
		cancel()
	}
}

// notifyGatewayUp publishes the active route to the Gateway. Best-effort: a
// failure is logged but never fails the deployment.
func (o *Orchestrator) notifyGatewayUp(ctx context.Context, depID string, detail *DeploymentDetail) {
	err := o.gateway.UpsertRoute(ctx, RouteUpdate{
		ModelID:      detail.ModelID,
		Endpoint:     detail.Endpoint,
		DeploymentID: depID,
		Quantization: detail.Quantization,
		State:        "active",
	})
	if err != nil {
		o.log.Warn("gateway route publish failed (best-effort)", "deployment", depID, "model", detail.ModelID, "err", err)
	}
}

// persist writes the deployment state + detail back to the registry.
func (o *Orchestrator) persist(ctx context.Context, dep *registry.Deployment, state string, detail *DeploymentDetail) {
	dep.State = state
	dep.Detail = mustJSON(detail)
	if err := o.reg.UpdateDeployment(ctx, dep); err != nil {
		o.log.Error("persist deployment failed", "deployment", dep.ID, "state", state, "err", err)
	}
}

func (o *Orchestrator) audit(ctx context.Context, action, target string, details map[string]any) {
	var raw json.RawMessage
	if details != nil {
		raw = mustJSON(details)
	}
	if err := o.reg.AppendAudit(ctx, &registry.AuditEntry{
		Actor:   "orchestrator",
		Action:  action,
		Target:  target,
		Details: raw,
	}); err != nil {
		o.log.Warn("append audit failed", "action", action, "err", err)
	}
}

// partition splits a plan's assignments into workers and the single host.
func partition(plan *purserv1.DeploymentPlan) (workers []*purserv1.Assignment, host *purserv1.Assignment, err error) {
	for _, a := range plan.GetAssignments() {
		switch a.GetRole() {
		case purserv1.Role_ROLE_WORKER:
			workers = append(workers, a)
		case purserv1.Role_ROLE_HOST:
			if host != nil {
				return nil, nil, errors.New("orchestrator: plan has more than one HOST assignment")
			}
			host = a
		default:
			return nil, nil, fmt.Errorf("orchestrator: assignment for node %q has unspecified role", a.GetNodeId())
		}
	}
	if host == nil {
		return nil, nil, errors.New("orchestrator: plan has no HOST assignment")
	}
	return workers, host, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}
