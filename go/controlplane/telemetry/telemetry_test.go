package telemetry_test

import (
	"context"
	"testing"

	"github.com/purser/purser/go/controlplane/telemetry"
)

// TestInitNoEndpoint verifies that Init with no OTEL_EXPORTER_OTLP_ENDPOINT
// returns a no-op shutdown function without error and does not attempt any
// network connection — safe for offline/CI environments.
func TestInitNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := telemetry.Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("Init() shutdown = nil, want non-nil function")
	}
	// Calling shutdown must not panic or return an error.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v, want nil", err)
	}
}

// TestInitNoEndpointCustomService verifies that a custom OTEL_SERVICE_NAME is
// accepted when no endpoint is set (path that bypasses exporter creation).
func TestInitNoEndpointCustomService(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "my-purser")

	shutdown, err := telemetry.Init(context.Background())
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	_ = shutdown(context.Background())
}
