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

// RegistryResolver resolves a node's endpoints from the registry. It prefers
// the addresses the agent advertised at Join time (Node.AdvertisedAgentAddr /
// Node.AdvertisedInferenceAddr) and falls back, per face, to the MVP convention
// of the node's hostname combined with a well-known port. The advertised
// addresses are what let multiple agents share a host; the fallback keeps
// zero-config single-node deploys working.
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
// The host + well-known-port convention below cannot address more than one
// agent on the SAME host — every agent would resolve to the same address. That
// is why agents now advertise their own AgentService and inference addresses in
// the JoinRequest (persisted on the Node); Resolve uses those when present and
// only synthesizes host:DefaultAgentPort as a fallback.
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
	// Prefer the addresses the agent advertised at Join time; fall back per face
	// to the hostname + well-known-port convention.
	ep := Endpoint{
		AgentAddr:     n.AdvertisedAgentAddr,
		InferenceAddr: n.AdvertisedInferenceAddr,
	}
	if ep.AgentAddr == "" || ep.InferenceAddr == "" {
		host := n.Hostname
		if host == "" {
			return Endpoint{}, fmt.Errorf("orchestrator: node %q has neither an advertised address nor a hostname to resolve", nodeID)
		}
		if ep.AgentAddr == "" {
			ep.AgentAddr = net.JoinHostPort(host, strconv.Itoa(r.AgentPort))
		}
		if ep.InferenceAddr == "" {
			ep.InferenceAddr = net.JoinHostPort(host, strconv.Itoa(r.InferencePort))
		}
	}
	return ep, nil
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
