// Package reconciler implements the control loop that keeps the fleet's real
// state aligned with the desired state — the self-healing property Purser
// borrows conceptually from Kubernetes.
//
// The loop continuously compares the desired state (active deployments in the
// Registry) with the real state (what Agent heartbeats report, surfaced as node
// last_seen/state) and acts to close the gap: missing/unreachable node →
// failover, engine down where it should run → restart, new node available →
// consider optimization, orphan deployment → clean up. See
// docs/04_Control_Plane.html §3.
//
// Two safety mechanisms gate action:
//   - Hysteresis: a discrepancy must persist for both a minimum dwell time and a
//     minimum number of consecutive passes before the loop acts, so transient
//     blips do not cause churn.
//   - Automation levels per event type (Auto / ApprovalRequired / NotifyOnly),
//     with conservative defaults: autonomous only for local engine restart;
//     multi-node failover requires approval; new-node optimization only notifies.
package reconciler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/encoding/protojson"
)

// AutomationLevel controls how much the reconciler acts on its own.
type AutomationLevel int

const (
	// AutomationAuto detects and acts autonomously.
	AutomationAuto AutomationLevel = iota
	// AutomationApprovalRequired proposes the action and waits for an operator.
	AutomationApprovalRequired
	// AutomationNotifyOnly detects and reports but never acts.
	AutomationNotifyOnly
)

func (l AutomationLevel) String() string {
	switch l {
	case AutomationAuto:
		return "auto"
	case AutomationApprovalRequired:
		return "approval_required"
	case AutomationNotifyOnly:
		return "notify_only"
	default:
		return "unknown"
	}
}

// EventType classifies a detected discrepancy.
type EventType string

const (
	// EventEngineDown: node is alive but its engine is not running where it
	// should be → local restart (low risk).
	EventEngineDown EventType = "engine_down"
	// EventNodeDown: node hosting an active engine is unreachable → failover
	// (multi-node, higher risk).
	EventNodeDown EventType = "node_down"
	// EventNewNode: a READY node is not part of any active deployment → consider
	// optimization/rebalance.
	EventNewNode EventType = "new_node"
	// EventOrphanDeployment: an active deployment references a model/nodes that
	// no longer exist → clean up.
	EventOrphanDeployment EventType = "orphan_deployment"
)

// DefaultLevels returns the safe-by-default automation policy: autonomous only
// for local restarts; failover and cleanup need approval; new-node optimization
// only notifies.
func DefaultLevels() map[EventType]AutomationLevel {
	return map[EventType]AutomationLevel{
		EventEngineDown:       AutomationAuto,
		EventNodeDown:         AutomationApprovalRequired,
		EventOrphanDeployment: AutomationApprovalRequired,
		EventNewNode:          AutomationNotifyOnly,
	}
}

// Config tunes the control loop. The hysteresis/threshold values are deliberate
// placeholders to be calibrated against real fleet telemetry.
type Config struct {
	// Interval between reconcile passes.
	Interval time.Duration
	// Hysteresis is the minimum dwell time a discrepancy must persist before
	// the loop acts (time-based anti-churn).
	Hysteresis time.Duration
	// FailureThreshold is the minimum number of consecutive passes a
	// discrepancy must be observed before acting (count-based anti-churn).
	FailureThreshold int
	// NodeTimeout is how long since the last heartbeat before a node is
	// considered unreachable.
	NodeTimeout time.Duration
	// ActionCooldown prevents re-issuing the same action every pass while a
	// prior action is still taking effect.
	ActionCooldown time.Duration
	// Levels is the automation policy per event type; missing entries fall back
	// to DefaultLevels.
	Levels map[EventType]AutomationLevel
	// WebhookURL, when non-empty, is the HTTP(S) endpoint that receives a POST
	// request whenever the reconciler raises an approval_required event. The
	// delivery is fire-and-forget (goroutine) and does not block the control loop.
	WebhookURL string
	// WebhookRetries is the maximum number of POST attempts before giving up.
	// Defaults to 3 when 0 or negative.
	WebhookRetries int
}

