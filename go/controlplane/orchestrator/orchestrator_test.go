package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// --- test doubles ----------------------------------------------------------

// scriptedStream feeds a fixed list of events, then either errors, blocks until
// the context is cancelled (simulating a hung engine), or returns EOF.
type scriptedStream struct {
	ctx    context.Context
	events []*purserv1.EngineEvent
	idx    int
	block  bool
	err    error
}

func (s *scriptedStream) Recv() (*purserv1.EngineEvent, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.block {
		<-s.ctx.Done()
		return nil, s.ctx.Err()
	}
	return nil, io.EOF
}

func readyStream(ctx context.Context, handle string) orchestrator.EngineStream {
	return &scriptedStream{ctx: ctx, events: []*purserv1.EngineEvent{
		{Kind: purserv1.EngineEventKind_ENGINE_EVENT_KIND_LOADING, Detail: "loading"},
		{Kind: purserv1.EngineEventKind_ENGINE_EVENT_KIND_READY, Detail: handle},
	}}
}

type startRecord struct {
	addr  string
	role  purserv1.Role
	peers []string
}

type mockAgentClient struct {
	mu     sync.Mutex
	starts []startRecord
	stops  []string
	stream func(ctx context.Context, addr string, req *purserv1.StartEngineRequest) (orchestrator.EngineStream, error)
}

func (m *mockAgentClient) StartEngine(ctx context.Context, addr string, req *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
	m.mu.Lock()
	m.starts = append(m.starts, startRecord{addr: addr, role: req.GetRole(), peers: req.GetPeers()})
	m.mu.Unlock()
	return m.stream(ctx, addr, req)
}

func (m *mockAgentClient) StopEngine(_ context.Context, _ string, req *purserv1.StopEngineRequest) (*purserv1.StopReply, error) {
	m.mu.Lock()
	m.stops = append(m.stops, req.GetHandle())
	m.mu.Unlock()
	return &purserv1.StopReply{Status: "stopped"}, nil
}

func (m *mockAgentClient) snapshot() ([]startRecord, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]startRecord(nil), m.starts...), append([]string(nil), m.stops...)
}

type mockGateway struct {
	mu        sync.Mutex
	upserts   []orchestrator.RouteUpdate
	deletes   []string
	upsertErr error
}

func (g *mockGateway) UpsertRoute(_ context.Context, u orchestrator.RouteUpdate) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.upserts = append(g.upserts, u)
	return g.upsertErr
}

func (g *mockGateway) DeleteRoute(_ context.Context, modelID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deletes = append(g.deletes, modelID)
	return nil
}

// --- helpers ---------------------------------------------------------------

func openReg(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

func twoWorkerPlan() *purserv1.DeploymentPlan {
	return &purserv1.DeploymentPlan{
		PlanId:       "plan-1",
		ModelId:      "llama-70b",
		Quantization: "q4",
		Assignments: []*purserv1.Assignment{
			{NodeId: "w1", Role: purserv1.Role_ROLE_WORKER, LayerStart: 0, LayerEnd: 40},
			{NodeId: "w2", Role: purserv1.Role_ROLE_WORKER, LayerStart: 40, LayerEnd: 80},
			{NodeId: "h1", Role: purserv1.Role_ROLE_HOST},
		},
	}
}

func topology() orchestrator.MapResolver {
	return orchestrator.MapResolver{
		"w1": {AgentAddr: "10.0.0.1:9443", InferenceAddr: "10.0.0.1:8000"},
		"w2": {AgentAddr: "10.0.0.2:9443", InferenceAddr: "10.0.0.2:8000"},
		"h1": {AgentAddr: "10.0.0.3:9443", InferenceAddr: "10.0.0.3:8000"},
	}
}

// --- tests -----------------------------------------------------------------

func TestApply_WorkerBeforeHostOrder(t *testing.T) {
	reg := openReg(t)
	gw := &mockGateway{}
	agent := &mockAgentClient{stream: func(ctx context.Context, addr string, _ *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
		return readyStream(ctx, "handle-"+addr), nil
	}}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents: agent, Resolver: topology(), Gateway: gw,
		Config: orchestrator.Config{StepTimeout: time.Second},
	})

	depID, err := orch.Apply(context.Background(), twoWorkerPlan())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	starts, _ := agent.snapshot()
	if len(starts) != 3 {
		t.Fatalf("expected 3 StartEngine calls, got %d", len(starts))
	}
	if starts[0].role != purserv1.Role_ROLE_WORKER || starts[1].role != purserv1.Role_ROLE_WORKER {
		t.Fatalf("first two starts must be workers, got %v %v", starts[0].role, starts[1].role)
	}
	if starts[2].role != purserv1.Role_ROLE_HOST {
		t.Fatalf("third start must be host, got %v", starts[2].role)
	}
	wantPeers := map[string]bool{"10.0.0.1:8000": true, "10.0.0.2:8000": true}
	if len(starts[2].peers) != 2 {
		t.Fatalf("host peers = %v, want 2", starts[2].peers)
	}
	for _, p := range starts[2].peers {
		if !wantPeers[p] {
			t.Errorf("unexpected host peer %q", p)
		}
	}

	dep, err := reg.GetDeployment(context.Background(), depID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if dep.State != orchestrator.StateActive {
		t.Fatalf("deployment state = %q, want ACTIVE", dep.State)
	}
	var detail orchestrator.DeploymentDetail
	if err := json.Unmarshal(dep.Detail, &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Endpoint != "http://10.0.0.3:8000" || detail.HostNodeID != "h1" {
		t.Errorf("bad detail: %+v", detail)
	}
	if len(gw.upserts) != 1 {
		t.Fatalf("gateway upserts = %d, want 1", len(gw.upserts))
	}
	if u := gw.upserts[0]; u.ModelID != "llama-70b" || u.Endpoint != "http://10.0.0.3:8000" || u.State != "active" || u.DeploymentID != depID {
		t.Errorf("bad route update: %+v", u)
	}
}

func TestApply_TimeoutMarksFailed(t *testing.T) {
	reg := openReg(t)
	agent := &mockAgentClient{stream: func(ctx context.Context, _ string, _ *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
		return &scriptedStream{ctx: ctx, block: true}, nil
	}}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents: agent, Resolver: topology(),
		Config: orchestrator.Config{StepTimeout: 40 * time.Millisecond},
	})

	depID, err := orch.Apply(context.Background(), twoWorkerPlan())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	starts, _ := agent.snapshot()
	if len(starts) != 1 || starts[0].role != purserv1.Role_ROLE_WORKER {
		t.Fatalf("expected a single worker start attempt, got %v", starts)
	}
	dep, err := reg.GetDeployment(context.Background(), depID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if dep.State != orchestrator.StateFailed {
		t.Fatalf("deployment state = %q, want FAILED", dep.State)
	}
}

