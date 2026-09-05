package orchestrator_test

import (
	"crypto/x509"
	"testing"

	"github.com/purser/purser/go/controlplane/orchestrator"
)

// TestNewGRPCAgentClientWithCA_TLSWhenPoolProvided verifies that when a non-nil
// CA cert pool is supplied the resulting client stores TLS transport credentials
// (security protocol == "tls").
func TestNewGRPCAgentClientWithCA_TLSWhenPoolProvided(t *testing.T) {
	pool := x509.NewCertPool()

	client := orchestrator.NewGRPCAgentClientWithCA(pool, nil)
	if client == nil {
		t.Fatal("expected non-nil GRPCAgentClient")
	}
	defer client.Close()

	tc := client.TransportCredsForTest()
	if tc == nil {
		t.Fatal("expected non-nil TransportCredentials when pool is provided")
	}
	if proto := tc.Info().SecurityProtocol; proto != "tls" {
		t.Errorf("SecurityProtocol = %q, want %q", proto, "tls")
	}
}

// TestNewGRPCAgentClientWithCA_InsecureWhenNilPool verifies that when pool is nil
// the client falls back to insecure transport (dev mode) — transportCreds is nil.
func TestNewGRPCAgentClientWithCA_InsecureWhenNilPool(t *testing.T) {
	client := orchestrator.NewGRPCAgentClientWithCA(nil, nil)
	if client == nil {
		t.Fatal("expected non-nil GRPCAgentClient for nil pool (dev mode)")
	}
	defer client.Close()

	tc := client.TransportCredsForTest()
	if tc != nil {
		t.Errorf("expected nil TransportCredentials for insecure (dev mode) client, got %T", tc)
	}
}
