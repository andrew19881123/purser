package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/server"
)

// stubReconcilerStatus implements server.ReconcilerStatusProvider for tests.
type stubReconcilerStatus struct {
	status reconciler.ReconcilerStatus
}

func (s *stubReconcilerStatus) Status() reconciler.ReconcilerStatus { return s.status }

// TestHandleReconcilerStatus_OK verifies that a configured reconciler returns
// 200 with the config and tracker fields matching the stub's values.
func TestHandleReconcilerStatus_OK(t *testing.T) {
	reg := newReg(t)
	stub := &stubReconcilerStatus{
		status: reconciler.ReconcilerStatus{
			Config: reconciler.ReconcilerConfigSnapshot{
				IntervalS:       10,
				NodeTimeoutS:    45,
				HysteresisS:     30,
				ActionCooldownS: 60,
			},
			Tracker: map[string]reconciler.ReconcilerEventSummary{
				"node_down":         {Tracked: 0, OldestAgeS: 0},
				"orphan_deployment": {Tracked: 1, OldestAgeS: 120},
			},
		},
	}
	srv := server.New(reg, server.Config{Reconciler: stub})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reconciler/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Config struct {
			IntervalS       int `json:"interval_s"`
			NodeTimeoutS    int `json:"node_timeout_s"`
			HysteresisS     int `json:"hysteresis_s"`
			ActionCooldownS int `json:"action_cooldown_s"`
		} `json:"config"`
		Tracker map[string]struct {
			Tracked    int     `json:"tracked"`
			OldestAgeS float64 `json:"oldest_age_s"`
		} `json:"tracker"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	if body.Config.IntervalS != 10 {
		t.Errorf("interval_s = %d, want 10", body.Config.IntervalS)
	}
	if body.Config.NodeTimeoutS != 45 {
		t.Errorf("node_timeout_s = %d, want 45", body.Config.NodeTimeoutS)
	}
	if body.Config.HysteresisS != 30 {
		t.Errorf("hysteresis_s = %d, want 30", body.Config.HysteresisS)
	}
	if body.Config.ActionCooldownS != 60 {
		t.Errorf("action_cooldown_s = %d, want 60", body.Config.ActionCooldownS)
	}
	if body.Tracker == nil {
		t.Fatal("tracker must not be null")
	}
	if nd, ok := body.Tracker["node_down"]; !ok || nd.Tracked != 0 {
		t.Errorf("tracker[node_down] = %+v, want {Tracked:0}", nd)
	}
	if od, ok := body.Tracker["orphan_deployment"]; !ok || od.Tracked != 1 {
		t.Errorf("tracker[orphan_deployment] = %+v, want {Tracked:1}", od)
	}
}

// TestHandleReconcilerStatus_NotConfigured verifies that a server without a
// reconciler returns 501 Not Implemented.
func TestHandleReconcilerStatus_NotConfigured(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{}) // no Reconciler

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reconciler/status", nil))

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleReconcilerStatus_ViewerAccess verifies that a viewer-role API key
// can access this read-only GET endpoint (RBAC: viewer-accessible).
func TestHandleReconcilerStatus_ViewerAccess(t *testing.T) {
	reg := newReg(t)
	stub := &stubReconcilerStatus{
		status: reconciler.ReconcilerStatus{
			Config:  reconciler.ReconcilerConfigSnapshot{IntervalS: 10},
			Tracker: map[string]reconciler.ReconcilerEventSummary{},
		},
	}

	// seedKeyWithRole is defined in rbac_test.go (same package server_test).
	viewerToken := seedKeyWithRole(t, reg, "key-reconciler-viewer", "reconciler-viewer", "viewer")
	srv := server.New(reg, server.Config{Reconciler: stub})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/reconciler/status", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("viewer GET: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}
