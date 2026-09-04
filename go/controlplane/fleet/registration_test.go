package fleet_test

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type harness struct {
	reg    registry.Registry
	ca     *pki.Authority
	mgr    *fleet.Manager
	srv    *fleet.RegistrationServer
	client purserv1.RegistrationServiceClient
	grpc   *grpc.Server
	conn   *grpc.ClientConn
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	reg, err := registry.Open(t.TempDir() + "/reg.db")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ca, err := pki.New(ctx, reg, pki.Options{})
	if err != nil {
		t.Fatalf("pki: %v", err)
	}
	mgr := fleet.NewWithSecret(reg, ca, []byte("test-secret-key"))
	srv := fleet.NewRegistrationServer(mgr, reg, nil, nil)

	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	purserv1.RegisterRegistrationServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	h := &harness{reg: reg, ca: ca, mgr: mgr, srv: srv, client: purserv1.NewRegistrationServiceClient(conn), grpc: gs, conn: conn}
	t.Cleanup(func() {
		conn.Close()
		gs.Stop()
		reg.Close()
	})
	return h
}

func TestRegistration_JoinIssuesCert(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	tok, err := h.mgr.GenerateJoinToken(ctx, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	reply, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken: tok.Token,
		HardwareProfile: &purserv1.HardwareProfile{
			NodeId:     "gpu-1",
			Hostname:   "gpu1.local",
			RamTotalGb: 128,
			Gpus:       []*purserv1.GpuInfo{{Name: "RTX", VramGb: 24, Count: 2}},
		},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if reply.GetNodeId() != "gpu-1" {
		t.Errorf("node id = %q, want gpu-1", reply.GetNodeId())
	}
	if len(reply.GetClientCert()) == 0 || len(reply.GetCaCert()) == 0 {
		t.Fatal("Join must return client cert and CA cert")
	}
	// CA cert returned must match the authority's CA PEM.
	if !bytes.Equal(reply.GetCaCert(), h.ca.CACertPEM()) {
		t.Error("returned CA cert does not match authority CA cert")
	}
	// The issued client cert must verify against the CA.
	if _, err := h.ca.VerifyClient(ctx, reply.GetClientCert()); err != nil {
		t.Errorf("issued client cert must verify: %v", err)
	}

	// Node must be registered ENROLLED with hardware summarized.
	n, err := h.reg.GetNode(ctx, "gpu-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.State != fleet.NodeStateEnrolled {
		t.Errorf("node state = %q, want %q", n.State, fleet.NodeStateEnrolled)
	}
	if n.RAMGB != 128 || n.VRAMGB != 48 { // 24 * 2 GPUs
		t.Errorf("hardware summary wrong: ram=%v vram=%v", n.RAMGB, n.VRAMGB)
	}
}

func TestRegistration_JoinStoresAdvertisedAddrs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	tok, err := h.mgr.GenerateJoinToken(ctx, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	reply, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken:               tok.Token,
		HardwareProfile:         &purserv1.HardwareProfile{NodeId: "adv-1", Hostname: "adv.local"},
		AdvertisedAgentAddr:     "192.168.1.10:50151",
		AdvertisedInferenceAddr: "192.168.1.10:8000",
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if reply.GetNodeId() != "adv-1" {
		t.Fatalf("node id = %q, want adv-1", reply.GetNodeId())
	}

	// Re-read the node and verify the advertised addresses were persisted.
	n, err := h.reg.GetNode(ctx, "adv-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.AdvertisedAgentAddr != "192.168.1.10:50151" {
		t.Errorf("AdvertisedAgentAddr = %q, want 192.168.1.10:50151", n.AdvertisedAgentAddr)
	}
	if n.AdvertisedInferenceAddr != "192.168.1.10:8000" {
		t.Errorf("AdvertisedInferenceAddr = %q, want 192.168.1.10:8000", n.AdvertisedInferenceAddr)
	}
}

func TestRegistration_JoinWithoutAdvertisedAddrs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	tok, _ := h.mgr.GenerateJoinToken(ctx, time.Hour)
	if _, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken:       tok.Token,
		HardwareProfile: &purserv1.HardwareProfile{NodeId: "noadv-1", Hostname: "noadv.local"},
	}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	n, err := h.reg.GetNode(ctx, "noadv-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.AdvertisedAgentAddr != "" || n.AdvertisedInferenceAddr != "" {
		t.Errorf("expected empty advertised addrs, got agent=%q inf=%q",
			n.AdvertisedAgentAddr, n.AdvertisedInferenceAddr)
	}
}

func TestRegistration_JoinRejectsBadAndReusedToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Bad token.
	if _, err := h.client.Join(ctx, &purserv1.JoinRequest{JoinToken: "garbage.sig"}); err == nil {
		t.Error("expected error for invalid token")
	}

	// Valid token works once, then is rejected (single-use).
	tok, _ := h.mgr.GenerateJoinToken(ctx, time.Hour)
	if _, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken:       tok.Token,
		HardwareProfile: &purserv1.HardwareProfile{NodeId: "n-a"},
	}); err != nil {
		t.Fatalf("first join: %v", err)
	}
	if _, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken:       tok.Token,
		HardwareProfile: &purserv1.HardwareProfile{NodeId: "n-b"},
	}); err == nil {
		t.Error("expected error reusing single-use token")
	}
}

func TestRegistration_JoinGeneratesNodeID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tok, _ := h.mgr.GenerateJoinToken(ctx, time.Hour)
	reply, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken:       tok.Token,
		HardwareProfile: &purserv1.HardwareProfile{Hostname: "anon"},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if len(reply.GetNodeId()) == 0 {
		t.Error("expected a generated node id")
	}
}

func TestRegistration_HeartbeatUpdatesRegistryAndMetrics(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Enroll a node first.
	tok, _ := h.mgr.GenerateJoinToken(ctx, time.Hour)
	if _, err := h.client.Join(ctx, &purserv1.JoinRequest{
		JoinToken:       tok.Token,
		HardwareProfile: &purserv1.HardwareProfile{NodeId: "hb-1", Hostname: "hb"},
	}); err != nil {
		t.Fatalf("join: %v", err)
	}

	stream, err := h.client.Heartbeat(ctx)
	if err != nil {
		t.Fatalf("heartbeat open: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := stream.Send(&purserv1.Heartbeat{
			NodeId:  "hb-1",
			State:   purserv1.NodeState_NODE_STATE_RUNNING,
			Metrics: &purserv1.EngineMetrics{DecodeTokS: 12.5, QueueDepth: 2},
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatalf("close/recv: %v", err)
	}

	// Registry state updated.
	n, err := h.reg.GetNode(ctx, "hb-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.State != "NODE_STATE_RUNNING" {
		t.Errorf("node state = %q, want RUNNING", n.State)
	}
	if n.LastSeen.IsZero() {
		t.Error("last_seen not updated by heartbeat")
	}

	// Live metrics cache updated.
	snap, _ := h.srv.Metrics().Snapshot(ctx)
	m, ok := snap.(map[string]any)
	if !ok || m["count"].(int) < 1 {
		t.Errorf("expected live metrics for the node, got %+v", snap)
	}
}