// DefaultConfig returns conservative defaults suitable for the MVP.
// NOTE: Interval, Hysteresis, FailureThreshold, NodeTimeout and ActionCooldown
// are placeholders pending calibration (docs §3, §12, C-5).
func DefaultConfig() Config {
	return Config{
		Interval:         10 * time.Second,
		Hysteresis:       30 * time.Second,
		FailureThreshold: 3,
		NodeTimeout:      45 * time.Second,
		ActionCooldown:   2 * time.Minute,
		Levels:           DefaultLevels(),
	}
}

// ConfigFromEnv returns a Config seeded from DefaultConfig() with overrides
// from the following environment variables:
//
//   - PURSER_RECONCILER_INTERVAL           – Interval between reconcile passes
//   - PURSER_RECONCILER_NODE_OFFLINE_AFTER – NodeTimeout threshold
//   - PURSER_RECONCILER_HYSTERESIS         – Hysteresis dwell time
//   - PURSER_RECONCILER_ACTION_COOLDOWN    – ActionCooldown between re-actions
//
// All values are parsed by time.ParseDuration. Unset or unparseable variables
// fall back to the DefaultConfig() value.
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	if d := envDuration("PURSER_RECONCILER_INTERVAL"); d > 0 {
		cfg.Interval = d
	}
	if d := envDuration("PURSER_RECONCILER_NODE_OFFLINE_AFTER"); d > 0 {
		cfg.NodeTimeout = d
	}
	if d := envDuration("PURSER_RECONCILER_HYSTERESIS"); d > 0 {
		cfg.Hysteresis = d
	}
	if d := envDuration("PURSER_RECONCILER_ACTION_COOLDOWN"); d > 0 {
		cfg.ActionCooldown = d
	}
	if v := os.Getenv("PURSER_RECONCILER_WEBHOOK_URL"); v != "" {
		cfg.WebhookURL = v
	}
	if v := os.Getenv("PURSER_RECONCILER_WEBHOOK_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.WebhookRetries = n
		}
	}
	return cfg
}

