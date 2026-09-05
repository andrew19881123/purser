package orchestrator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"sync"

	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// EngineStream is the subset of the AgentService.StartEngine server stream that
// the orchestrator consumes: it reads EngineEvents until READY/ERROR.
type EngineStream interface {
	Recv() (*purserv1.EngineEvent, error)
}

// AgentClient abstracts the AgentService RPCs the orchestrator drives, keyed by
// the agent's control-plane address. The production implementation dials gRPC;
// tests inject a mock that simulates READY / timeout / crash.
type AgentClient interface {
	// StartEngine opens the server-streaming StartEngine RPC against the agent
	// at addr and returns the event stream.
	StartEngine(ctx context.Context, addr string, req *purserv1.StartEngineRequest) (EngineStream, error)
	// StopEngine stops a running engine identified by req.Handle.
	StopEngine(ctx context.Context, addr string, req *purserv1.StopEngineRequest) (*purserv1.StopReply, error)
}

// GRPCAgentClient is the production AgentClient. It dials agents lazily and
// caches connections per address.
type GRPCAgentClient struct {
	dialOpts       []grpc.DialOption
	transportCreds credentials.TransportCredentials // nil → insecure (dev mode)

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

var _ AgentClient = (*GRPCAgentClient)(nil)

// NewGRPCAgentClient builds a gRPC-backed AgentClient. If no dial options are
// supplied, an insecure transport is used (dev mode / non-PKI bootstrap).
func NewGRPCAgentClient(opts ...grpc.DialOption) *GRPCAgentClient {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	return &GRPCAgentClient{dialOpts: opts, conns: map[string]*grpc.ClientConn{}}
}

// NewGRPCAgentClientWithCA builds a gRPC-backed AgentClient that verifies
// agent server certificates against the given CA pool (server-side TLS).
// The control plane does not present a client certificate — agents verify the
// control plane's identity via the shared CA pool they received at enrolment.
//
// If pool is nil the client falls back to insecure transport and logs a warning
// so the control plane still starts in dev/test environments where PKI is
// absent.
func NewGRPCAgentClientWithCA(pool *x509.CertPool, log *slog.Logger, extra ...grpc.DialOption) *GRPCAgentClient {
	if pool == nil {
		if log == nil {
			log = slog.Default()
		}
		log.Warn("orchestrator: no CA pool supplied — using insecure transport (dev mode only)")
		return NewGRPCAgentClient(extra...)
	}
	tlsCfg := &tls.Config{RootCAs: pool}
	tc := credentials.NewTLS(tlsCfg)
	opts := make([]grpc.DialOption, 0, 1+len(extra))
	opts = append(opts, grpc.WithTransportCredentials(tc))
	opts = append(opts, extra...)
	return &GRPCAgentClient{dialOpts: opts, transportCreds: tc, conns: map[string]*grpc.ClientConn{}}
}

func (c *GRPCAgentClient) conn(addr string) (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cc, ok := c.conns[addr]; ok {
		return cc, nil
	}
	cc, err := grpc.NewClient(addr, c.dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: dial agent %q: %w", addr, err)
	}
	c.conns[addr] = cc
	return cc, nil
}

func (c *GRPCAgentClient) StartEngine(ctx context.Context, addr string, req *purserv1.StartEngineRequest) (EngineStream, error) {
	cc, err := c.conn(addr)
	if err != nil {
		return nil, err
	}
	stream, err := purserv1.NewAgentServiceClient(cc).StartEngine(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: StartEngine %q: %w", addr, err)
	}
	return stream, nil
}

func (c *GRPCAgentClient) StopEngine(ctx context.Context, addr string, req *purserv1.StopEngineRequest) (*purserv1.StopReply, error) {
	cc, err := c.conn(addr)
	if err != nil {
		return nil, err
	}
	return purserv1.NewAgentServiceClient(cc).StopEngine(ctx, req)
}

// Close tears down all cached connections.
func (c *GRPCAgentClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cc := range c.conns {
		_ = cc.Close()
	}
	c.conns = map[string]*grpc.ClientConn{}
	return nil
}
