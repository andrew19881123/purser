// approvals.go — deployment approval gates (AI Act Art.14 human oversight).
//
// Enterprise-gated ("deployment_approvals" feature): when active, every deploy
// request queues a pending approval record instead of launching the rollout
// immediately. An admin must call POST /api/v1/approvals/{id}/approve to
// release the deployment.
//
// Endpoints:
//
//	GET  /api/v1/approvals                         — list approvals (admin/viewer)
//	GET  /api/v1/approvals/{deploymentId}           — get one approval
//	POST /api/v1/approvals/{deploymentId}/approve   — approve (admin only)
//	POST /api/v1/approvals/{deploymentId}/reject    — reject  (admin only)
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// featureDeploymentApprovals is the license feature entitlement name.
const featureDeploymentApprovals = "deployment_approvals"

// handleListApprovals serves GET /api/v1/approvals.
// Accessible by admin and viewer roles. Requires the deployment_approvals
// enterprise feature — returns 402 when not entitled.
//
// Query params:
//   - status=pending|approved|rejected (default: all)
//   - limit=N (default: 50, max 200)
func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureDeploymentApprovals) {
		s.writeLicenseRequired(w, featureDeploymentApprovals)
		return
	}

	status := r.URL.Query().Get("status")
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	approvals, err := s.reg.ListDeploymentApprovals(r.Context(), status, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_approvals_failed", err.Error())
		return
	}
	if approvals == nil {
		approvals = []*registry.DeploymentApproval{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

// handleGetApproval serves GET /api/v1/approvals/{deploymentId}.
// Accessible by admin and viewer roles. Requires the deployment_approvals
// enterprise feature — returns 402 when not entitled.
func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureDeploymentApprovals) {
		s.writeLicenseRequired(w, featureDeploymentApprovals)
		return
	}

	depID := r.PathValue("deploymentId")
	approval, err := s.reg.GetDeploymentApproval(r.Context(), depID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "approval not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_approval_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, approval)
}

// approvalActionRequest is the body of POST .../approve and POST .../reject.
type approvalActionRequest struct {
	Notes string `json:"notes"`
}

// handleApproveDeployment serves POST /api/v1/approvals/{deploymentId}/approve.
// Admin-only. Records this reviewer's "approved" vote. When the quorum defined
// by required_approvals is reached, the approval is transitioned to "approved"
// and the deployment rollout is released.
//
// Dual-control (AI Act Art.14) semantics:
//   - Self-approval denied (requester == reviewer) → 409 self_approval_denied
//   - Duplicate vote denied → 409 already_voted
//   - Expired approval → 410 Gone
//   - Quorum not yet reached → 200 with quorum_reached:false
//   - Quorum reached → 200 with quorum_reached:true and deployment started
func (s *Server) handleApproveDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureDeploymentApprovals) {
		s.writeLicenseRequired(w, featureDeploymentApprovals)
		return
	}
	reviewer := apiKeyHashFromRequest(r)
	if !s.requestIsAdmin(r) {
		s.writeJSON(w, http.StatusForbidden, map[string]any{
			"error":   "forbidden",
			"message": "only admin role can approve deployments",
		})
		return
	}

	depID := r.PathValue("deploymentId")

	var body approvalActionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
	}

	// Load existing approval — must exist and be "pending".
	approval, err := s.reg.GetDeploymentApproval(r.Context(), depID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "approval not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_approval_failed", err.Error())
		return
	}
	if approval.Status != "pending" {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "not_pending",
			"message": "approval is not in pending status (current: " + approval.Status + ")",
			"status":  approval.Status,
		})
		return
	}

	// Expiry check.
	if approval.ExpiresAt != nil && time.Now().After(*approval.ExpiresAt) {
		s.writeError(w, http.StatusGone, "approval_expired",
			"this approval request has expired")
		return
	}

	// Record the vote. RecordApprovalVote validates self-approval and duplicates.
	if err := s.reg.RecordApprovalVote(r.Context(), depID, reviewer, "approved",
		body.Notes, extractIPPrefix(r.RemoteAddr)); err != nil {
		if strings.Contains(err.Error(), "self_approval_denied") {
			s.writeError(w, http.StatusConflict, "self_approval_denied",
				"the requester cannot approve their own deployment")
			return
		}
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			s.writeError(w, http.StatusConflict, "already_voted",
				"you have already voted on this approval")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "vote_failed", err.Error())
		return
	}

	// Check whether quorum has been reached.
	reached, approved, required, err := s.reg.CheckApprovalQuorum(r.Context(), depID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "quorum_check_failed", err.Error())
		return
	}

	if reached {
		notes := fmt.Sprintf("quorum reached (%d/%d)", approved, required)
		if err := s.reg.UpdateDeploymentApprovalStatus(r.Context(), depID, "approved", reviewer, notes); err != nil {
			s.writeError(w, http.StatusInternalServerError, "update_approval_failed", err.Error())
			return
		}
		_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
			Actor: reviewer, Action: "deployment.approval.approved", Target: depID,
		})
	}

	msg := fmt.Sprintf("Vote recorded. Waiting for %d more approval(s).", required-approved)
	if reached {
		msg = "Deployment approved and starting."
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"voted":            true,
		"quorum_reached":   reached,
		"approvals_so_far": approved,
		"approvals_needed": required,
		"message":          msg,
	})
}

