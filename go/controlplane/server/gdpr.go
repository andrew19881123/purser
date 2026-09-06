package server

// gdpr.go — GDPR Art.17 right-to-erasure REST handlers.
//
// POST /api/v1/gdpr/erasure     — pseudonymise inference_audit_log records for
//                                  a given api_key_hash subject.
// GET  /api/v1/gdpr/erasure-log — list past erasure operations (audit trail).
//
// Both endpoints are admin-only and enterprise-gated.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// featureGDPR is the enterprise entitlement required by the GDPR endpoints.
const featureGDPR = "gdpr"

// handleGDPRErasure executes a GDPR Art.17 right-to-erasure request.
//
// Enterprise gate: the active license must include the "gdpr" feature.
// Auth: admin only (belt-and-suspenders over rbacMiddleware).
//
// POST /api/v1/gdpr/erasure
//
// Request body:
//
//	{
//	  "subject_type":       "api_key",
//	  "subject_identifier": "<sha256-hex of the API key>",
//	  "reason":             "user requested deletion under GDPR Art.17"
//	}
//
// Response (200):
//
//	{
//	  "erased_events":  42,
//	  "erasure_type":   "inference_audit",
//	  "completed_at":   "2026-09-06T12:00:00Z",
//	  "subject_prefix": "a1b2c3d4..."
//	}
//
// The operation pseudonymises (does NOT hard-delete) inference_audit_log rows
// for the subject so the tamper-evident hash chain stays intact. The erasure is
// recorded in the immutable gdpr_erasure_log and in the admin audit trail.
func (s *Server) handleGDPRErasure(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureGDPR) {
		s.writeLicenseRequired(w, featureGDPR)
		return
	}

	// Admin-only belt-and-suspenders guard (rbacMiddleware has already enforced
	// RBAC but this is a destructive / compliance-critical operation).
	if !s.requestIsAdmin(r) {
		s.writeError(w, http.StatusForbidden, "forbidden", "admin role required")
		return
	}

	var req struct {
		SubjectType       string `json:"subject_type"`       // currently only "api_key"
		SubjectIdentifier string `json:"subject_identifier"` // sha256 hex of the API key
		Reason            string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.SubjectIdentifier == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "subject_identifier required")
		return
	}

	// Pseudonymise inference_audit_log rows for the subject.
	count, err := s.reg.EraseInferenceEventsBySubject(r.Context(), req.SubjectIdentifier)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "erasure_failed", err.Error())
		return
	}

	now := time.Now().UTC()
	actor := actorFromRequest(r)
	subjectPrefix := req.SubjectIdentifier[:min(8, len(req.SubjectIdentifier))]

	// Append an immutable record to gdpr_erasure_log (compliance audit trail).
	erasureLog := &registry.GDPRErasureLog{
		SubjectHash:  req.SubjectIdentifier,
		ErasedAt:     now,
		ErasedBy:     actor,
		Reason:       req.Reason,
		EventsErased: count,
		ErasureType:  "inference_audit",
	}
	if err := s.reg.RecordGDPRErasure(r.Context(), erasureLog); err != nil {
		// Non-fatal for the HTTP caller — the pseudonymisation has already
		// completed. Log and continue so the response reflects what happened.
		s.log.Error("gdpr: failed to record erasure log entry", "err", err)
	}

	// Tamper-evident audit entry (does not carry the full subject hash to avoid
	// logging the PII-adjacent identifier in the audit trail itself).
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actor,
		Action: "gdpr.erasure.completed",
		Target: subjectPrefix,
		Details: json.RawMessage(fmt.Sprintf(
			`{"events_erased":%d,"reason":%q,"erasure_type":"inference_audit"}`,
			count, req.Reason,
		)),
	})

	s.writeJSON(w, http.StatusOK, map[string]any{
		"erased_events":  count,
		"erasure_type":   "inference_audit",
		"completed_at":   now.Format(time.RFC3339),
		"subject_prefix": subjectPrefix + "...",
	})
}

// handleGDPRErasureLog returns the list of past GDPR erasure operations.
//
// Enterprise gate: the active license must include the "gdpr" feature.
// Auth: admin only.
//
// GET /api/v1/gdpr/erasure-log
//
// Note: full listing (pagination, filtering) is planned for v0.4. This
// endpoint currently returns an empty list so tooling can discover the
// endpoint exists.
func (s *Server) handleGDPRErasureLog(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureGDPR) {
		s.writeLicenseRequired(w, featureGDPR)
		return
	}
	if !s.requestIsAdmin(r) {
		s.writeError(w, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"erasures": []any{},
		"note":     "GDPR erasure log listing will be implemented in v0.4",
	})
}
