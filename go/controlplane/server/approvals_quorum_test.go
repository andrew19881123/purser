package server_test

// approvals_quorum_test.go — TDD tests for dual-control quorum (AI Act Art.14).
//
// Coverage:
//   TestQuorum_SingleApprover_DefaultBehavior — min=1 → immediate (backward compat)
//   TestQuorum_TwoRequired_OneApproves        — still pending after 1 of 2
//   TestQuorum_TwoRequired_TwoApprove         — released after 2 of 2
//   TestQuorum_ReviewerKeyRestriction         — non-reviewer approval ignored
//   TestQuorum_DistinctApprovers_Duplicate    — same actor twice → 409
//   TestQuorum_StatusShowsProgress            — GET /approvals/{id} shows quorum

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/config"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newQuorumServer creates a server with the deployment_approvals feature
// licensed and the supplied QuorumConfig applied. It seeds admin key pairs for
// tok1 (key-admin-q1) and tok2 (key-admin-q2) and returns the server along
// with both tokens.
func newQuorumServer(t *testing.T, q *config.QuorumConfig) (*server.Server, registry.Registry, string, string) {
	t.Helper()
	reg := newReg(t)
	lic := newApprovalLicense(t)
	srv := server.New(reg, server.Config{
		Addr:    ":0",
		License: lic,
		Quorum:  q,
	})
	const tok1 = "quorum-admin-token-one"
	const tok2 = "quorum-admin-token-two"
	seedAdminToken(t, reg, "key-admin-q1", tok1)
	seedAdminToken(t, reg, "key-admin-q2", tok2)
	return srv, reg, tok1, tok2
}

// seedQuorumApproval creates a pending approval with RequiredApprovals=1
// (the quorum minimum is overridden by the server's QuorumConfig).
func seedQuorumApproval(t *testing.T, reg registry.Registry, depID string) {
	t.Helper()
	if err := reg.RequestDeploymentApproval(context.Background(), &registry.DeploymentApproval{
		DeploymentID: depID,
		ModelID:      "llama3-8b",
		Requester:    "operator-hash-xyz",
		// RequiredApprovals intentionally left 0 — the server's QuorumConfig.MinApprovers
		// should override this via CheckApprovalQuorumFiltered.
	}); err != nil {
		t.Fatalf("seed quorum approval %q: %v", depID, err)
	}
}

// keyHash returns the SHA-256 hex hash for a raw token string.
func keyHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// TestQuorum_SingleApprover_DefaultBehavior
// ---------------------------------------------------------------------------