func TestApply_WorkerCrashRollsBack(t *testing.T) {
	reg := openReg(t)
	agent := &mockAgentClient{stream: func(ctx context.Context, addr string, req *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
		if addr == "10.0.0.2:9443" {
			return &scriptedStream{ctx: ctx, events: []*purserv1.EngineEvent{
				{Kind: purserv1.EngineEventKind_ENGINE_EVENT_KIND_ERROR, Detail: "cuda oom"},
			}}, nil
		}
		return readyStream(ctx, "handle-"+addr), nil
	}}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents: agent, Resolver: topology(),
		Config: orchestrator.Config{StepTimeout: time.Second},
	})

	_, err := orch.Apply(context.Background(), twoWorkerPlan())
	if err == nil {
		t.Fatal("expected error from crashed worker")
	}
	starts, stops := agent.snapshot()
	if len(starts) != 2 {
		t.Fatalf("expected 2 start attempts, got %d", len(starts))
	}
	if len(stops) != 1 || stops[0] != "handle-10.0.0.1:9443" {
		t.Fatalf("expected rollback stop of w1 handle, got %v", stops)
	}
}

func TestApply_GatewayFailureDoesNotFailDeployment(t *testing.T) {
	reg := openReg(t)
	gw := &mockGateway{upsertErr: errors.New("gateway down")}
	agent := &mockAgentClient{stream: func(ctx context.Context, addr string, _ *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
		return readyStream(ctx, "h-"+addr), nil
	}}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents: agent, Resolver: topology(), Gateway: gw,
		Config: orchestrator.Config{StepTimeout: time.Second},
	})

	depID, err := orch.Apply(context.Background(), twoWorkerPlan())
	if err != nil {
		t.Fatalf("Apply must succeed despite gateway failure: %v", err)
	}
	dep, _ := reg.GetDeployment(context.Background(), depID)
	if dep.State != orchestrator.StateActive {
		t.Fatalf("deployment must be ACTIVE despite gateway failure, got %q", dep.State)
	}
}

func TestTeardown_ReverseOrderAndRouteDelete(t *testing.T) {
	reg := openReg(t)
	gw := &mockGateway{}
	agent := &mockAgentClient{stream: func(ctx context.Context, addr string, _ *purserv1.StartEngineRequest) (orchestrator.EngineStream, error) {
		return readyStream(ctx, "handle-"+addr), nil
	}}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents: agent, Resolver: topology(), Gateway: gw,
		Config: orchestrator.Config{StepTimeout: time.Second},
	})
	depID, err := orch.Apply(context.Background(), twoWorkerPlan())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := orch.Teardown(context.Background(), depID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	_, stops := agent.snapshot()
	if len(stops) != 3 {
		t.Fatalf("expected 3 stops, got %v", stops)
	}
	if stops[0] != "handle-10.0.0.3:9443" {
		t.Errorf("host engine should be stopped first, got %q", stops[0])
	}
	if len(gw.deletes) != 1 || gw.deletes[0] != "llama-70b" {
		t.Errorf("expected DeleteRoute(llama-70b), got %v", gw.deletes)
	}
	dep, _ := reg.GetDeployment(context.Background(), depID)
	if dep.State != orchestrator.StateStopped {
		t.Errorf("deployment state = %q, want STOPPED", dep.State)
	}
}
