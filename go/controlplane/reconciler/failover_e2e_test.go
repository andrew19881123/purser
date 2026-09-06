package reconciler_test

// TestFailoverExecutionEndToEnd is the regression test for E6: it exercises
// OrchestratorActuator.Failover() end-to-end against a real orchestrator backed
// by mock agent / resolver / gateway collaborators.
//
// Verified:
//  1. When a deployment's Detail already carries a FailoverPlanID, Failover
//     does NOT return "no alternate plan" — it loads the plan from the registry.
//  2. The orchestrator's Apply() is invoked for the new plan, creating an ACTIVE
//     deployment on the spare node.
//  3. The orchestrator's Teardown() is invoked for the failing deployment,
//     transitioning it to STOPPED.
//
// This test complements the unit tests in failover_test.go (which mock the
// Actuator) by exercising the real OrchestratorActuator code path.

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// --- test doubles for the orchestrator collaborators -----------------------

// e2eStream returns a scripted ENGINE_EVENT sequence that reaches READY
// immediately, simulating a fast engine start in tests.
type e2eStream struct {
	events []*purserv1.EngineEvent
	idx    int
}

func (s *e2eStream) Recv() (*purserv1.EngineEvent, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	return nil, io.EOF
}

// e2eAgentClient is a mock AgentClient whose StartEngine always returns a
// scripted LOADING→READY stream, and whose StopEngine always succeeds.
type e2eAgentClient struct {
	mu     sync.Mutex
	starts []string // node addresses that received StartEngine
	stops  []string // handles that received StopEngine
}

func (a *e2eAgentClient) StartEngine(_ context.Context, addr string, _ *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
	a.mu.Lock()
	a.starts = append(a.starts, addr)
	a.mu.Unlock()
	return &e2eStream{events: []*purserv1.EngineEvent{
		{Kind: purserv1.EngineEventKind_ENGINE_EVENT_KIND_LOADING, Detail: "loading"},
		{Kind: purserv1.EngineEventKind_ENGINE_EVENT_KIND_READY, Detail: "handle-e2e"},
	}}, nil
}

func (a *e2eAgentClient) StopEngine(_ context.Context, _ string, req *purserv1.StopEngineRequest) (*purserv1.StopReply, error) {
	a.mu.Lock()
	a.stops = append(a.stops, req.GetHandle())
	a.mu.Unlock()
	return &purserv1.StopReply{Status: "stopped"}, nil
}

// --- the end-to-end test ---------------------------------------------------

func TestFailoverExecutionEndToEnd(t *testing.T) {
	reg := openReg(t)
	ctx := context.Background()

	// Seed the model that the deployment references.
	if err := reg.CreateModel(ctx, &registry.Model{ID: "m-e2e"}); err != nil {
		t.Fatalf("create model: %v", err)
	}

	// Build the failover plan that the actuator will apply.
	foplan := &purserv1.DeploymentPlan{
		PlanId:  "fo-plan-e2e",
		ModelId: "m-e2e",
		Assignments: []*purserv1.Assignment{
			{NodeId: "spare-node", Role: purserv1.Role_ROLE_HOST},
		},
	}
	planBlob, err := protojson.Marshal(foplan)
	if err != nil {
		t.Fatalf("marshal failover plan: %v", err)
	}
	if err := reg.CreatePlan(ctx, &registry.Plan{
		ID:      "fo-plan-e2e",
		ModelID: "m-e2e",
		Plan:    planBlob,
	}); err != nil {
		t.Fatalf("create failover plan: %v", err)
	}

	// Create the original (failing) deployment with FailoverPlanID stamped in
	// its Detail — exactly what reconciler.handleNodeDown produces before
	// calling actuator.Failover.
	originalDetail := orchestrator.DeploymentDetail{
		ModelID:        "m-e2e",
		FailoverPlanID: "fo-plan-e2e",
		Engines: []orchestrator.EngineRef{
			{NodeID: "failing-node", AgentAddr: "failing:50151", Role: "host", Handle: "h-orig"},
		},
	}
	detailBlob, err := json.Marshal(originalDetail)
	if err != nil {
		t.Fatalf("marshal original detail: %v", err)
	}
	origDep := &registry.Deployment{
		ID:      "dep-failing-e2e",
		ModelID: "m-e2e",
		PlanID:  "plan-orig",
		State:   orchestrator.StateActive,
		Detail:  detailBlob,
	}
	if err := reg.CreateDeployment(ctx, origDep); err != nil {
		t.Fatalf("create original deployment: %v", err)
	}

	// Build a real orchestrator with mock collaborators.
	agent := &e2eAgentClient{}
	resolver := orchestrator.MapResolver{
		"spare-node": orchestrator.Endpoint{
			AgentAddr:     "spare:50151",
			InferenceAddr: "spare:8000",
		},
		// failing-node must be resolvable for Teardown's rollback to call StopEngine.
		"failing-node": orchestrator.Endpoint{
			AgentAddr:     "failing:50151",
			InferenceAddr: "failing:8000",
		},
	}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents:   agent,
		Resolver: resolver,
		// Gateway: nil → NopGatewaySync used automatically by orchestrator.New.
	})

	// Build the actuator (the real production adapter) and run Failover.
	act := reconciler.NewOrchestratorActuator(orch, reg)
	if err := act.Failover(ctx, "dep-failing-e2e"); err != nil {
		t.Fatalf("Failover returned error: %v\n"+
			"  (expected nil because FailoverPlanID is set in the deployment detail)", err)
	}

	// Verify: a new ACTIVE deployment was created (Apply was called).
	deps, err := reg.ListDeployments(ctx)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	var newDeps []*registry.Deployment
	var origFinal *registry.Deployment
	for _, d := range deps {
		if d.ID == "dep-failing-e2e" {
			origFinal = d
		} else {
			newDeps = append(newDeps, d)
		}
	}
	if len(newDeps) != 1 {
		allIDs := make([]string, len(deps))
		for i, d := range deps {
			allIDs[i] = d.ID + "(" + d.State + ")"
		}
		t.Fatalf("expected exactly 1 new deployment after failover, got %d; all=%v",
			len(newDeps), allIDs)
	}
	if newDeps[0].State != orchestrator.StateActive {
		t.Errorf("new deployment state = %q, want %q (Apply must have succeeded)",
			newDeps[0].State, orchestrator.StateActive)
	}

	// Verify: original deployment was torn down — state should be STOPPED.
	if origFinal == nil {
		t.Fatal("original deployment row is missing from the registry after failover")
	}
	if origFinal.State != orchestrator.StateStopped {
		t.Errorf("original deployment state = %q, want %q (Teardown must have been called)",
			origFinal.State, orchestrator.StateStopped)
	}

	// Verify: the spare-node's agent received a StartEngine call.
	agent.mu.Lock()
	starts := append([]string(nil), agent.starts...)
	agent.mu.Unlock()
	if len(starts) == 0 {
		t.Error("StartEngine was never called — Apply did not reach the spare node's agent")
	}
}
