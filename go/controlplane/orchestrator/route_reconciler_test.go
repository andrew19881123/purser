package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/registry"
)

// --- test doubles ----------------------------------------------------------

// reconGateway is an in-memory fake Gateway that keeps the same
// `model_id -> route` table shape the real Gateway holds *in memory*, so a test
// can observe what a reconcile pass actually converged to. It implements
// orchestrator.RouteLister as well, standing in for `GET /api/v1/routes`.
type reconGateway struct {
	mu      sync.Mutex
	routes  map[string]orchestrator.RouteUpdate
	upserts int
	deletes []string
	down    bool
}

var _ orchestrator.RouteLister = (*reconGateway)(nil)

func newReconGateway() *reconGateway {
	return &reconGateway{routes: make(map[string]orchestrator.RouteUpdate)}
}

func (g *reconGateway) UpsertRoute(_ context.Context, u orchestrator.RouteUpdate) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down {
		return errGatewayDown
	}
	g.upserts++
	g.routes[u.ModelID] = u
	return nil
}

func (g *reconGateway) DeleteRoute(_ context.Context, modelID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down {
		return errGatewayDown
	}
	g.deletes = append(g.deletes, modelID)
	delete(g.routes, modelID)
	return nil
}

func (g *reconGateway) ListRoutes(_ context.Context) ([]orchestrator.RouteView, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down {
		return nil, errGatewayDown
	}
	out := make([]orchestrator.RouteView, 0, len(g.routes))
	for _, r := range g.routes {
		out = append(out, orchestrator.RouteView{
			ModelID:      r.ModelID,
			Endpoint:     r.Endpoint,
			DeploymentID: r.DeploymentID,
			Quantization: r.Quantization,
			State:        r.State,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out, nil
}

// seed places a route in the table without counting as a reconcile upsert.
func (g *reconGateway) seed(u orchestrator.RouteUpdate) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[u.ModelID] = u
}

// restart drops every route the way a Gateway process restart does: the table
// is in memory only, so it comes back empty.
func (g *reconGateway) restart() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes = make(map[string]orchestrator.RouteUpdate)
}

func (g *reconGateway) setDown(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.down = v
}

func (g *reconGateway) modelIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.routes))
	for id := range g.routes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (g *reconGateway) route(modelID string) (orchestrator.RouteUpdate, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.routes[modelID]
	return r, ok
}

func (g *reconGateway) deleted(modelID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range g.deletes {
		if d == modelID {
			return true
		}
	}
	return false
}

// reconPushOnlyGateway implements GatewaySync but *not* RouteLister, proving the
// reconciler still converges (via its own bookkeeping) against a Gateway that
// cannot be listed.
type reconPushOnlyGateway struct {
	mu      sync.Mutex
	routes  map[string]orchestrator.RouteUpdate
	deletes []string
}

var _ orchestrator.GatewaySync = (*reconPushOnlyGateway)(nil)

func newReconPushOnlyGateway() *reconPushOnlyGateway {
	return &reconPushOnlyGateway{routes: make(map[string]orchestrator.RouteUpdate)}
}

func (g *reconPushOnlyGateway) UpsertRoute(_ context.Context, u orchestrator.RouteUpdate) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[u.ModelID] = u
	return nil
}

func (g *reconPushOnlyGateway) DeleteRoute(_ context.Context, modelID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.routes, modelID)
	g.deletes = append(g.deletes, modelID)
	return nil
}

func (g *reconPushOnlyGateway) modelIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.routes))
	for id := range g.routes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

var errGatewayDown = errors.New("gateway unreachable")

// --- helpers ---------------------------------------------------------------

// createDeployment writes a deployment with an orchestrator-shaped Detail blob.
func createDeployment(t *testing.T, reg registry.Registry, id, modelID, endpoint, state string) {
	t.Helper()
	detail, err := json.Marshal(orchestrator.DeploymentDetail{
		ModelID:      modelID,
		Endpoint:     endpoint,
		Quantization: "q4",
	})
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if err := reg.CreateDeployment(context.Background(), &registry.Deployment{
		ID:      id,
		ModelID: modelID,
		State:   state,
		Detail:  detail,
	}); err != nil {
		t.Fatalf("create deployment %s: %v", id, err)
	}
}

