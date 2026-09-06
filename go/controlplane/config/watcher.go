package config

import (
	"context"
	"crypto/sha256"
	"os"
	"time"
)

// Watcher polls a purser.yaml file and fires onChange when its content changes.
// It uses polling (not inotify) for simplicity and portability: polling works
// in containers, on network filesystems, and across platforms without extra
// dependencies. It is also robust to file replacement (e.g. kubectl ConfigMap
// atomic-rename) which inotify misses on some kernels.
type Watcher struct {
	path     string
	interval time.Duration
	lastHash [32]byte
	onChange func(*ClusterConfig)
}

// NewWatcher returns a Watcher that polls path every interval and calls
// onChange with the parsed ClusterConfig whenever the file's SHA-256 hash
// changes. The caller owns the goroutine — call Run to start polling.
func NewWatcher(path string, interval time.Duration, onChange func(*ClusterConfig)) *Watcher {
	return &Watcher{path: path, interval: interval, onChange: onChange}
}

// Run starts the watch loop. It blocks until ctx is cancelled, then returns
// ctx.Err(). An initial check is performed immediately before the first tick
// so that onChange fires at startup without waiting a full interval.
//
// Errors from individual polls (missing file, invalid YAML) are non-fatal:
// the watcher logs nothing internally — callers should wrap the onChange
// callback with their own error handling. The hash is only updated on a
// successful parse, so a temporarily invalid file retries on the next tick.
func (w *Watcher) Run(ctx context.Context) error {
	// Initial load — non-fatal: file may not exist yet (GitOps late-mount).
	_ = w.check()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = w.check()
		}
	}
}

// check reads the file, hashes it, and calls onChange when the hash differs
// from the last successful parse. Returns a non-nil error on read or parse
// failure; the hash is not updated on parse failure so the file is retried.
func (w *Watcher) check() error {
	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	h := sha256.Sum256(data)
	if h == w.lastHash {
		return nil // no change
	}
	cfg, err := Load(data)
	if err != nil {
		// Do not update the hash: keep retrying until the file is valid.
		return err
	}
	w.lastHash = h
	w.onChange(cfg)
	return nil
}
