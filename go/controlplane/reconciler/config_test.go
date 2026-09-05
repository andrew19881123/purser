package reconciler_test

import (
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/reconciler"
)

func TestConfigFromEnv_PicksUpInterval(t *testing.T) {
	t.Setenv("PURSER_RECONCILER_INTERVAL", "30s")

	cfg := reconciler.ConfigFromEnv()
	if cfg.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", cfg.Interval)
	}
}

func TestConfigFromEnv_PicksUpNodeOfflineAfter(t *testing.T) {
	t.Setenv("PURSER_RECONCILER_NODE_OFFLINE_AFTER", "2m")

	cfg := reconciler.ConfigFromEnv()
	if cfg.NodeTimeout != 2*time.Minute {
		t.Errorf("NodeTimeout = %v, want 2m", cfg.NodeTimeout)
	}
}

func TestConfigFromEnv_PicksUpHysteresis(t *testing.T) {
	t.Setenv("PURSER_RECONCILER_HYSTERESIS", "1m")

	cfg := reconciler.ConfigFromEnv()
	if cfg.Hysteresis != time.Minute {
		t.Errorf("Hysteresis = %v, want 1m", cfg.Hysteresis)
	}
}

func TestConfigFromEnv_PicksUpActionCooldown(t *testing.T) {
	t.Setenv("PURSER_RECONCILER_ACTION_COOLDOWN", "5m")

	cfg := reconciler.ConfigFromEnv()
	if cfg.ActionCooldown != 5*time.Minute {
		t.Errorf("ActionCooldown = %v, want 5m", cfg.ActionCooldown)
	}
}

func TestConfigFromEnv_UnsetVarsUseDefaults(t *testing.T) {
	// Ensure none of the vars are set.
	t.Setenv("PURSER_RECONCILER_INTERVAL", "")
	t.Setenv("PURSER_RECONCILER_NODE_OFFLINE_AFTER", "")
	t.Setenv("PURSER_RECONCILER_HYSTERESIS", "")
	t.Setenv("PURSER_RECONCILER_ACTION_COOLDOWN", "")

	cfg := reconciler.ConfigFromEnv()
	def := reconciler.DefaultConfig()

	if cfg.Interval != def.Interval {
		t.Errorf("Interval = %v, want default %v", cfg.Interval, def.Interval)
	}
	if cfg.NodeTimeout != def.NodeTimeout {
		t.Errorf("NodeTimeout = %v, want default %v", cfg.NodeTimeout, def.NodeTimeout)
	}
	if cfg.Hysteresis != def.Hysteresis {
		t.Errorf("Hysteresis = %v, want default %v", cfg.Hysteresis, def.Hysteresis)
	}
	if cfg.ActionCooldown != def.ActionCooldown {
		t.Errorf("ActionCooldown = %v, want default %v", cfg.ActionCooldown, def.ActionCooldown)
	}
}

func TestConfigFromEnv_InvalidDurationFallsBack(t *testing.T) {
	t.Setenv("PURSER_RECONCILER_INTERVAL", "not-a-duration")

	cfg := reconciler.ConfigFromEnv()
	def := reconciler.DefaultConfig()

	if cfg.Interval != def.Interval {
		t.Errorf("Interval = %v after bad input, want default %v", cfg.Interval, def.Interval)
	}
}
