package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/purser/purser/go/controlplane/policy"
	"github.com/purser/purser/go/controlplane/registry"
)

// handleListPolicies returns all stored Rego policies.
//
// Enterprise gate: the active license must include the "policy_engine" feature;
// returns 402 Payment Required otherwise.
// Auth: any authenticated caller (admin or viewer).
//
// GET /api/v1/policies
func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featurePolicyEngine) {
		s.writeLicenseRequired(w, featurePolicyEngine)
		return
	}
	rows, err := s.reg.ListPolicies(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_policies_failed", err.Error())
		return
	}
	if rows == nil {
		rows = []*registry.Policy{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"policies": rows})
}

// upsertPolicyBody is the request body for PUT /api/v1/policies/{name}.
type upsertPolicyBody struct {
	Rego    string `json:"rego"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// handleUpsertPolicy creates or replaces the named policy.
//
// Enterprise gate: the active license must include "policy_engine".
// Auth: admin only (enforced by rbacMiddleware on non-GET /api/v1/*).
//
// PUT /api/v1/policies/{name}
// Body: {"rego": "<rego source>", "enabled": true}
//
// Returns 200 with the stored policy on success.
// Returns 400 when the Rego fails to compile (dry-run validation).
func (s *Server) handleUpsertPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featurePolicyEngine) {
		s.writeLicenseRequired(w, featurePolicyEngine)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "policy name is required")
		return
	}

	var body upsertPolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if body.Rego == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "rego field is required")
		return
	}

	// Compile the Rego before storing — fail fast with a clear 400 if it's invalid.
	if _, err := policy.LoadPolicies(r.Context(), []policy.PolicySource{{Name: name, Rego: body.Rego}}); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_rego", err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	p := &registry.Policy{
		Name:    name,
		Rego:    body.Rego,
		Enabled: enabled,
	}
	if err := s.reg.UpsertPolicy(r.Context(), p); err != nil {
		s.writeError(w, http.StatusInternalServerError, "upsert_policy_failed", err.Error())
		return
	}

	// Reload the engine so the new/updated policy takes effect immediately.
	s.reloadPolicies(r.Context())

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: actorFromRequest(r), Action: "policy.upserted", Target: name,
	})
	s.writeJSON(w, http.StatusOK, p)
}

// handleDeletePolicy removes the named policy and reloads the engine.
//
// Enterprise gate: the active license must include "policy_engine".
// Auth: admin only (enforced by rbacMiddleware on non-GET /api/v1/*).
//
// DELETE /api/v1/policies/{name}
// Returns 204 No Content on success; 404 if no such policy exists.
func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featurePolicyEngine) {
		s.writeLicenseRequired(w, featurePolicyEngine)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "policy name is required")
		return
	}
	if err := s.reg.DeletePolicy(r.Context(), name); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "policy not found: "+name)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "delete_policy_failed", err.Error())
		return
	}

	// Reload the engine so the removed policy is no longer evaluated.
	s.reloadPolicies(r.Context())

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: actorFromRequest(r), Action: "policy.deleted", Target: name,
	})
	w.WriteHeader(http.StatusNoContent)
}

// evalPolicyBody is the request body for POST /api/v1/policies/eval.
type evalPolicyBody struct {
	Action   string            `json:"action"`
	ModelID  string            `json:"model_id"`
	TenantID string            `json:"tenant_id"`
	KeyHash  string            `json:"key_hash"`
	Claims   map[string]string `json:"claims"`
}

// handleEvalPolicy is a dry-run evaluation endpoint: it evaluates the given
// request against the currently loaded policy engine and returns the verdict.
// This lets operators test policies before they affect traffic.
//
// Enterprise gate: the active license must include "policy_engine".
// Auth: admin only (enforced by rbacMiddleware on non-GET /api/v1/*).
//
// POST /api/v1/policies/eval
func (s *Server) handleEvalPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featurePolicyEngine) {
		s.writeLicenseRequired(w, featurePolicyEngine)
		return
	}

	var body evalPolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	s.policyMu.RLock()
	eng := s.policyEngine
	s.policyMu.RUnlock()

	if eng == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"allowed": true,
			"reason":  "no policies loaded (open-by-default)",
		})
		return
	}

	req := policy.EvalRequest{
		Action:   body.Action,
		ModelID:  body.ModelID,
		TenantID: body.TenantID,
		KeyHash:  body.KeyHash,
		Claims:   body.Claims,
	}
	result, err := eng.Allow(r.Context(), req)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "policy_eval_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"allowed": result.Allowed,
		"reason":  result.Reason,
	})
}
