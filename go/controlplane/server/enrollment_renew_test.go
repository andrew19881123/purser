package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
)

// enrollRequest is the JSON body for POST /api/v1/enrollment/renew.
type enrollRequest struct {
	NodeID string `json:"node_id"`
}

// renewResponse is the successful JSON response from the renewal endpoint.
type renewResponse struct {
	CertPEM   string `json:"certificate_pem"`
	ExpiresAt string `json:"expires_at"`
}

// enrollNode uses the fleet manager to fully enroll a node and returns its ID.
func enrollNode(t *testing.T, mgr *fleet.Manager, nodeID string) {
	t.Helper()
	n := &registry.Node{
		ID:       nodeID,
		Hostname: nodeID,
		State:    fleet.NodeStateEnrolled,
	}
	hw := &purserv1.HardwareProfile{NodeId: nodeID, Hostname: nodeID}
	// Obtain a join token, then call Join to create the node + issue a cert.
	tok, err := mgr.GenerateJoinToken(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("GenerateJoinToken: %v", err)
	}
	_ = n // use the registry node shape for reference only
	if _, err := mgr.Join(context.Background(), tok.Token, hw, "", ""); err != nil {
		t.Fatalf("Join(%q): %v", nodeID, err)
	}
}

// TestEnrollmentRenew_NodeNotFound verifies that an unknown node returns 404.
func TestEnrollmentRenew_NodeNotFound(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{Fleet: mgr})

	body, _ := json.Marshal(enrollRequest{NodeID: "nonexistent-node"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollment/renew", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rec.Body.String())
	}
	if resp["error"] != "not_found" {
		t.Errorf("error code = %q, want %q", resp["error"], "not_found")
	}
}

// TestEnrollmentRenew_TooEarly verifies that a node with a fresh cert (> 60 days
// remaining) gets a 409 Conflict response.
func TestEnrollmentRenew_TooEarly(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)

	// Enroll the node — this issues a cert with the default 90-day TTL, which
	// is > 60 days, so the renewal should be rejected.
	enrollNode(t, mgr, "node-fresh")

	srv := server.New(reg, server.Config{Fleet: mgr})

	body, _ := json.Marshal(enrollRequest{NodeID: "node-fresh"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollment/renew", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rec.Body.String())
	}
	if resp["error"] != "cert_not_yet_expiring" {
		t.Errorf("error code = %q, want %q", resp["error"], "cert_not_yet_expiring")
	}
}

// TestEnrollmentRenew_Success verifies that a node whose cert is close to expiry
// receives a new cert and a 200 OK.
func TestEnrollmentRenew_Success(t *testing.T) {
	reg := newReg(t)

	// Build a CA with a very short leaf TTL so the issued cert is immediately
	// eligible for renewal (< 60 days remaining).
	ca, err := pki.New(context.Background(), reg, pki.Options{
		LeafTTL: 30 * 24 * time.Hour, // 30 days — below the 60-day guard
	})
	if err != nil {
		t.Fatalf("pki.New: %v", err)
	}
	mgr := fleet.NewWithSecret(reg, ca, []byte("test-secret"))

	// Enroll with the short-TTL CA so the cert is < 60 days remaining.
	tok, err := mgr.GenerateJoinToken(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("GenerateJoinToken: %v", err)
	}
	hw := &purserv1.HardwareProfile{NodeId: "node-expiring", Hostname: "node-expiring"}
	if _, err := mgr.Join(context.Background(), tok.Token, hw, "", ""); err != nil {
		t.Fatalf("Join: %v", err)
	}

	srv := server.New(reg, server.Config{Fleet: mgr})

	body, _ := json.Marshal(enrollRequest{NodeID: "node-expiring"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollment/renew", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp renewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rec.Body.String())
	}
	if !strings.HasPrefix(resp.CertPEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("certificate_pem does not look like a PEM cert; value=%q", resp.CertPEM)
	}
	if resp.ExpiresAt == "" {
		t.Error("expires_at is empty")
	}
	// Verify the returned expiry is in the future.
	exp, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at %q: %v", resp.ExpiresAt, err)
	}
	if !exp.After(time.Now()) {
		t.Errorf("expires_at %s is not in the future", resp.ExpiresAt)
	}
}

// TestEnrollmentRenew_NoFleet verifies that 501 is returned when no fleet is configured.
func TestEnrollmentRenew_NoFleet(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{}) // no fleet

	body, _ := json.Marshal(enrollRequest{NodeID: "any"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollment/renew", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

// TestEnrollmentRenew_MissingNodeID verifies that an empty node_id returns 400.
func TestEnrollmentRenew_MissingNodeID(t *testing.T) {
	reg := newReg(t)
	mgr := newFleetManager(t, reg)
	srv := server.New(reg, server.Config{Fleet: mgr})

	body, _ := json.Marshal(enrollRequest{NodeID: ""})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollment/renew", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
