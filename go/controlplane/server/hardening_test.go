// Package server — hardening_test.go covers the security hardening fixes:
//   - H7: constant-time internal-token comparison
package server

import (
	"context"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
)

// TestConstantTimeTokenValidation verifies the validateInternalToken helper:
//   - correct token → true
//   - wrong token with same length → false (timing safety)
//   - wrong token with different length → false
//   - empty provided with non-empty configured → false
//   - any token when configured secret is empty → false
func TestConstantTimeTokenValidation(t *testing.T) {
	const secret = "supersecrettoken123"

	// Build a minimal server with the secret configured.
	s := New(nil, Config{InternalToken: secret})

	cases := []struct {
		name     string
		provided string
		want     bool
	}{
		{"correct token", secret, true},
		{"same length wrong token", "supersecrettoken456", false},
		{"shorter token", "short", false},
		{"longer token", secret + "extra", false},
		{"empty token", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.validateInternalToken(tc.provided)
			if got != tc.want {
				t.Errorf("validateInternalToken(%q) = %v, want %v", tc.provided, got, tc.want)
			}
		})
	}

	// When no secret is configured, ALL tokens must be rejected.
	t.Run("no secret configured", func(t *testing.T) {
		// Use a real (but empty) registry so New() doesn't panic.
		sNoToken := New(newTestRegistry(t), Config{})
		if sNoToken.validateInternalToken(secret) {
			t.Error("validateInternalToken returned true with empty internalToken")
		}
		if sNoToken.validateInternalToken("") {
			t.Error("validateInternalToken(empty) returned true with empty internalToken")
		}
	})
}

// newTestRegistry opens an in-memory SQLiteRegistry for tests in the server
// package (package-internal, not exported to server_test).
func newTestRegistry(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(":memory:")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}
