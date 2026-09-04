package orchestrator

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/purser/purser/go/controlplane/registry"
)

// Endpoint locates the two network faces of a node that the orchestrator needs:
// the gRPC control address (AgentService) and the inference address that the
// Gateway routes traffic to when the node runs the HOST role.
type Endpoint struct {
	// AgentAddr is the AgentService gRPC address, "host:port".
	AgentAddr string
	// InferenceAddr is the address the Gateway/peers use to reach the engine,
	// "host:port".
	InferenceAddr string
}

// Resolver maps a node ID to its Endpoint. It is injectable so tests can supply
// a static map without touching the registry.
type Resolver interface {
	Resolve(ctx context.Context, nodeID string) (Endpoint, error)
}

// RegistryResolver derives endpoints from the node's hostname recorded in the
// registry, combined with well-known ports. This is the MVP convention until
// agents advertise their listen addresses explicitly.
type RegistryResolver struct {
	Reg           registry.Registry
	AgentPort     int
	InferencePort int
}

// DefaultAgentPort / DefaultInferencePort are the MVP port conventions.
//
// DefaultAgentPort matches the agent's real AgentService bind port
// (rust/crates/agent DEFAULT_AGENT_PORT = 50151, overridable there via
// PURSER_AGENT_BIND). Keeping the orchestrator's default in sync lets a
// single-node deploy reach the AgentService with zero configuration. The port
// is still overridable here via NewRegistryResolver (main.go reads
// PURSER_AGENT_PORT).
//
// TODO(multi-agent): this host:well-known-port convention cannot address more
// than one agent on the SAME host — every agent would resolve to the same
// address. Supporting multiple agents per host requires each agent to register
// its own advertised address (a RegistrationService/proto change), after which
// the resolver should read the node's advertised AgentAddr instead of
// synthesizing host:DefaultAgentPort. Out of scope for now; documented only.
const (
	DefaultAgentPort     = 50151
	DefaultInferencePort = 8000
)

var _ Resolver = (*RegistryResolver)(nil)

// NewRegistryResolver builds a RegistryResolver with default ports where unset.
func NewRegistryResolver(reg registry.Registry, agentPort, infPort int) *RegistryResolver {
	if agentPort == 0 {
		agentPort = DefaultAgentPort
	}
	if infPort == 0 {
		infPort = DefaultInferencePort
	}
	return &RegistryResolver{Reg: reg, AgentPort: agentPort, InferencePort: infPort}
}

func (r *RegistryResolver) Resolve(ctx context.Context, nodeID string) (Endpoint, error) {
	n, err := r.Reg.GetNode(ctx, nodeID)
	if err != nil {
		return Endpoint{}, fmt.Errorf("orchestrator: resolve node %q: %w", nodeID, err)
	}
	host := n.Hostname
	if host == "" {
		return Endpoint{}, fmt.Errorf("orchestrator: node %q has no hostname to resolve", nodeID)
	}
	return Endpoint{
		AgentAddr:     net.JoinHostPort(host, strconv.Itoa(r.AgentPort)),
		InferenceAddr: net.JoinHostPort(host, strconv.Itoa(r.InferencePort)),
	}, nil
}

// MapResolver is a static Resolver for tests and fixed topologies.
type MapResolver map[string]Endpoint

var _ Resolver = (MapResolver)(nil)

func (m MapResolver) Resolve(_ context.Context, nodeID string) (Endpoint, error) {
	ep, ok := m[nodeID]
	if !ok {
		return Endpoint{}, fmt.Errorf("orchestrator: no endpoint for node %q", nodeID)
	}
	return ep, nil
}