// --- tests -----------------------------------------------------------------

// (a) happy path — every ACTIVE deployment is pushed, inactive ones are not.
func TestRouteReconciler_PushesAllActiveDeployments(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "m1", "http://10.0.0.1:8000", orchestrator.StateActive)
	createDeployment(t, reg, "dep-2", "m2", "http://10.0.0.2:8000", orchestrator.StateActive)
	createDeployment(t, reg, "dep-3", "m3", "http://10.0.0.3:8000", orchestrator.StateStopped)

	gw := newReconGateway()
	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)
	if err := rc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := gw.modelIDs()
	want := []string{"m1", "m2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("gateway routes = %v, want %v (STOPPED deployments must not be pushed)", got, want)
	}

	// The payload must carry the endpoint the orchestrator recorded, so the
	// gateway can actually reach the deployment host.
	r, ok := gw.route("m1")
	if !ok {
		t.Fatal("m1 route missing")
	}
	if r.Endpoint != "http://10.0.0.1:8000" {
		t.Errorf("endpoint = %q, want http://10.0.0.1:8000", r.Endpoint)
	}
	if r.State != "active" {
		t.Errorf("state = %q, want active", r.State)
	}
	if r.DeploymentID != "dep-1" {
		t.Errorf("deployment_id = %q, want dep-1", r.DeploymentID)
	}
}

// (b) THE REGRESSION TEST: a Gateway that restarted with an empty in-memory
// table must get its routes back on the next reconcile tick, with no operator
// action and no control-plane restart.
func TestRouteReconciler_RestoresRoutesAfterGatewayRestart(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "tinyllama-1b", "http://10.0.0.1:8000", orchestrator.StateActive)

	gw := newReconGateway()
	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)

	ctx := context.Background()
	if err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	if got := gw.modelIDs(); len(got) != 1 {
		t.Fatalf("initial routes = %v, want [tinyllama-1b]", got)
	}

	// The Gateway process restarts. Its table is in memory only, so it comes
	// back empty — this is exactly the production incident (503 model not
	// available, /v1/models empty) with no operator PUT to repair it.
	gw.restart()
	if got := gw.modelIDs(); len(got) != 0 {
		t.Fatalf("after restart routes = %v, want empty", got)
	}

	if err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after gateway restart: %v", err)
	}
	got := gw.modelIDs()
	if len(got) != 1 || got[0] != "tinyllama-1b" {
		t.Fatalf("routes after gateway restart = %v, want [tinyllama-1b] — "+
			"the route table did not self-heal", got)
	}
}

// (c) stale cleanup — a route whose model is no longer ACTIVE is deleted, not
// merely left alone.
func TestRouteReconciler_DeletesStaleRoutes(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "m1", "http://10.0.0.1:8000", orchestrator.StateActive)
	// m2 was active earlier in the process' life and is now torn down.
	createDeployment(t, reg, "dep-2", "m2", "http://10.0.0.2:8000", orchestrator.StateStopped)

	gw := newReconGateway()
	gw.seed(orchestrator.RouteUpdate{
		ModelID: "m2", Endpoint: "http://10.0.0.2:8000", DeploymentID: "dep-2", State: "active",
	})

	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)
	if err := rc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if _, ok := gw.route("m2"); ok {
		t.Error("route for the no-longer-ACTIVE model m2 is still on the gateway")
	}
	if !gw.deleted("m2") {
		t.Error("expected DeleteRoute(m2) to be called")
	}
	if _, ok := gw.route("m1"); !ok {
		t.Error("active route m1 was dropped")
	}
}