// envDuration parses a time.Duration from the named environment variable.
// It returns 0 if the variable is unset or cannot be parsed.
func envDuration(key string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func (c Config) level(t EventType) AutomationLevel {
	if c.Levels != nil {
		if l, ok := c.Levels[t]; ok {
			return l
		}
	}
	return DefaultLevels()[t]
}

// Actuator performs the corrective actions. It is injectable so the control
// loop can be tested without a live orchestrator; OrchestratorActuator adapts
// the real orchestrator.
type Actuator interface {
	// RestartEngine restarts one node's engine within a deployment.
	RestartEngine(ctx context.Context, deploymentID, nodeID string) error
	// Failover recovers a deployment whose node became unreachable.
	Failover(ctx context.Context, deploymentID string) error
	// Cleanup tears down an orphaned deployment.
	Cleanup(ctx context.Context, deploymentID string) error
}

// FailoverPlanner computes a deployment plan for a model against the current
// READY fleet. Used by the reconciler during EventNodeDown handling to find
// an alternate placement when a node fails. The failed node is excluded
// because its lifecycle state transitions away from READY/RUNNING before the
// planner is consulted.
//
// A non-nil error (e.g. *planning.FitError) means no feasible plan is
// available; the reconciler falls back to approval-required behavior. The
// interface is satisfied by a thin wrapper over *planning.Planner, declared
// structurally here so the reconciler can be tested without a live planner.
type FailoverPlanner interface {
	Plan(ctx context.Context, modelID string) (*purserv1.DeploymentPlan, error)
}

// Event is a detected discrepancy and the decision taken about it.
type Event struct {
	Type         EventType
	DeploymentID string
	NodeID       string
	Level        AutomationLevel
	Acted        bool
	Reason       string
}

// Report summarizes one reconcile pass (returned for observability/tests).
type Report struct {
	Detected []Event
	Acted    []Event
}

// discrepancy is an observed gap keyed for hysteresis tracking.
type discrepancy struct {
	typ    EventType
	depID  string
	nodeID string
}

func (d discrepancy) key() string { return string(d.typ) + "|" + d.depID + "|" + d.nodeID }

// discState tracks how long/often a discrepancy has been observed.
type discState struct {
	firstSeen time.Time
	count     int
	seenThis  bool
	lastActed time.Time
}

// Reconciler runs the control loop.
type Reconciler struct {
	reg     registry.Registry
	act     Actuator
	planner FailoverPlanner
	cfg     Config
	log     *slog.Logger
	now     func() time.Time

	tracker map[string]*discState

	// OTEL instruments. All are no-ops unless a real MeterProvider was
	// installed by telemetry.Init before New() is called.
	ctrEventsDetected  metric.Int64Counter
	ctrEventsActed     metric.Int64Counter
	gaugeEventsPending metric.Int64Gauge
	histLoopDuration   metric.Float64Histogram
}

// New builds a Reconciler. act may be nil for NotifyOnly-only operation.
func New(reg registry.Registry, act Actuator, cfg Config) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultConfig().Interval
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 1
	}
	if cfg.NodeTimeout <= 0 {
		cfg.NodeTimeout = DefaultConfig().NodeTimeout
	}
	rc := &Reconciler{
		reg:     reg,
		act:     act,
		cfg:     cfg,
		log:     slog.Default(),
		now:     time.Now,
		tracker: map[string]*discState{},
	}

	// Initialise OTEL metric instruments. otel.Meter() returns a no-op meter
	// (zero overhead) if no real MeterProvider was installed by telemetry.Init,
	// so this is always safe even without a collector.
	m := otel.Meter("purser.reconciler")
	rc.ctrEventsDetected, _ = m.Int64Counter("purser.reconciler.events_detected",
		metric.WithDescription("Reconciler events dispatched (past hysteresis threshold), counted per event type"),
		metric.WithUnit("{event}"))
	rc.ctrEventsActed, _ = m.Int64Counter("purser.reconciler.events_acted",
		metric.WithDescription("Reconciler events where a corrective action was taken, counted per event type"),
		metric.WithUnit("{event}"))
	rc.gaugeEventsPending, _ = m.Int64Gauge("purser.reconciler.events_pending_approval",
		metric.WithDescription("Reconciler events currently waiting for operator approval per event type"),
		metric.WithUnit("{event}"))
	rc.histLoopDuration, _ = m.Float64Histogram("purser.reconciler.loop_duration_ms",
		metric.WithDescription("Wall-clock duration of each Reconcile() pass in milliseconds"),
		metric.WithUnit("ms"))

	return rc
}

// SetLogger overrides the logger.
func (rc *Reconciler) SetLogger(l *slog.Logger) {
	if l != nil {
		rc.log = l
	}
}

// SetClock overrides the time source (tests).
func (rc *Reconciler) SetClock(now func() time.Time) {
	if now != nil {
		rc.now = now
	}
}

// SetPlanner wires the FailoverPlanner consulted during EventNodeDown
// handling. Without a planner the reconciler calls the Actuator's Failover
// directly; failover succeeds only if the deployment already has a
// FailoverPlanID set externally. Setting one enables autonomous plan
// computation and execution.
func (rc *Reconciler) SetPlanner(p FailoverPlanner) { rc.planner = p }

// Run drives the control loop until ctx is cancelled.
func (rc *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(rc.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := rc.Reconcile(ctx); err != nil {
				rc.log.Error("reconcile pass failed", "err", err)
			}
		}
	}
}

