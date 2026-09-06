package server_test

// dual_approval_test.go — tests for dual-control deployment approval gates
// (AI Act Art.14). These complement the basic approval-flow tests in
// approvals_test.go; they focus on the vote-accumulation, quorum, self-approval,
// duplicate-vote, and expiry semantics.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// seedAdminToken creates an admin API key in reg and returns its raw token.
// The key hash is derived from the token with SHA-256.
func seedAdminToken(t *testing.T, reg registry.Registry, id, token string) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	if err := reg.CreateAPIKey(context.Background(), &registry.APIKey{
		ID:      id,
		Name:    id,
		KeyHash: hex.EncodeToString(sum[:]),
		Role:    "admin",
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed admin key %q: %v", id, err)
	}
}

// newDualApprovalServer creates a server licensed for deployment_approvals,
// seeds two distinct admin tokens, and returns (server, registry, token1, token2).
func newDualApprovalServer(t *testing.T) (*server.Server, registry.Registry, string, string) {
	t.Helper()
	reg := newReg(t)
	lic := newApprovalLicense(t)
	srv := server.New(reg, server.Config{Addr: ":0", License: lic})

	const tok1 = "admin-token-one"
	const tok2 = "admin-token-two"
	seedAdminToken(t, reg, "key-admin-1", tok1)
	seedAdminToken(t, reg, "key-admin-2", tok2)
	return srv, reg, tok1, tok2
}

// requireerHash is the hash that the server would store for the requester token.
func requesterHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// seedPendingApproval creates a pending approval for depID whose requester is
// NOT one of the admin tokens (so neither admin is blocked by the self-approval guard).
func seedPendingApproval(t *testing.T, reg registry.Registry, depID string, required int) {
	t.Helper()
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID:      depID,
		ModelID:           "llama3-8b",
		Requester:         "operator-hash-xyz", // distinct from both admin tokens
		RequiredApprovals: required,
	}); err != nil {
		t.Fatalf("seed approval %q: %v", depID, err)
	}
}

// TestDualApproval_RequiresSecondApprover verifies the dual-control workflow:
//   - With required_approvals=2, Admin-1 vote → quorum not reached, deploy
//     does not start.
//   - Admin-2 vote → quorum reached, response carries quorum_reached:true.
func TestDualApproval_RequiresSecondApprover(t *testing.T) {
	srv, reg, tok1, tok2 := newDualApprovalServer(t)
	seedPendingApproval(t, reg, "dep-dual", 2)

	// First vote (Admin-1): quorum not reached.
	rec := authPost(t, srv, "/api/v1/approvals/dep-dual/approve", tok1,
		map[string]string{"notes": "first LGTM"})
	if rec.Code != http.StatusOK {
		t.Fatalf("vote-1 status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body1 struct {
		QuorumReached   bool `json:"quorum_reached"`
		ApprovalsSoFar  int  `json:"approvals_so_far"`
		ApprovalsNeeded int  `json:"approvals_needed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body1); err != nil {
		t.Fatalf("decode vote-1 body: %v; raw=%s", err, rec.Body.String())
	}
	if body1.QuorumReached {
		t.Error("quorum_reached = true after first vote, want false")
	}
	if body1.ApprovalsSoFar != 1 {
		t.Errorf("approvals_so_far = %d, want 1", body1.ApprovalsSoFar)
	}
	if body1.ApprovalsNeeded != 2 {
		t.Errorf("approvals_needed = %d, want 2", body1.ApprovalsNeeded)
	}

	// Confirm the deployment status is still "pending" in the registry.
	a, err := reg.GetDeploymentApproval(context.Background(), "dep-dual")
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "pending" {
		t.Errorf("approval status = %q after first vote, want pending", a.Status)
	}

	// Second vote (Admin-2): quorum reached.
	rec = authPost(t, srv, "/api/v1/approvals/dep-dual/approve", tok2,
		map[string]string{"notes": "second LGTM"})
	if rec.Code != http.StatusOK {
		t.Fatalf("vote-2 status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body2 struct {
		QuorumReached   bool `json:"quorum_reached"`
		ApprovalsSoFar  int  `json:"approvals_so_far"`
		ApprovalsNeeded int  `json:"approvals_needed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body2); err != nil {
		t.Fatalf("decode vote-2 body: %v; raw=%s", err, rec.Body.String())
	}
	if !body2.QuorumReached {
		t.Error("quorum_reached = false after second vote, want true")
	}
	if body2.ApprovalsSoFar != 2 {
		t.Errorf("approvals_so_far = %d, want 2", body2.ApprovalsSoFar)
	}

	// Confirm the approval status is now "approved" in the registry.
	a, err = reg.GetDeploymentApproval(context.Background(), "dep-dual")
	if err != nil {
		t.Fatalf("get approval after quorum: %v", err)
	}
	if a.Status != "approved" {
		t.Errorf("approval status = %q after quorum, want approved", a.Status)
	}
}

// TestDualApproval_SelfApproval_Denied verifies that the requester cannot
// approve their own deployment (409 Conflict with error "self_approval_denied").
func TestDualApproval_SelfApproval_Denied(t *testing.T) {
	srv, reg, tok1, _ := newDualApprovalServer(t)

	// The requester is Admin-1 (tok1).
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID:      "dep-self",
		ModelID:           "llama3-8b",
		Requester:         requesterHash(tok1), // same as reviewer
		RequiredApprovals: 1,
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := authPost(t, srv, "/api/v1/approvals/dep-self/approve", tok1, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "self_approval_denied" {
		t.Errorf("error = %q, want self_approval_denied; body=%s", body["error"], rec.Body.String())
	}
}

// TestDualApproval_DuplicateVote_Denied verifies that a reviewer who has
// already voted cannot vote again (409 Conflict with error "already_voted").
func TestDualApproval_DuplicateVote_Denied(t *testing.T) {
	srv, reg, tok1, _ := newDualApprovalServer(t)
	seedPendingApproval(t, reg, "dep-dup", 2)

	// First vote succeeds.
	rec := authPost(t, srv, "/api/v1/approvals/dep-dup/approve", tok1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first vote status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Second vote from the same reviewer must be rejected.
	rec = authPost(t, srv, "/api/v1/approvals/dep-dup/approve", tok1, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dup-vote status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "already_voted" {
		t.Errorf("error = %q, want already_voted; body=%s", body["error"], rec.Body.String())
	}
}

// TestApproval_ExpiresAt_ReturnsGone verifies that an approve attempt on an
// expired approval returns 410 Gone.
func TestApproval_ExpiresAt_ReturnsGone(t *testing.T) {
	srv, reg, tok1, _ := newDualApprovalServer(t)

	// Create an approval that expired one hour ago.
	past := time.Now().Add(-time.Hour)
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID:      "dep-expired",
		ModelID:           "llama3-8b",
		Requester:         "operator-hash-xyz",
		RequiredApprovals: 1,
		ExpiresAt:         &past,
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := authPost(t, srv, "/api/v1/approvals/dep-expired/approve", tok1, nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "approval_expired" {
		t.Errorf("error = %q, want approval_expired; body=%s", body["error"], rec.Body.String())
	}
}
