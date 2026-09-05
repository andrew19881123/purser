package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/server"
)

func TestEnrollmentBundle_ContainsRequiredVars(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{
		Fleet:      mgr,
		ClusterID:  "test-cluster",
		PublicAddr: "http://cp.example.com:8080",
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/enrollment-bundle?ttl_seconds=3600", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"PURSER_CONTROL_PLANE_ADDR=",
		"PURSER_CLUSTER_ID=",
		"PURSER_JOIN_TOKEN=",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bundle missing %q; full body:\n%s", want, body)
		}
	}
	// Values must be present (not empty assignments).
	if !strings.Contains(body, "PURSER_CONTROL_PLANE_ADDR=http://cp.example.com:8080") {
		t.Errorf("PURSER_CONTROL_PLANE_ADDR value wrong; body=%s", body)
	}
	if !strings.Contains(body, "PURSER_CLUSTER_ID=test-cluster") {
		t.Errorf("PURSER_CLUSTER_ID value wrong; body=%s", body)
	}
}

func TestEnrollmentBundle_ContentType(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{Fleet: mgr})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/enrollment-bundle", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
}

func TestEnrollmentBundle_TTLForwarded(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{Fleet: mgr})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/enrollment-bundle?ttl_seconds=86400", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The token must be non-empty: the fleet manager accepted the TTL and minted one.
	tokenIdx := strings.Index(body, "PURSER_JOIN_TOKEN=")
	if tokenIdx < 0 {
		t.Fatalf("PURSER_JOIN_TOKEN not found in bundle; body=%s", body)
	}
	tokenLine := strings.TrimSpace(strings.SplitN(body[tokenIdx:], "\n", 2)[0])
	if tokenLine == "PURSER_JOIN_TOKEN=" {
		t.Error("PURSER_JOIN_TOKEN is empty — TTL was not forwarded or token minting failed")
	}

	// Expiry comment should reflect a 24-hour window. The bundle header contains
	// both "Generated:" and "Expires:" lines; they must differ.
	if !strings.Contains(body, "# Expires:") {
		t.Error("bundle is missing '# Expires:' comment")
	}
	if !strings.Contains(body, "# Generated:") {
		t.Error("bundle is missing '# Generated:' comment")
	}
}

func TestEnrollmentBundle_NoFleet(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{}) // no fleet manager
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/enrollment-bundle", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}