// An ACTIVE deployment that disappears from the Registry entirely (hard delete)
// is stale too — the observed gateway table, not the Registry, is the input for
// what needs removing.
func TestRouteReconciler_DeletesRoutesForUnknownModels(t *testing.T) {
	reg := openReg(t)
	gw := newReconGateway()
	gw.seed(orchestrator.RouteUpdate{ModelID: "ghost", Endpoint: "http://10.9.9.9:1", State: "active"})

	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)
	if err := rc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := gw.route("ghost"); ok {
		t.Error("route with no backing deployment was not cleaned up")
	}
}

// Fallback: a Gateway that cannot be listed (no RouteLister) still gets stale
// routes removed, using the reconciler's own last-pushed bookkeeping.
func TestRouteReconciler_StaleCleanupWithoutLister(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "m1", "http://10.0.0.1:8000", orchestrator.StateActive)

	gw := newReconPushOnlyGateway()
	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)
	ctx := context.Background()
	if err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if got := gw.modelIDs(); len(got) != 1 {
		t.Fatalf("routes = %v, want [m1]", got)
	}

	// m1 goes inactive, then the registry row is replaced with STOPPED.
	dep, err := reg.GetDeployment(ctx, "dep-1")
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	dep.State = orchestrator.StateStopped
	if err := reg.UpdateDeployment(ctx, dep); err != nil {
		t.Fatalf("update deployment: %v", err)
	}

	if err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := gw.modelIDs(); len(got) != 0 {
		t.Fatalf("routes = %v, want empty after the model went inactive", got)
	}
}

// (d) error path — an unreachable Gateway makes Reconcile return an error (so
// the loop can log and retry) but never panics, and the next pass recovers.
func TestRouteReconciler_GatewayDownThenRecovers(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "m1", "http://10.0.0.1:8000", orchestrator.StateActive)

	gw := newReconGateway()
	gw.setDown(true)

	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)
	ctx := context.Background()
	if err := rc.Reconcile(ctx); err == nil {
		t.Fatal("expected an error when the gateway is unreachable")
	}
	if got := gw.modelIDs(); len(got) != 0 {
		t.Fatalf("routes = %v, want empty while the gateway is down", got)
	}

	// The gateway comes back: the very next pass converges, with no restart.
	gw.setDown(false)
	if err := rc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after gateway recovered: %v", err)
	}
	if got := gw.modelIDs(); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("routes = %v, want [m1] after recovery", got)
	}
}

// Run pushes once immediately (control-plane startup) rather than sleeping for
// a whole interval first.
func TestRouteReconciler_RunReconcilesImmediately(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "m1", "http://10.0.0.1:8000", orchestrator.StateActive)

	gw := newReconGateway()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A very long interval: only the startup pass can populate the table.
	rc := orchestrator.NewRouteReconciler(reg, gw, time.Hour, nil)
	done := make(chan error, 1)
	go func() { done <- rc.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if got := gw.modelIDs(); len(got) == 1 && got[0] == "m1" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("startup reconcile did not push the route; routes = %v", gw.modelIDs())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// Run survives a Gateway outage: it keeps ticking, never returns early, and
// restores the routes on a later tick once the Gateway is reachable again.
func TestRouteReconciler_RunSurvivesGatewayOutage(t *testing.T) {
	reg := openReg(t)
	createDeployment(t, reg, "dep-1", "m1", "http://10.0.0.1:8000", orchestrator.StateActive)

	gw := newReconGateway()
	gw.setDown(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rc := orchestrator.NewRouteReconciler(reg, gw, 20*time.Millisecond, nil)
	done := make(chan error, 1)
	go func() { done <- rc.Run(ctx) }()

	// Several failing ticks must not terminate the loop.
	time.Sleep(80 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Run exited while the gateway was down: %v", err)
	default:
	}

	// Gateway returns; a later tick must converge on its own.
	gw.setDown(false)
	deadline := time.After(3 * time.Second)
	for {
		if got := gw.modelIDs(); len(got) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("route not restored after the gateway recovered; routes = %v", gw.modelIDs())
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}
