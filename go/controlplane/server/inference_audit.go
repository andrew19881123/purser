package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// featureInferenceAudit is the license feature required to read inference
// audit events via the REST API. Recording (POST /api/v1/inference-events) is
// always active when the endpoint is configured; reading is enterprise-gated.
const featureInferenceAudit = "inference_audit"

// handleListInferenceAudit returns a paginated list of inference audit events.
//
// Enterprise gate: the active license must include the "inference_audit"
// feature; returns 402 Payment Required otherwise.
// Auth: RBAC viewer or admin (enforced by the global rbacMiddleware on all GET
// /api/v1/* paths — no per-handler check needed).
//
// Query parameters (all optional):
//
//	api_key_hash  — filter by key hash
//	model_id      — filter by model
//	tenant_id     — filter by tenant
//	after         — RFC3339 exclusive lower bound on timestamp
//	before        — RFC3339 exclusive upper bound on timestamp
//	limit         — page size (default 100, max 1000)
//	page_token    — opaque cursor from a previous response's next_page_token
func (s *Server) handleListInferenceAudit(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureInferenceAudit) {
		s.writeLicenseRequired(w, featureInferenceAudit)
		return
	}

	q := r.URL.Query()
	req := &registry.ListInferenceEventsRequest{
		APIKeyHash: q.Get("api_key_hash"),
		ModelID:    q.Get("model_id"),
		TenantID:   q.Get("tenant_id"),
		PageToken:  q.Get("page_token"),
	}

	if l := q.Get("limit"); l != "" {
		if n, err := strconv.ParseInt(l, 10, 32); err == nil && n > 0 {
			req.Limit = int32(n)
		}
	}

	if v := q.Get("after"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			req.After = t
		}
	}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			req.Before = t
		}
	}

	resp, err := s.reg.ListInferenceEvents(r.Context(), req)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_inference_events_failed", err.Error())
		return
	}

	if resp.Events == nil {
		resp.Events = []*registry.InferenceEvent{}
	}

	out := map[string]any{"events": resp.Events}
	if resp.NextPageToken != "" {
		out["next_page_token"] = resp.NextPageToken
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleRecordInferenceEvent is the internal endpoint the gateway calls after
// each completed inference to record an audit event. Authentication is via the
// X-Purser-Internal-Token header (same shared secret used for route-sync). When
// InternalToken is set on the server, the header is required; when it is empty
// (dev/single-node), the endpoint is open.
//
// The operation is idempotent: a duplicate request_id is silently ignored.
// Returns 204 No Content on success.
func (s *Server) handleRecordInferenceEvent(w http.ResponseWriter, r *http.Request) {
	if s.internalToken != "" {
		if !s.validateInternalToken(r.Header.Get("X-Purser-Internal-Token")) {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing internal token")
			return
		}
	}

	var event registry.InferenceEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	if err := s.reg.RecordInferenceEvent(r.Context(), &event); err != nil {
		s.writeError(w, http.StatusInternalServerError, "record_inference_event_failed", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