// handleRejectDeployment serves POST /api/v1/approvals/{deploymentId}/reject.
// Admin-only. A single reject is sufficient to block the deployment immediately,
// regardless of required_approvals — one veto outweighs any number of approvals.
//
// Dual-control (AI Act Art.14) semantics:
//   - Self-rejection denied (requester == reviewer) → 409 self_approval_denied
//   - Expired approval → 410 Gone
func (s *Server) handleRejectDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureDeploymentApprovals) {
		s.writeLicenseRequired(w, featureDeploymentApprovals)
		return
	}
	reviewer := apiKeyHashFromRequest(r)
	if !s.requestIsAdmin(r) {
		s.writeJSON(w, http.StatusForbidden, map[string]any{
			"error":   "forbidden",
			"message": "only admin role can reject deployments",
		})
		return
	}

	depID := r.PathValue("deploymentId")

	var body approvalActionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
	}

	approval, err := s.reg.GetDeploymentApproval(r.Context(), depID)
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "approval not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_approval_failed", err.Error())
		return
	}
	if approval.Status != "pending" {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "not_pending",
			"message": "approval is not in pending status (current: " + approval.Status + ")",
			"status":  approval.Status,
		})
		return
	}

	// Expiry check.
	if approval.ExpiresAt != nil && time.Now().After(*approval.ExpiresAt) {
		s.writeError(w, http.StatusGone, "approval_expired",
			"this approval request has expired")
		return
	}

	// Self-rejection guard (mirrors approve).
	if approval.Requester == reviewer {
		s.writeError(w, http.StatusConflict, "self_approval_denied",
			"the requester cannot reject their own deployment")
		return
	}

	// Record the reject vote before transitioning (best-effort; non-fatal).
	_ = s.reg.RecordApprovalVote(r.Context(), depID, reviewer, "rejected",
		body.Notes, extractIPPrefix(r.RemoteAddr))

	if err := s.reg.UpdateDeploymentApprovalStatus(r.Context(), depID, "rejected", reviewer, body.Notes); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_approval_failed", err.Error())
		return
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: reviewer, Action: "deployment.approval.rejected", Target: depID,
	})

	updated, err := s.reg.GetDeploymentApproval(r.Context(), depID)
	if err != nil {
		approval.Status = "rejected"
		s.writeJSON(w, http.StatusOK, approval)
		return
	}
	s.writeJSON(w, http.StatusOK, updated)
}

// apiKeyHashFromRequest returns the SHA-256 hex hash of the bearer token in
// the request, or an empty string when no bearer token is present.
// This is used to record the requester/reviewer without storing raw tokens.
func apiKeyHashFromRequest(r *http.Request) string {
	tok := bearerToken(r)
	if tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// extractIPPrefix returns the /24 CIDR prefix of a "host:port" RemoteAddr
// string (GDPR data minimisation — the full IP is never stored).
// For IPv6 addresses the host portion is returned verbatim.
// An unparseable RemoteAddr returns an empty string.
func extractIPPrefix(remoteAddr string) string {
	host := remoteAddr
	if h, _, found := strings.Cut(remoteAddr, ":"); found {
		host = h
	}
	// For an IPv4 address return the /24 prefix (first three octets).
	parts := strings.Split(host, ".")
	if len(parts) == 4 {
		return strings.Join(parts[:3], ".") + ".0"
	}
	return host
}

// requestIsAdmin reports whether the request's bearer token has admin role.
// rbacMiddleware has already enforced RBAC, so if we reached a POST handler
// with a viewer key it would have been rejected — this check is a belt-and-
// suspenders guard specifically for the approve/reject actions.
func (s *Server) requestIsAdmin(r *http.Request) bool {
	tok := bearerToken(r)
	if tok == "" {
		// No token present — pass through (rbacMiddleware already handled auth).
		return true
	}
	// Internal gateway token always has admin-equivalent access.
	if s.internalToken != "" && tok == s.internalToken {
		return true
	}
	if s.reg == nil {
		return true
	}
	// Linear scan to find the key.
	keys, err := s.reg.ListAPIKeys(r.Context())
	if err != nil {
		return true // conservative pass-through on registry error
	}
	sum := sha256.Sum256([]byte(tok))
	hashHex := hex.EncodeToString(sum[:])
	for _, k := range keys {
		if k.KeyHash == hashHex {
			return k.Role == "admin" || k.Role == ""
		}
	}
	// Unknown key: pass through (rbacMiddleware already handled auth).
	return true
}