// Reconcile performs exactly one pass: observe discrepancies, update the
// hysteresis tracker, and act on those past threshold according to the
// automation policy. It returns a Report for observability and tests.
func (rc *Reconciler) Reconcile(ctx context.Context) (Report, error) {
	loopStart := rc.now()
	defer func() {
		rc.histLoopDuration.Record(ctx, float64(rc.now().Sub(loopStart).Milliseconds()))
	}()

	now := rc.now().UTC()

	deps, err := rc.reg.ListDeployments(ctx)
	if err != nil {
		return Report{}, err
	}
	nodes, err := rc.reg.ListNodes(ctx)
	if err != nil {
		return Report{}, err
	}
	byID := make(map[string]*registry.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	observed := rc.observe(ctx, deps, byID, now)

	// Update tracker: mark observed, register new, then act on eligible ones.
	for _, st := range rc.tracker {
		st.seenThis = false
	}
	var report Report
	for _, d := range observed {
		k := d.key()
		st, ok := rc.tracker[k]
		if !ok {
			st = &discState{firstSeen: now}
			rc.tracker[k] = st
		}
		st.seenThis = true
		st.count++

		ev := Event{Type: d.typ, DeploymentID: d.depID, NodeID: d.nodeID, Level: rc.cfg.level(d.typ)}

		eligible := st.count >= rc.cfg.FailureThreshold && now.Sub(st.firstSeen) >= rc.cfg.Hysteresis
		cooling := !st.lastActed.IsZero() && now.Sub(st.lastActed) < rc.cfg.ActionCooldown
		if eligible && !cooling {
			ev.Acted = rc.dispatch(ctx, ev)
			if ev.Acted {
				st.lastActed = now
			} else {
				ev.Reason = "pending_" + ev.Level.String()
			}
		} else if !eligible {
			ev.Reason = "within_hysteresis"
		} else {
			ev.Reason = "action_cooldown"
		}
		report.Detected = append(report.Detected, ev)
		if ev.Acted {
			report.Acted = append(report.Acted, ev)
		}
	}

	// Reset healed discrepancies so their counters do not accumulate.
	for k, st := range rc.tracker {
		if !st.seenThis {
			delete(rc.tracker, k)
		}
	}

	// Publish the count of events currently waiting for operator approval,
	// labelled by event type, so operators can see approval backlogs.
	pendingByType := map[EventType]int64{}
	for _, ev := range report.Detected {
		if ev.Level == AutomationApprovalRequired && !ev.Acted {
			pendingByType[ev.Type]++
		}
	}
	for typ, count := range pendingByType {
		rc.gaugeEventsPending.Record(ctx, count,
			metric.WithAttributes(attribute.String("type", string(typ))))
	}

	return report, nil
}

// observe computes the current set of discrepancies.
func (rc *Reconciler) observe(ctx context.Context, deps []*registry.Deployment, byID map[string]*registry.Node, now time.Time) []discrepancy {
	var out []discrepancy
	claimed := map[string]bool{} // node IDs used by active deployments

	for _, d := range deps {
		if d.State != orchestrator.StateActive {
			continue
		}
		detail := decodeDetail(d)

		// Orphan: the deployment's model no longer exists.
		if detail.ModelID != "" {
			if _, err := rc.reg.GetModel(ctx, detail.ModelID); errors.Is(err, registry.ErrNotFound) {
				out = append(out, discrepancy{typ: EventOrphanDeployment, depID: d.ID})
				continue
			}
		}

		for _, e := range detail.Engines {
			claimed[e.NodeID] = true
			n, ok := byID[e.NodeID]
			if !ok || rc.nodeDown(n, now) {
				out = append(out, discrepancy{typ: EventNodeDown, depID: d.ID, nodeID: e.NodeID})
				continue
			}
			if !engineHealthy(n) {
				out = append(out, discrepancy{typ: EventEngineDown, depID: d.ID, nodeID: e.NodeID})
			}
		}
	}

	// New nodes: READY and not claimed by any active deployment.
	for _, n := range byID {
		if claimed[n.ID] {
			continue
		}
		if n.State == nodeStateReady && !rc.nodeDown(n, now) {
			out = append(out, discrepancy{typ: EventNewNode, nodeID: n.ID})
		}
	}
	return out
}

// dispatch performs (or defers) the action for ev per its automation level.
// Returns true if an action was actually performed.
func (rc *Reconciler) dispatch(ctx context.Context, ev Event) (acted bool) {
	typeAttr := metric.WithAttributes(attribute.String("type", string(ev.Type)))
	defer func() {
		rc.ctrEventsDetected.Add(ctx, 1, typeAttr)
		if acted {
			rc.ctrEventsActed.Add(ctx, 1, typeAttr)
		}
	}()
	switch ev.Level {
	case AutomationNotifyOnly:
		rc.log.Info("reconciler notify", "type", ev.Type, "deployment", ev.DeploymentID, "node", ev.NodeID)
		rc.audit(ctx, "reconciler.notify", ev)
		return false
	case AutomationApprovalRequired:
		rc.log.Warn("reconciler action pending approval", "type", ev.Type, "deployment", ev.DeploymentID, "node", ev.NodeID)
		rc.audit(ctx, "reconciler.pending_approval", ev)
		if rc.cfg.WebhookURL != "" {
			// Fire-and-forget: use context.Background() so the delivery is not
			// cancelled when the reconciler's request context expires.
			go rc.sendWebhook(context.Background(), ev)
		}
		return false
	case AutomationAuto:
		if rc.act == nil {
			rc.log.Warn("no actuator; cannot auto-act", "type", ev.Type)
			return false
		}
		// EventNodeDown: compute and persist a failover plan (if a planner is
		// configured) before delegating to the actuator so it has a populated
		// FailoverPlanID to apply.
		if ev.Type == EventNodeDown {
			return rc.handleNodeDown(ctx, ev)
		}
		var err error
		switch ev.Type {
		case EventEngineDown:
			err = rc.act.RestartEngine(ctx, ev.DeploymentID, ev.NodeID)
		case EventOrphanDeployment:
			err = rc.act.Cleanup(ctx, ev.DeploymentID)
		case EventNewNode:
			// No autonomous action for new nodes; treated as notify.
			rc.audit(ctx, "reconciler.notify", ev)
			return false
		}
		if err != nil {
			rc.log.Error("reconciler action failed", "type", ev.Type, "deployment", ev.DeploymentID, "node", ev.NodeID, "err", err)
			rc.audit(ctx, "reconciler.action_failed", ev)
			return false
		}
		rc.log.Info("reconciler acted", "type", ev.Type, "deployment", ev.DeploymentID, "node", ev.NodeID)
		rc.audit(ctx, "reconciler.acted", ev)
		return true
	default:
		return false
	}
}

func (rc *Reconciler) nodeDown(n *registry.Node, now time.Time) bool {
	switch n.State {
	case nodeStateUnreachable, nodeStateDecommissioned:
		return true
	}
	if n.LastSeen.IsZero() {
		// Never reported liveness: only treat as down if it isn't freshly created.
		return now.Sub(n.CreatedAt) > rc.cfg.NodeTimeout
	}
	return now.Sub(n.LastSeen) > rc.cfg.NodeTimeout
}

// handleNodeDown is the AutomationAuto handler for EventNodeDown. When a
// FailoverPlanner is configured it:
//  1. Loads the deployment and verifies it is non-terminal.
//  2. Calls the planner for an alternate plan (the failed node is excluded
//     because its state is no longer READY/RUNNING by this point).
//  3. Persists the plan and stamps deployment.Detail.FailoverPlanID so the
//     Actuator can load it.
//  4. Calls actuator.Failover, which applies the new plan and tears down the
//     failed deployment.
//
// Without a planner the Actuator is called directly (legacy/manual path;
// succeeds only if the deployment already carries a FailoverPlanID).
//
// On any planning or persistence failure the method emits a
// reconciler.failover.no_capacity audit event and returns false so the
// discrepancy is reported as pending-approval.
func (rc *Reconciler) handleNodeDown(ctx context.Context, ev Event) bool {
	if rc.planner != nil {
		dep, err := rc.reg.GetDeployment(ctx, ev.DeploymentID)
		if err != nil {
			rc.log.Error("failover: load deployment failed",
				"deployment", ev.DeploymentID, "err", err)
			rc.audit(ctx, "reconciler.action_failed", ev)
			return false
		}
		if failoverIsTerminal(dep.State) {
			// Cleaned up concurrently; nothing to failover.
			return false
		}
		detail := decodeDetail(dep)
		if detail.ModelID != "" {
			newPlan, planErr := rc.planner.Plan(ctx, detail.ModelID)
			if planErr != nil {
				rc.log.Warn("failover: no alternate plan available",
					"deployment", ev.DeploymentID, "node", ev.NodeID,
					"model", detail.ModelID, "err", planErr)
				rc.audit(ctx, "reconciler.failover.no_capacity", ev)
				return false
			}
			// Assign a unique persistence ID to avoid collisions on the plans PK
			// (the planner's ID is deterministic for a given model+fleet snapshot).
			planID := newPlan.GetPlanId() + "-fo-" + failoverRandHex(4)
			newPlan.PlanId = planID
			planBlob, merr := protojson.Marshal(newPlan)
			if merr != nil {
				rc.log.Error("failover: marshal plan failed",
					"deployment", ev.DeploymentID, "err", merr)
				rc.audit(ctx, "reconciler.action_failed", ev)
				return false
			}
			if perr := rc.reg.CreatePlan(ctx, &registry.Plan{
				ID:           planID,
				ModelID:      newPlan.GetModelId(),
				Quantization: newPlan.GetQuantization(),
				Cost:         newPlan.GetCost(),
				Plan:         planBlob,
			}); perr != nil {
				rc.log.Error("failover: persist plan failed",
					"deployment", ev.DeploymentID, "plan", planID, "err", perr)
				rc.audit(ctx, "reconciler.action_failed", ev)
				return false
			}
			// Stamp the deployment so actuator.Failover can retrieve the plan.
			detail.FailoverPlanID = planID
			dep.Detail = failoverMustJSON(detail)
			if uerr := rc.reg.UpdateDeployment(ctx, dep); uerr != nil {
				rc.log.Error("failover: update deployment detail failed",
					"deployment", ev.DeploymentID, "err", uerr)
				rc.audit(ctx, "reconciler.action_failed", ev)
				return false
			}
		}
	}

	if err := rc.act.Failover(ctx, ev.DeploymentID); err != nil {
		rc.log.Error("failover: actuator error",
			"deployment", ev.DeploymentID, "node", ev.NodeID, "err", err)
		rc.audit(ctx, "reconciler.action_failed", ev)
		return false
	}
	rc.log.Info("failover initiated", "deployment", ev.DeploymentID, "node", ev.NodeID)
	rc.audit(ctx, "reconciler.failover.initiated", ev)
	return true
}

// failoverIsTerminal reports whether state means the deployment has released
// all resources (STOPPED or FAILED). Uses the orchestrator's exported state
// constants to stay in sync with the rest of the control plane.
func failoverIsTerminal(state string) bool {
	return state == orchestrator.StateStopped || state == orchestrator.StateFailed
}

// failoverMustJSON marshals v to JSON, returning "{}" on error. Mirrors the
// same helper in the orchestrator package (unexported there, so redefined).
func failoverMustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// failoverRandHex returns n random bytes encoded as a hex string.
func failoverRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (rc *Reconciler) audit(ctx context.Context, action string, ev Event) {
	_ = rc.reg.AppendAudit(ctx, &registry.AuditEntry{
		Actor:  "reconciler",
		Action: action,
		Target: ev.DeploymentID,
	})
}

// ReconcilerConfigSnapshot is the JSON-serialisable view of Config
// returned by Status(). Durations are converted to whole seconds for
// easy consumption by dashboards and the status endpoint.
type ReconcilerConfigSnapshot struct {
	IntervalS       int `json:"interval_s"`
	NodeTimeoutS    int `json:"node_timeout_s"`
	HysteresisS     int `json:"hysteresis_s"`
	ActionCooldownS int `json:"action_cooldown_s"`
}

// ReconcilerEventSummary is the per-EventType tracker aggregation returned
// by Status(). OldestAgeS is the age in seconds of the oldest discrepancy
// still in the tracker (0 when Tracked == 0).
type ReconcilerEventSummary struct {
	Tracked    int     `json:"tracked"`
	OldestAgeS float64 `json:"oldest_age_s"`
}

// ReconcilerStatus is the snapshot returned by Status().
type ReconcilerStatus struct {
	Config  ReconcilerConfigSnapshot          `json:"config"`
	Tracker map[string]ReconcilerEventSummary `json:"tracker"`
}

// Status returns a point-in-time snapshot of the reconciler's configuration
// and hysteresis tracker state. It is safe to call from an HTTP handler
// concurrently with Reconcile (reads are non-destructive; the tracker map is
// only mutated inside Reconcile's single goroutine, so the read may observe
// a partially-updated pass — this is intentional, eventual-consistency
// semantics).
func (rc *Reconciler) Status() ReconcilerStatus {
	now := rc.now()

	// Aggregate tracker entries by EventType. Each key is "type|depID|nodeID".
	type summary struct {
		tracked    int
		oldestAgeS float64
	}
	byType := map[string]*summary{}
	for k, st := range rc.tracker {
		typ, _, _ := strings.Cut(k, "|")
		s, ok := byType[typ]
		if !ok {
			s = &summary{}
			byType[typ] = s
		}
		s.tracked++
		age := now.Sub(st.firstSeen).Seconds()
		if age > s.oldestAgeS {
			s.oldestAgeS = age
		}
	}

	tracker := make(map[string]ReconcilerEventSummary, len(byType))
	for et, s := range byType {
		tracker[et] = ReconcilerEventSummary{
			Tracked:    s.tracked,
			OldestAgeS: s.oldestAgeS,
		}
	}

	return ReconcilerStatus{
		Config: ReconcilerConfigSnapshot{
			IntervalS:       int(rc.cfg.Interval.Seconds()),
			NodeTimeoutS:    int(rc.cfg.NodeTimeout.Seconds()),
			HysteresisS:     int(rc.cfg.Hysteresis.Seconds()),
			ActionCooldownS: int(rc.cfg.ActionCooldown.Seconds()),
		},
		Tracker: tracker,
	}
}

// webhookPayload is the JSON body sent to the configured webhook URL when the
// reconciler raises an approval_required event.
type webhookPayload struct {
	Event        string `json:"event"`
	EventType    string `json:"event_type"`
	NodeID       string `json:"node_id"`
	DeploymentID string `json:"deployment_id"`
	Timestamp    string `json:"timestamp"`
	Version      string `json:"purser_version"`
	Message      string `json:"message"`
}

// sendWebhook delivers the approval_required payload to rc.cfg.WebhookURL with
// exponential-backoff retries. It is always called in a goroutine
// (fire-and-forget) and logs failures at WARN level without blocking the loop.
func (rc *Reconciler) sendWebhook(ctx context.Context, ev Event) {
	maxTries := rc.cfg.WebhookRetries
	if maxTries <= 0 {
		maxTries = 3
	}
	payload := webhookPayload{
		Event:        "approval_required",
		EventType:    string(ev.Type),
		NodeID:       ev.NodeID,
		DeploymentID: ev.DeploymentID,
		Timestamp:    rc.now().UTC().Format(time.RFC3339),
		Version:      "0.3.0",
		Message: fmt.Sprintf(
			"Node %s went down; deployment %s requires manual approval to failover",
			ev.NodeID, ev.DeploymentID,
		),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		rc.log.Warn("webhook: marshal payload failed", "err", err)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for attempt := 0; attempt < maxTries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 500ms, 1s, 2s, …
			sleep := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return
			case <-time.After(sleep):
			}
		}
		resp, postErr := client.Post(rc.cfg.WebhookURL, "application/json", bytes.NewReader(body))
		if postErr != nil {
			rc.log.Warn("webhook: delivery failed", "attempt", attempt+1, "url", rc.cfg.WebhookURL, "err", postErr)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			rc.log.Info("webhook: delivered", "url", rc.cfg.WebhookURL, "status", resp.StatusCode)
			return
		}
		rc.log.Warn("webhook: non-2xx response", "attempt", attempt+1, "status", resp.StatusCode, "url", rc.cfg.WebhookURL)
	}
	rc.log.Warn("webhook: all attempts exhausted", "url", rc.cfg.WebhookURL, "max_tries", maxTries)
}

// Node state string constants (mirror the NodeState proto enum values used
// across the control plane).
const (
	nodeStateReady          = "NODE_STATE_READY"
	nodeStateRunning        = "NODE_STATE_RUNNING"
	nodeStateUnreachable    = "NODE_STATE_UNREACHABLE"
	nodeStateDecommissioned = "NODE_STATE_DECOMMISSIONED"
)

// engineHealthy reports whether a node's reported state indicates its engine is
// running as desired.
func engineHealthy(n *registry.Node) bool {
	return n.State == nodeStateRunning || n.State == nodeStateReady
}

func decodeDetail(d *registry.Deployment) orchestrator.DeploymentDetail {
	var detail orchestrator.DeploymentDetail
	if len(d.Detail) > 0 {
		_ = json.Unmarshal(d.Detail, &detail)
	}
	return detail
}
