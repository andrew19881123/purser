package orchestrator

import (
	"context"
	"fmt"
	"sync"

	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/grpc"
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
//
// TODO(security): the default dial options use insecure transport. Once the PKI
// bootstrap is wired end-to-end, supply mTLS credentials built from the agent
// client certificate and the CA pool.
type GRPCAgentClient struct {
	dialOpts []grpc.DialOption

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

var _ AgentClient = (*GRPCAgentClient)(nil)

// NewGRPCAgentClient builds a gRPC-backed AgentClient. If no dial options are
// supplied, an insecure transport is used (MVP / non-mTLS bootstrap).
func NewGRPCAgentClient(opts ...grpc.DialOption) *GRPCAgentClient {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	return &GRPCAgentClient{dialOpts: opts, conns: map[string]*grpc.ClientConn{}}
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