// TestQuorum_SingleApprover_DefaultBehavior verifies that with min_approvers=1
// (the default), a single admin approval immediately reaches quorum —
// preserving backward-compatible behaviour.
func TestQuorum_SingleApprover_DefaultBehavior(t *testing.T) {
	srv, reg, tok1, _ := newQuorumServer(t, &config.QuorumConfig{
		MinApprovers:    1,
		RequireDistinct: true,
	})
	seedQuorumApproval(t, reg, "dep-q-single")

	rec := authPost(t, srv, "/api/v1/approvals/dep-q-single/approve", tok1,
		map[string]string{"notes": "single approver LGTM"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		QuorumReached   bool `json:"quorum_reached"`
		ApprovalsSoFar  int  `json:"approvals_so_far"`
		ApprovalsNeeded int  `json:"approvals_needed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if !body.QuorumReached {
		t.Error("quorum_reached = false, want true (min_approvers=1)")
	}
	if body.ApprovalsSoFar != 1 {
		t.Errorf("approvals_so_far = %d, want 1", body.ApprovalsSoFar)
	}
	if body.ApprovalsNeeded != 1 {
		t.Errorf("approvals_needed = %d, want 1", body.ApprovalsNeeded)
	}
	// Registry record must have transitioned.
	a, err := reg.GetDeploymentApproval(context.Background(), "dep-q-single")
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "approved" {
		t.Errorf("registry status = %q, want approved", a.Status)
	}
}

// ---------------------------------------------------------------------------
// TestQuorum_TwoRequired_OneApproves
// ---------------------------------------------------------------------------

// TestQuorum_TwoRequired_OneApproves verifies that with min_approvers=2, the
// first vote is recorded but quorum is not reached and the deployment stays
// pending.
func TestQuorum_TwoRequired_OneApproves(t *testing.T) {
	srv, reg, tok1, _ := newQuorumServer(t, &config.QuorumConfig{
		MinApprovers:    2,
		RequireDistinct: true,
	})
	seedQuorumApproval(t, reg, "dep-q-partial")

	rec := authPost(t, srv, "/api/v1/approvals/dep-q-partial/approve", tok1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		QuorumReached   bool `json:"quorum_reached"`
		ApprovalsSoFar  int  `json:"approvals_so_far"`
		ApprovalsNeeded int  `json:"approvals_needed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if body.QuorumReached {
		t.Error("quorum_reached = true after first of two votes, want false")
	}
	if body.ApprovalsSoFar != 1 {
		t.Errorf("approvals_so_far = %d, want 1", body.ApprovalsSoFar)
	}
	if body.ApprovalsNeeded != 2 {
		t.Errorf("approvals_needed = %d, want 2", body.ApprovalsNeeded)
	}
	// Deployment must still be pending.
	a, err := reg.GetDeploymentApproval(context.Background(), "dep-q-partial")
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "pending" {
		t.Errorf("registry status = %q after first vote, want pending", a.Status)
	}
}

// ---------------------------------------------------------------------------
// TestQuorum_TwoRequired_TwoApprove
// ---------------------------------------------------------------------------

// TestQuorum_TwoRequired_TwoApprove verifies that with min_approvers=2, the
// second distinct approval reaches quorum and transitions the record to approved.
func TestQuorum_TwoRequired_TwoApprove(t *testing.T) {
	srv, reg, tok1, tok2 := newQuorumServer(t, &config.QuorumConfig{
		MinApprovers:    2,
		RequireDistinct: true,
	})
	seedQuorumApproval(t, reg, "dep-q-dual")

	// First vote — not enough.
	rec := authPost(t, srv, "/api/v1/approvals/dep-q-dual/approve", tok1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vote-1 status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Second vote from a different admin — quorum reached.
	rec = authPost(t, srv, "/api/v1/approvals/dep-q-dual/approve", tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vote-2 status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		QuorumReached   bool `json:"quorum_reached"`
		ApprovalsSoFar  int  `json:"approvals_so_far"`
		ApprovalsNeeded int  `json:"approvals_needed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if !body.QuorumReached {
		t.Error("quorum_reached = false after second vote, want true")
	}
	if body.ApprovalsSoFar != 2 {
		t.Errorf("approvals_so_far = %d, want 2", body.ApprovalsSoFar)
	}
	// Registry record must be approved.
	a, err := reg.GetDeploymentApproval(context.Background(), "dep-q-dual")
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "approved" {
		t.Errorf("registry status = %q after quorum, want approved", a.Status)
	}
}

// ---------------------------------------------------------------------------
// TestQuorum_ReviewerKeyRestriction
// ---------------------------------------------------------------------------

// TestQuorum_ReviewerKeyRestriction verifies that when reviewer_keys is set,
// a vote from a non-designated admin is recorded but does NOT count toward
// quorum. Only a vote from a designated reviewer completes the gate.
func TestQuorum_ReviewerKeyRestriction(t *testing.T) {
	// Only key-admin-q2 is a designated reviewer.
	srv, reg, tok1, tok2 := newQuorumServer(t, &config.QuorumConfig{
		MinApprovers:    1,
		ReviewerKeys:    []string{"key-admin-q2"},
		RequireDistinct: true,
	})
	seedQuorumApproval(t, reg, "dep-q-restricted")

	// tok1 (key-admin-q1) is NOT a designated reviewer — vote is accepted but
	// quorum should NOT be reached.
	rec := authPost(t, srv, "/api/v1/approvals/dep-q-restricted/approve", tok1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-reviewer vote status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body1 struct {
		QuorumReached  bool `json:"quorum_reached"`
		ApprovalsSoFar int  `json:"approvals_so_far"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body1); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if body1.QuorumReached {
		t.Error("quorum_reached = true after non-reviewer vote, want false")
	}
	if body1.ApprovalsSoFar != 0 {
		t.Errorf("approvals_so_far = %d (counted non-reviewer vote), want 0", body1.ApprovalsSoFar)
	}

	// Deployment must still be pending.
	a, err := reg.GetDeploymentApproval(context.Background(), "dep-q-restricted")
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "pending" {
		t.Errorf("registry status = %q after non-reviewer vote, want pending", a.Status)
	}

	// tok2 (key-admin-q2) IS a designated reviewer — quorum reached after this vote.
	rec = authPost(t, srv, "/api/v1/approvals/dep-q-restricted/approve", tok2, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reviewer vote status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body2 struct {
		QuorumReached  bool `json:"quorum_reached"`
		ApprovalsSoFar int  `json:"approvals_so_far"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body2); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if !body2.QuorumReached {
		t.Error("quorum_reached = false after designated reviewer vote, want true")
	}
	if body2.ApprovalsSoFar != 1 {
		t.Errorf("approvals_so_far = %d, want 1 (only the reviewer vote counts)", body2.ApprovalsSoFar)
	}
}

// ---------------------------------------------------------------------------
// TestQuorum_DistinctApprovers_Duplicate
// ---------------------------------------------------------------------------

// TestQuorum_DistinctApprovers_Duplicate verifies that when require_distinct is
// true (the default), a reviewer who has already voted cannot vote again and
// receives 409 Conflict with error "already_voted".
func TestQuorum_DistinctApprovers_Duplicate(t *testing.T) {
	srv, reg, tok1, _ := newQuorumServer(t, &config.QuorumConfig{
		MinApprovers:    2,
		RequireDistinct: true,
	})
	seedQuorumApproval(t, reg, "dep-q-dup")

	// First vote succeeds.
	rec := authPost(t, srv, "/api/v1/approvals/dep-q-dup/approve", tok1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first vote status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Second vote from the same reviewer must be rejected with 409.
	rec = authPost(t, srv, "/api/v1/approvals/dep-q-dup/approve", tok1, nil)
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

// ---------------------------------------------------------------------------
// TestQuorum_StatusShowsProgress
// ---------------------------------------------------------------------------

// TestQuorum_StatusShowsProgress verifies that GET /api/v1/approvals/{id}
// returns a "quorum" block showing the current received/required counts and
// the list of approvers so far.
func TestQuorum_StatusShowsProgress(t *testing.T) {
	srv, reg, tok1, _ := newQuorumServer(t, &config.QuorumConfig{
		MinApprovers:    2,
		RequireDistinct: true,
	})
	seedQuorumApproval(t, reg, "dep-q-status")

	// Cast one vote.
	rec := authPost(t, srv, "/api/v1/approvals/dep-q-status/approve", tok1,
		map[string]string{"notes": "LGTM"})
	if rec.Code != http.StatusOK {
		t.Fatalf("vote status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// GET the approval — should include quorum block with received=1, required=2.
	rec = authGet(t, srv, "/api/v1/approvals/dep-q-status", tok1)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Status string `json:"status"`
		Quorum *struct {
			Required  int `json:"required"`
			Received  int `json:"received"`
			Remaining int `json:"remaining"`
			Approvers []struct {
				Actor      string    `json:"actor"`
				ApprovedAt time.Time `json:"approved_at"`
			} `json:"approvers"`
		} `json:"quorum"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET body: %v; raw=%s", err, rec.Body.String())
	}
	if body.Quorum == nil {
		t.Fatalf("quorum field missing from GET response; body=%s", rec.Body.String())
	}
	if body.Quorum.Required != 2 {
		t.Errorf("quorum.required = %d, want 2", body.Quorum.Required)
	}
	if body.Quorum.Received != 1 {
		t.Errorf("quorum.received = %d, want 1", body.Quorum.Received)
	}
	if body.Quorum.Remaining != 1 {
		t.Errorf("quorum.remaining = %d, want 1", body.Quorum.Remaining)
	}
	if len(body.Quorum.Approvers) != 1 {
		t.Errorf("quorum.approvers len = %d, want 1", len(body.Quorum.Approvers))
	} else {
		// Approver actor should be the SHA-256 hash of tok1.
		want := keyHash("quorum-admin-token-one")
		if body.Quorum.Approvers[0].Actor != want {
			t.Errorf("approvers[0].actor = %q, want %q", body.Quorum.Approvers[0].Actor, want)
		}
	}
}
