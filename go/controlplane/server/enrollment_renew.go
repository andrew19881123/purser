package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/registry"
)

// enrollmentRenewRequest is the JSON body accepted by POST /api/v1/enrollment/renew.
type enrollmentRenewRequest struct {
	NodeID string `json:"node_id"`
}

// handleEnrollmentRenew issues a fresh mTLS certificate for an existing node.
//
// The agent sends its node_id in the request body. The server:
//  1. Looks the node up in the registry (404 if unknown).
//  2. Rejects the request with 409 if the current cert is valid for > 60 days
//     (prevents premature churn / cert-flooding).
//  3. Issues a new leaf certificate via the internal PKI.
//  4. Returns {"certificate_pem": "...", "expires_at": "RFC3339"}.
//
// Authentication: in a TLS-terminated deployment the agent's current client
// certificate is verified by the transport layer (mTLS). The node_id in the
// body is used only for registry lookup; the operator is expected to configure
// mTLS on the control-plane listener (PURSER_TLS_AUTO=true or explicit cert/key)
// so that only the legitimate node can authenticate.
func (s *Server) handleEnrollmentRenew(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		s.writeError(w, http.StatusNotImplemented, "no_fleet", "fleet manager not configured")
		return
	}

	var body enrollmentRenewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if body.NodeID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "node_id is required")
		return
	}

	result, err := s.fleet.RenewCert(r.Context(), body.NodeID)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrNotFound):
			s.writeError(w, http.StatusNotFound, "not_found", "node not found")
		case errors.Is(err, fleet.ErrCertNotYetExpiring):
			s.writeError(w, http.StatusConflict, "cert_not_yet_expiring",
				"certificate is still valid for more than 60 days; renewal rejected to prevent premature churn")
		default:
			s.writeError(w, http.StatusInternalServerError, "renew_failed", err.Error())
		}
		return
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "enrollment.cert_renewed",
		Target: body.NodeID,
	})

	s.writeJSON(w, http.StatusOK, map[string]any{
		"certificate_pem": string(result.CertPEM),
		"expires_at":      result.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
