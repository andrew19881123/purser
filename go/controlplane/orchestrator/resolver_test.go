package orchestrator_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/registry"
)

// newResolverReg opens a migrated, temp-file SQLite registry (pure-Go driver,
// CGO-free) for resolver tests.
func newResolverReg(t *testing.T) registry.Registry {
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

// A node that advertised both addresses must have them used verbatim, ignoring
// the hostname + well-known-port convention.
func TestRegistryResolver_UsesAdvertisedAddrs(t *testing.T) {
	ctx := context.Background()
	reg := newResolverReg(t)
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:                      "adv-node",
		Hostname:                "gpu.local",
		AdvertisedAgentAddr:     "10.0.0.5:41000",
		AdvertisedInferenceAddr: "10.0.0.5:9001",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	r := orchestrator.NewRegistryResolver(reg, 0, 0) // default ports
	ep, err := r.Resolve(ctx, "adv-node")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ep.AgentAddr != "10.0.0.5:41000" {
		t.Errorf("AgentAddr = %q, want advertised 10.0.0.5:41000", ep.AgentAddr)
	}
	if ep.InferenceAddr != "10.0.0.5:9001" {
		t.Errorf("InferenceAddr = %q, want advertised 10.0.0.5:9001", ep.InferenceAddr)
	}
}

// A node with no advertised addresses must fall back to hostname + the
// configured well-known ports (the MVP convention).
func TestRegistryResolver_FallsBackToConvention(t *testing.T) {
	ctx := context.Background()
	reg := newResolverReg(t)
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       "plain-node",
		Hostname: "host.local",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	r := orchestrator.NewRegistryResolver(reg, 0, 0)
	ep, err := r.Resolve(ctx, "plain-node")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	wantAgent := "host.local:" + strconv.Itoa(orchestrator.DefaultAgentPort)
	wantInf := "host.local:" + strconv.Itoa(orchestrator.DefaultInferencePort)
	if ep.AgentAddr != wantAgent {
		t.Errorf("AgentAddr = %q, want %q", ep.AgentAddr, wantAgent)
	}
	if ep.InferenceAddr != wantInf {
		t.Errorf("InferenceAddr = %q, want %q", ep.InferenceAddr, wantInf)
	}
}

// A per-face mix: agent advertised, inference not. The advertised agent addr is
// used; the inference addr falls back to hostname + well-known port.
func TestRegistryResolver_PartialAdvertisedMixesWithFallback(t *testing.T) {
	ctx := context.Background()
	reg := newResolverReg(t)
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:                  "mixed-node",
		Hostname:            "mix.local",
		AdvertisedAgentAddr: "192.168.1.10:50151",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	r := orchestrator.NewRegistryResolver(reg, 0, 0)
	ep, err := r.Resolve(ctx, "mixed-node")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ep.AgentAddr != "192.168.1.10:50151" {
		t.Errorf("AgentAddr = %q, want advertised 192.168.1.10:50151", ep.AgentAddr)
	}
	wantInf := "mix.local:" + strconv.Itoa(orchestrator.DefaultInferencePort)
	if ep.InferenceAddr != wantInf {
		t.Errorf("InferenceAddr = %q, want fallback %q", ep.InferenceAddr, wantInf)
	}
}

// A node with advertised addresses but no hostname still resolves (no fallback
// is needed, so the missing hostname is not an error).
func TestRegistryResolver_AdvertisedWithoutHostname(t *testing.T) {
	ctx := context.Background()
	reg := newResolverReg(t)
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:                      "no-host",
		AdvertisedAgentAddr:     "10.1.1.1:5000",
		AdvertisedInferenceAddr: "10.1.1.1:8080",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	r := orchestrator.NewRegistryResolver(reg, 0, 0)
	ep, err := r.Resolve(ctx, "no-host")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ep.AgentAddr != "10.1.1.1:5000" || ep.InferenceAddr != "10.1.1.1:8080" {
		t.Errorf("endpoint = %+v, want advertised addrs", ep)
	}
}

// A node with neither an advertised address nor a hostname cannot be resolved.
func TestRegistryResolver_NoHostnameNoAdvertisedErrors(t *testing.T) {
	ctx := context.Background()
	reg := newResolverReg(t)
	if err := reg.CreateNode(ctx, &registry.Node{ID: "empty"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	r := orchestrator.NewRegistryResolver(reg, 0, 0)
	if _, err := r.Resolve(ctx, "empty"); err == nil {
		t.Error("expected error resolving node with no advertised addr and no hostname")
	}
}
