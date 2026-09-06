package telemetry

// Internal tests for buildSampler (unexported). Using package telemetry
// (not telemetry_test) gives access to the unexported function directly.

import (
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestBuildSampler_DefaultIsAlwaysOn(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	s := buildSampler()
	want := sdktrace.AlwaysSample().Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q (AlwaysSample)", s.Description(), want)
	}
}

func TestBuildSampler_AlwaysOn(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	s := buildSampler()
	want := sdktrace.AlwaysSample().Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q", s.Description(), want)
	}
}

func TestBuildSampler_AlwaysOff(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	s := buildSampler()
	want := sdktrace.NeverSample().Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q", s.Description(), want)
	}
}

func TestBuildSampler_TraceIDRatio(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.5")

	s := buildSampler()
	want := sdktrace.TraceIDRatioBased(0.5).Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q", s.Description(), want)
	}
}

// TestBuildSampler_TraceIDRatioBadArg verifies that an unparseable
// OTEL_TRACES_SAMPLER_ARG defaults to ratio 1.0 (100% sampling).
func TestBuildSampler_TraceIDRatioBadArg(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "not-a-float")

	s := buildSampler()
	want := sdktrace.TraceIDRatioBased(1.0).Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q (default ratio 1.0)", s.Description(), want)
	}
}

func TestBuildSampler_ParentBasedTraceIDRatio(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.5")

	s := buildSampler()
	want := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.5)).Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q", s.Description(), want)
	}
}

func TestBuildSampler_ParentBasedAlwaysOff(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_off")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	s := buildSampler()
	want := sdktrace.ParentBased(sdktrace.NeverSample()).Description()
	if s.Description() != want {
		t.Errorf("buildSampler() description = %q, want %q", s.Description(), want)
	}
}
