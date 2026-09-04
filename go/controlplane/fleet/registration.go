package fleet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RegistrationServer implements the RegistrationService gRPC server: Join
// (enrollment) and Heartbeat (liveness/metrics streaming). Heartbeats update the
// registry (last_seen/state) — the reconciler's view of real state — and feed
// the live-metrics cache consumed by the SSE metrics endpoint.
type RegistrationServer struct {
	purserv1.UnimplementedRegistrationServiceServer

	mgr     *Manager
	reg     registry.Registry
	metrics *LiveMetrics
	log     *slog.Logger
	clock   func() time.Time
}

// NewRegistrationServer builds the RegistrationService server. metrics may be
// nil (a private cache is created).
func NewRegistrationServer(mgr *Manager, reg registry.Registry, metrics *LiveMetrics, log *slog.Logger) *RegistrationServer {
	if metrics == nil {
		metrics = NewLiveMetrics()
	}
	if log == nil {
		log = slog.Default()
	}
	return &RegistrationServer{mgr: mgr, reg: reg, metrics: metrics, log: log, clock: time.Now}
}

// Metrics exposes the live-metrics cache (so the HTTP server can read it).
func (s *RegistrationServer) Metrics() *LiveMetrics { return s.metrics }

// Join enrolls a node and returns its node ID, client cert and CA cert.
func (s *RegistrationServer) Join(ctx context.Context, req *purserv1.JoinRequest) (*purserv1.JoinReply, error) {
	res, err := s.mgr.Join(ctx, req.GetJoinToken(), req.GetHardwareProfile())
	if err != nil {
		if errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrTokenUsed) {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &purserv1.JoinReply{
		NodeId:     res.NodeID,
		ClientCert: res.ClientCert,
		CaCert:     res.CACert,
	}, nil
}

// Heartbeat consumes the client-streaming heartbeat: each message updates the
// node's last_seen/state in the registry and the live-metrics cache. When the
// stream ends it acknowledges with an Ack.
func (s *RegistrationServer) Heartbeat(stream purserv1.RegistrationService_HeartbeatServer) error {
	ctx := stream.Context()
	for {
		hb, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&purserv1.Ack{})
		}
		if err != nil {
			return err
		}
		s.applyHeartbeat(ctx, hb)
	}
}

// applyHeartbeat records one heartbeat into the registry and the live cache.
func (s *RegistrationServer) applyHeartbeat(ctx context.Context, hb *purserv1.Heartbeat) {
	nodeID := hb.GetNodeId()
	if nodeID == "" {
		return
	}
	ts := s.clock().UTC()
	if hb.GetTs() != nil {
		ts = hb.GetTs().AsTime().UTC()
	}
	s.metrics.Update(nodeID, hb.GetState().String(), hb.GetMetrics(), ts)

	n, err := s.reg.GetNode(ctx, nodeID)
	if err != nil {
		// Heartbeat from an unknown node: ignore (it must Join first).
		s.log.Warn("heartbeat from unknown node", "node", nodeID)
		return
	}
	n.LastSeen = ts
	if st := hb.GetState().String(); st != "" && st != "NODE_STATE_UNSPECIFIED" {
		n.State = st
	}
	if err := s.reg.UpdateNode(ctx, n); err != nil {
		s.log.Error("update node from heartbeat failed", "node", nodeID, "err", err)
	}
}

// --- Live metrics cache ----------------------------------------------------

// NodeMetrics is the latest heartbeat sample for a node.
type NodeMetrics struct {
	NodeID     string    `json:"node_id"`
	State      string    `json:"state"`
	PrefillTps float64   `json:"prefill_tok_s"`
	DecodeTps  float64   `json:"decode_tok_s"`
	RAMUsedGB  float64   `json:"ram_used_gb"`
	VRAMUsedGB float64   `json:"vram_used_gb"`
	QueueDepth uint32    `json:"queue_depth"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// LiveMetrics is a concurrency-safe cache of the latest per-node metrics,
// populated by the Heartbeat handler and read by the SSE metrics endpoint.
type LiveMetrics struct {
	mu   sync.RWMutex
	byID map[string]NodeMetrics
}

// NewLiveMetrics builds an empty cache.
func NewLiveMetrics() *LiveMetrics {
	return &LiveMetrics{byID: map[string]NodeMetrics{}}
}

// Update records the latest sample for a node.
func (l *LiveMetrics) Update(nodeID, state string, m *purserv1.EngineMetrics, ts time.Time) {
	nm := NodeMetrics{NodeID: nodeID, State: state, UpdatedAt: ts}
	if m != nil {
		nm.PrefillTps = m.GetPrefillTokS()
		nm.DecodeTps = m.GetDecodeTokS()
		nm.RAMUsedGB = m.GetRamUsedGb()
		nm.VRAMUsedGB = m.GetVramUsedGb()
		nm.QueueDepth = m.GetQueueDepth()
	}
	l.mu.Lock()
	l.byID[nodeID] = nm
	l.mu.Unlock()
}

// Snapshot returns the current per-node metrics. It satisfies the HTTP server's
// MetricsSource interface.
func (l *LiveMetrics) Snapshot(_ context.Context) (any, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]NodeMetrics, 0, len(l.byID))
	for _, m := range l.byID {
		out = append(out, m)
	}
	return map[string]any{"nodes": out, "count": len(out)}, nil
}
