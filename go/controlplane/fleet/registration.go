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
	res, err := s.mgr.Join(ctx, req.GetJoinToken(), req.GetHardwareProfile(),
		req.GetAdvertisedAgentAddr(), req.GetAdvertisedInferenceAddr())
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
	s.metrics.Update(nodeID, hb.GetState().String(), hb.GetMetrics(), hb.GetNodeMetrics(), ts)

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
// The EngineMetrics fields (PrefillTps … AcceptedTokensRatio) come from the
// legacy purserv1.EngineMetrics payload; the hardware fields
// (CpuUtilizationPct … InferencePortAlive) come from purserv1.NodeMetrics,
// which agents started emitting in the v0.3 heartbeat extension.
type NodeMetrics struct {
	NodeID              string    `json:"node_id"`
	State               string    `json:"state"`
	PrefillTps          float64   `json:"prefill_tok_s"`
	DecodeTps           float64   `json:"decode_tok_s"`
	RAMUsedGB           float64   `json:"ram_used_gb"`
	VRAMUsedGB          float64   `json:"vram_used_gb"`
	QueueDepth          uint32    `json:"queue_depth"`
	AcceptedTokensRatio float64   `json:"accepted_tokens_ratio"`
	UpdatedAt           time.Time `json:"updated_at"`

	// Hardware metrics from purserv1.NodeMetrics (0–100 for percentage fields).
	CpuUtilizationPct   float64 `json:"cpu_utilization_pct"`
	GpuUtilizationPct   float64 `json:"gpu_utilization_pct"`
	MemBandwidthUtilPct float64 `json:"mem_bandwidth_util_pct"`
	TokensPerSecond     float64 `json:"tokens_per_second"`
	InferencePortAlive  bool    `json:"inference_port_alive"`
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

// Update records the latest sample for a node. em carries the engine-level
// metrics (inference throughput, VRAM usage, queue depth); hw carries the
// per-node hardware metrics (CPU/GPU utilization, memory bandwidth,
// inference-port liveness). Either may be nil; absent fields are left at their
// zero values.
func (l *LiveMetrics) Update(nodeID, state string, em *purserv1.EngineMetrics, hw *purserv1.NodeMetrics, ts time.Time) {
	nm := NodeMetrics{NodeID: nodeID, State: state, UpdatedAt: ts}
	if em != nil {
		nm.PrefillTps = em.GetPrefillTokS()
		nm.DecodeTps = em.GetDecodeTokS()
		nm.RAMUsedGB = em.GetRamUsedGb()
		nm.VRAMUsedGB = em.GetVramUsedGb()
		nm.QueueDepth = em.GetQueueDepth()
		nm.AcceptedTokensRatio = em.GetAcceptedTokensRatio()
	}
	if hw != nil {
		nm.CpuUtilizationPct = float64(hw.GetCpuUtilizationPct())
		nm.GpuUtilizationPct = float64(hw.GetGpuUtilizationPct())
		nm.MemBandwidthUtilPct = float64(hw.GetMemBandwidthUtilPct())
		nm.TokensPerSecond = float64(hw.GetTokensPerSecond())
		nm.InferencePortAlive = hw.GetInferencePortAlive()
	}
	l.mu.Lock()
	l.byID[nodeID] = nm
	l.mu.Unlock()
}

// Get returns the latest metrics sample for nodeID. ok is false when the node
// has not yet sent a heartbeat.
func (l *LiveMetrics) Get(nodeID string) (NodeMetrics, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	m, ok := l.byID[nodeID]
	return m, ok
}

// Snapshot returns the current per-node metrics in the SSE wire format. It
// satisfies the HTTP server's MetricsSource interface. Only nodes that have
// reported at least one heartbeat appear; use the server's metricsSnapshot
// (which joins against the registry) when zero-fill for silent nodes is
// needed.
//
// Wire shape:
//
//	{
//	  "at":                     "RFC3339",
//	  "aggregate_decode_tok_s": 0.0,
//	  "nodes": [
//	    {"node_id": "...", "state": "...", "metrics": {decode_tok_s, …}}
//	  ]
//	}
func (l *LiveMetrics) Snapshot(_ context.Context) (any, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	type metricsWire struct {
		PrefillTokS         float64 `json:"prefill_tok_s"`
		DecodeTokS          float64 `json:"decode_tok_s"`
		RAMUsedGB           float64 `json:"ram_used_gb"`
		VRAMUsedGB          float64 `json:"vram_used_gb"`
		QueueDepth          uint32  `json:"queue_depth"`
		AcceptedTokensRatio float64 `json:"accepted_tokens_ratio"`
	}
	type nodeWire struct {
		NodeID  string      `json:"node_id"`
		State   string      `json:"state"`
		Metrics metricsWire `json:"metrics"`
	}
	out := make([]nodeWire, 0, len(l.byID))
	var aggDecode float64
	for _, m := range l.byID {
		out = append(out, nodeWire{
			NodeID: m.NodeID,
			State:  m.State,
			Metrics: metricsWire{
				PrefillTokS:         m.PrefillTps,
				DecodeTokS:          m.DecodeTps,
				RAMUsedGB:           m.RAMUsedGB,
				VRAMUsedGB:          m.VRAMUsedGB,
				QueueDepth:          m.QueueDepth,
				AcceptedTokensRatio: m.AcceptedTokensRatio,
			},
		})
		aggDecode += m.DecodeTps
	}
	return map[string]any{
		"at":                     time.Now().UTC().Format(time.RFC3339),
		"aggregate_decode_tok_s": aggDecode,
		"nodes":                  out,
	}, nil
}
