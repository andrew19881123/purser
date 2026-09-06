package reconciler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
)

// TestWebhookDeliveredOnApprovalRequired verifies that an EventNodeDown that
// lands in the ApprovalRequired path delivers a well-formed webhook request to
// the configured URL.
func TestWebhookDeliveredOnApprovalRequired(t *testing.T) {
	var received atomic.Int32
	var lastBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("webhook: method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("webhook: Content-Type = %s, want application/json", ct)
		}
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		lastBody = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := openReg(t)
	ctx := context.Background()

	// Seed a model and node so the reconciler can find them.
	if err := reg.CreateModel(ctx, &registry.Model{ID: "mdl-1"}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	nodeID := "node-wh-1"
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:       nodeID,
		Hostname: "wh-host",
		State:    "NODE_STATE_UNREACHABLE", // triggers EventNodeDown
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Create an ACTIVE deployment referencing the unreachable node.
	depID := seedActiveDeployment(t, reg, "mdl-1", nodeID, false)

	// Reconciler: ApprovalRequired for NodeDown, hysteresis bypassed.
	cfg := reconciler.Config{
		Interval:         10 * time.Second,
		FailureThreshold: 1,
		Hysteresis:       0,
		NodeTimeout:      45 * time.Second,
		ActionCooldown:   0,
		Levels: map[reconciler.EventType]reconciler.AutomationLevel{
			reconciler.EventNodeDown: reconciler.AutomationApprovalRequired,
		},
		WebhookURL:     srv.URL,
		WebhookRetries: 1,
	}
	rc := reconciler.New(reg, nil, cfg)

	// Fast-forward clock so hysteresis passes immediately.
	rc.SetClock(func() time.Time { return time.Now().Add(time.Hour) })

	_, err := rc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Give the goroutine time to deliver.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && received.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if received.Load() == 0 {
		t.Fatal("webhook: no request received")
	}

	// Validate payload fields.
	var payload map[string]any
	if err := json.Unmarshal(lastBody, &payload); err != nil {
		t.Fatalf("webhook: parse body: %v; raw=%s", err, lastBody)
	}
	checks := map[string]string{
		"event":         "approval_required",
		"event_type":    "node_down",
		"node_id":       nodeID,
		"deployment_id": depID,
	}
	for field, want := range checks {
		if got, _ := payload[field].(string); got != want {
			t.Errorf("webhook payload[%q] = %q, want %q", field, got, want)
		}
	}
	for _, field := range []string{"timestamp", "purser_version", "message"} {
		if v, _ := payload[field].(string); v == "" {
			t.Errorf("webhook payload[%q] is empty", field)
		}
	}
}

// TestWebhookRetryOnFailure verifies that the webhook is retried when the
// server returns a non-2xx status on the first attempt.
func TestWebhookRetryOnFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 2 {
			// First call: return 503 to force a retry.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := openReg(t)
	ctx := context.Background()

	if err := reg.CreateModel(ctx, &registry.Model{ID: "mdl-retry"}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	nodeID := "node-retry"
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:    nodeID,
		State: "NODE_STATE_UNREACHABLE",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	seedActiveDeployment(t, reg, "mdl-retry", nodeID, false)

	cfg := reconciler.Config{
		Interval:         10 * time.Second,
		FailureThreshold: 1,
		Hysteresis:       0,
		NodeTimeout:      45 * time.Second,
		ActionCooldown:   0,
		Levels: map[reconciler.EventType]reconciler.AutomationLevel{
			reconciler.EventNodeDown: reconciler.AutomationApprovalRequired,
		},
		WebhookURL:     srv.URL,
		WebhookRetries: 3,
	}
	rc := reconciler.New(reg, nil, cfg)
	rc.SetClock(func() time.Time { return time.Now().Add(time.Hour) })

	_, err := rc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Wait long enough for both attempts (first fails, second succeeds).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(50 * time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Errorf("webhook: want >= 2 attempts, got %d", calls.Load())
	}
}

// TestWebhookNotFiredForAutoLevel verifies that no webhook is delivered when
// the automation level is AutomationAuto (not ApprovalRequired).
// The scenario: EventNodeDown fires with level AutomationAuto; the reconciler
// cannot act (nil actuator) but must NOT send a webhook.
func TestWebhookNotFiredForAutoLevel(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := openReg(t)
	ctx := context.Background()
	if err := reg.CreateModel(ctx, &registry.Model{ID: "mdl-auto"}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	nodeID := "node-auto"
	// UNREACHABLE → EventNodeDown will fire.
	if err := reg.CreateNode(ctx, &registry.Node{
		ID:    nodeID,
		State: "NODE_STATE_UNREACHABLE",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	seedActiveDeployment(t, reg, "mdl-auto", nodeID, false)

	cfg := reconciler.Config{
		Interval:         10 * time.Second,
		FailureThreshold: 1,
		Hysteresis:       0,
		NodeTimeout:      45 * time.Second,
		ActionCooldown:   0,
		// EventNodeDown → AutomationAuto: the reconciler tries to act
		// autonomously. With nil actuator it logs and returns false —
		// crucially, no webhook should fire (webhook is only for
		// ApprovalRequired).
		Levels: map[reconciler.EventType]reconciler.AutomationLevel{
			reconciler.EventNodeDown: reconciler.AutomationAuto,
		},
		WebhookURL:     srv.URL,
		WebhookRetries: 1,
	}
	// No actuator: Auto-level act fails, but webhook must NOT fire.
	rc := reconciler.New(reg, nil, cfg)
	rc.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	_, err := rc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Brief pause to confirm no delivery.
	time.Sleep(200 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("webhook fired for Auto level, want 0 calls, got %d", calls.Load())
	}
}
