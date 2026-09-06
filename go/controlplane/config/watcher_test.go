package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/config"
)

const watcherYAML1 = `apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: test
`

const watcherYAML2 = `apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: updated
`

// TestWatcher_FiresOnChange verifies that the watcher:
//  1. fires onChange immediately on Run with the initial file content,
//  2. fires again when the file changes, and
//  3. does NOT fire a second time when the file is unchanged.
func TestWatcher_FiresOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "purser.yaml")

	if err := os.WriteFile(path, []byte(watcherYAML1), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fired := make(chan *config.ClusterConfig, 4)
	w := config.NewWatcher(path, 10*time.Millisecond, func(c *config.ClusterConfig) {
		fired <- c
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Should fire immediately with initial config.
	select {
	case cfg := <-fired:
		if cfg.Metadata.Name != "test" {
			t.Errorf("initial fire: Metadata.Name = %q, want %q", cfg.Metadata.Name, "test")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher did not fire on initial load")
	}

	// Update file — should fire again.
	if err := os.WriteFile(path, []byte(watcherYAML2), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	select {
	case cfg := <-fired:
		if cfg.Metadata.Name != "updated" {
			t.Errorf("change fire: Metadata.Name = %q, want %q", cfg.Metadata.Name, "updated")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher did not fire on file change")
	}

	// No extra fires for same content.
	select {
	case extra := <-fired:
		t.Errorf("watcher fired spuriously; Metadata.Name = %q", extra.Metadata.Name)
	case <-time.After(100 * time.Millisecond):
		// Expected: no spurious fire.
	}
}

// TestWatcher_IgnoresInvalidYAML verifies that a file with invalid YAML does
// not trigger onChange and does not panic or crash the watcher.
func TestWatcher_IgnoresInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "purser.yaml")

	if err := os.WriteFile(path, []byte("not: valid: yaml: {{{"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fired := make(chan struct{}, 4)
	w := config.NewWatcher(path, 10*time.Millisecond, func(*config.ClusterConfig) {
		fired <- struct{}{}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// Run to completion (ctx will expire); watcher must not panic.
	_ = w.Run(ctx)

	select {
	case <-fired:
		t.Fatal("onChange called for invalid YAML")
	default:
		// Expected: no fire.
	}
}

// TestWatcher_MissingFile verifies that a missing file is non-fatal: the
// watcher does not fire onChange, does not panic, and continues running so
// it can pick up the file once it appears (Kubernetes ConfigMap late-mount).
func TestWatcher_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.yaml")

	fired := make(chan struct{}, 4)
	w := config.NewWatcher(path, 10*time.Millisecond, func(*config.ClusterConfig) {
		fired <- struct{}{}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	select {
	case <-fired:
		t.Fatal("onChange called for missing file")
	default:
		// Expected: no fire.
	}
}

// TestWatcher_LateMount verifies the Kubernetes ConfigMap late-mount scenario:
// file is absent at startup, appears later, watcher fires when it does.
func TestWatcher_LateMount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "purser.yaml")

	fired := make(chan *config.ClusterConfig, 4)
	w := config.NewWatcher(path, 20*time.Millisecond, func(c *config.ClusterConfig) {
		fired <- c
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Wait a bit, then create the file.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte(watcherYAML1), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case cfg := <-fired:
		if cfg.Metadata.Name != "test" {
			t.Errorf("Metadata.Name = %q, want %q", cfg.Metadata.Name, "test")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher did not fire after file appeared")
	}
}
