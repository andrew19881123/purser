package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newApprovalLicense returns a license with the deployment_approvals feature.
func newApprovalLicense(t *testing.T) *license.License {
	t.Helper()
	now := time.Now().UTC()
	return signedLicense(t, license.Payload{
		Licensee: "Test Corp",
		Features: []string{"deployment_approvals"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
}

// newApprovalServer returns a server with the deployment_approvals feature
// licensed, and a registry with an admin API key seeded.
// Returns (server, reg, adminToken).
func newApprovalServer(t *testing.T) (*server.Server, registry.Registry, string) {
	t.Helper()
	reg := newReg(t)
	lic := newApprovalLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	// Seed an admin API key.
	const adminToken = "admin-secret-token-123"
	sum := sha256.Sum256([]byte(adminToken))
	hashHex := hex.EncodeToString(sum[:])
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      "key-admin",
		Name:    "admin",
		KeyHash: hashHex,
		Role:    "admin",
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed admin key: %v", err)
	}
	return srv, reg, adminToken
}

// authGet performs a GET with a Bearer token.
func authGet(t *testing.T, srv *server.Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// authPost performs a POST with a Bearer token and optional JSON body.
func authPost(t *testing.T, srv *server.Server, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestGetApprovals_NoLicense confirms that GET /api/v1/approvals returns 402
// when the deployment_approvals feature is not licensed.
func TestGetApprovals_NoLicense(t *testing.T) {
	srv := newEnterpriseServer(t, nil) // community edition
	rec := get(t, srv, "/api/v1/approvals")
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// Error should mention the feature name.
	if !strings.Contains(rec.Body.String(), "deployment_approvals") {
		t.Errorf("body should mention feature name; got=%s", rec.Body.String())
	}
}

// TestGetApprovals_Licensed_Empty confirms that GET /api/v1/approvals with a
// valid enterprise license returns {"approvals":[]} (never null) when empty.
func TestGetApprovals_Licensed_Empty(t *testing.T) {
	srv, _, _ := newApprovalServer(t)
	rec := get(t, srv, "/api/v1/approvals")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals []*registry.DeploymentApproval `json:"approvals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Approvals == nil {
		t.Errorf("approvals must be [] not null")
	}
	if len(body.Approvals) != 0 {
		t.Errorf("approvals len = %d, want 0", len(body.Approvals))
	}
}

// TestApproveDeployment_ViewerForbidden confirms that a viewer-role token
// cannot approve a deployment (403 Forbidden).
func TestApproveDeployment_ViewerForbidden(t *testing.T) {
	reg := newReg(t)
	lic := newApprovalLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	const viewerToken = "viewer-token-xyz"
	sum := sha256.Sum256([]byte(viewerToken))
	hashHex := hex.EncodeToString(sum[:])
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      "key-viewer",
		Name:    "viewer",
		KeyHash: hashHex,
		Role:    "viewer",
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed viewer key: %v", err)
	}

	// Seed a pending approval directly in the registry.
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID: "dep-abc",
		ModelID:      "model-x",
		Requester:    "some-requester-hash",
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := authPost(t, srv, "/api/v1/approvals/dep-abc/approve", viewerToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestApproveDeployment_AdminOK confirms that an admin can approve a pending
// deployment and the status is updated to "approved".
func TestApproveDeployment_AdminOK(t *testing.T) {
	srv, reg, adminToken := newApprovalServer(t)

	// Seed a pending approval.
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID: "dep-xyz",
		ModelID:      "llama3",
		Requester:    "requester-hash",
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := authPost(t, srv, "/api/v1/approvals/dep-xyz/approve", adminToken,
		map[string]string{"notes": "LGTM"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body registry.DeploymentApproval
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if body.Status != "approved" {
		t.Errorf("status = %q, want approved", body.Status)
	}
	if body.Notes != "LGTM" {
		t.Errorf("notes = %q, want LGTM", body.Notes)
	}
}

// TestApproveDeployment_AlreadyApproved confirms that approving an already-
// approved deployment returns 409 Conflict.
func TestApproveDeployment_AlreadyApproved(t *testing.T) {
	srv, reg, adminToken := newApprovalServer(t)

	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID: "dep-done",
		ModelID:      "llama3",
		Requester:    "hash",
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	// Approve first time.
	if err := reg.UpdateDeploymentApprovalStatus(context.Background(), "dep-done", "approved", "admin", "ok"); err != nil {
		t.Fatalf("first approve: %v", err)
	}

	// Second approve attempt.
	rec := authPost(t, srv, "/api/v1/approvals/dep-done/approve", adminToken, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetApproval_NotFound confirms that GET /api/v1/approvals/{id} returns
// 404 for an unknown deployment ID.
func TestGetApproval_NotFound(t *testing.T) {
	srv, _, _ := newApprovalServer(t)
	rec := get(t, srv, "/api/v1/approvals/nonexistent-id")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetApprovals_FilterByStatus confirms that ?status=pending only returns
// pending approvals.
func TestGetApprovals_FilterByStatus(t *testing.T) {
	srv, reg, _ := newApprovalServer(t)

	// Seed two pending and one approved.
	for _, id := range []string{"dep-p1", "dep-p2"} {
		if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
			DeploymentID: id, ModelID: "m1", Requester: "r",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID: "dep-approved", ModelID: "m2", Requester: "r",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := reg.UpdateDeploymentApprovalStatus(context.Background(), "dep-approved", "approved", "admin", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	rec := get(t, srv, "/api/v1/approvals?status=pending")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals []*registry.DeploymentApproval `json:"approvals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Approvals) != 2 {
		t.Errorf("approvals len = %d, want 2; body=%s", len(body.Approvals), rec.Body.String())
	}
	for _, a := range body.Approvals {
		if a.Status != "pending" {
			t.Errorf("got status %q, want pending", a.Status)
		}
	}
}
