package server

import (
	"net/http"
)

// handleOrgBillingReport serves GET /api/v1/platform/orgs/{orgId}/billing.
//
// Enterprise gate: requires the "billing" feature in the active license.
// Returns 402 Payment Required without a valid entitlement.
//
// Query parameters (all optional):
//
//	start  — RFC3339 window start; defaults to now − 30 days
//	end    — RFC3339 window end;   defaults to now
//
// Teams are discovered using the naming convention "<orgId>/<teamSlug>" for
// tenant_id values in the inference_audit_log. Only teams with at least one
// inference event in the requested window appear in the response.
func (s *Server) handleOrgBillingReport(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureBilling) {
		s.writeLicenseRequired(w, featureBilling)
		return
	}

	orgID := r.PathValue("orgId")
	if orgID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "orgId is required")
		return
	}

	start, end, err := parseBillingWindow(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	report, err := s.reg.GetOrgBillingReport(r.Context(), orgID, start, end)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "org_billing_failed", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, report)
}

// handleTeamBillingReport serves GET /api/v1/platform/teams/{teamId}/billing.
//
// Enterprise gate: requires the "billing" feature in the active license.
// Returns 402 Payment Required without a valid entitlement.
//
// The teamId path parameter must match the tenant_id used in the team's API
// keys (e.g. "acme/engineering"). Query parameters are identical to the org
// billing endpoint.
func (s *Server) handleTeamBillingReport(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureBilling) {
		s.writeLicenseRequired(w, featureBilling)
		return
	}

	teamID := r.PathValue("teamId")
	if teamID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "teamId is required")
		return
	}

	start, end, err := parseBillingWindow(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	report, err := s.reg.GetTeamBillingReport(r.Context(), teamID, start, end)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "team_billing_failed", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, report)
}
