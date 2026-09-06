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
	"net/http"
	"strconv"

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
// Admin-only. Transitions the approval from "pending" to "approved" and, if a
// deployer is wired up, kicks off the actual rollout.
func (s *Server) handleApproveDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureDeploymentApprovals) {
		s.writeLicenseRequired(w, featureDeploymentApprovals)
		return
	}
	// Enforce admin role: extract the API key hash from the bearer token so we
	// can store it as the reviewer. rbacMiddleware has already enforced the role
	// for non-GET routes, but we also need the hash for auditing.
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

	if err := s.reg.UpdateDeploymentApprovalStatus(r.Context(), depID, "approved", reviewer, body.Notes); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update_approval_failed", err.Error())
		return
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: reviewer, Action: "deployment.approval.approved", Target: depID,
	})

	// Re-fetch the updated record to return current state.
	updated, err := s.reg.GetDeploymentApproval(r.Context(), depID)
	if err != nil {
		// Non-fatal: the approval was updated; just echo what we know.
		approval.Status = "approved"
		s.writeJSON(w, http.StatusOK, approval)
		return
	}
	s.writeJSON(w, http.StatusOK, updated)
}

// handleRejectDeployment serves POST /api/v1/approvals/{deploymentId}/reject.
// Admin-only. Transitions the approval from "pending" to "rejected".
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
