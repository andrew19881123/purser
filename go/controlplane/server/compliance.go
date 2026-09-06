package server

import (
	"net/http"
	"time"
)

// buildVersion is the control-plane version reported in compliance documents.
// Overridden by the build system via -ldflags "-X server.buildVersion=vX.Y.Z".
var buildVersion = "v0.3.0"

// featureAIActCompliance is the license feature required to generate AI Act
// technical documentation. Either "ai_act_compliance" or "inference_audit"
// entitles the endpoint (inference_audit implies full AI Act logging).
const featureAIActCompliance = "ai_act_compliance"

// handleAIActTechnicalDoc serves GET /api/v1/compliance/ai-act/technical-doc.
//
// Generates a machine-readable AI Act Art.11 technical documentation summary
// for this Purser deployment. Enterprise-gated: the active license must
// include either the "ai_act_compliance" or "inference_audit" feature.
//
// Response shape (always 200 when licensed):
//
//	{
//	  "generated_at":   "2026-...",
//	  "system_name":    "Purser AI Inference Gateway",
//	  "provider":       "<licensee>",
//	  "version":        "v0.3.0",
//	  "deployed_models": [{"model_id": "...", "deployed_at": "..."}],
//	  "human_oversight_measures": {...},
//	  "data_governance": {...},
//	  "audit_log": {...},
//	  "total_api_keys": N,
//	  "conformity_basis": "AI Act Art.11, Annex IV"
//	}
func (s *Server) handleAIActTechnicalDoc(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureAIActCompliance) && !s.licenseAllows(featureInferenceAudit) {
		s.writeLicenseRequired(w, featureAIActCompliance)
		return
	}

	deployments, _ := s.reg.ListDeployments(r.Context())
	keys, _ := s.reg.ListAPIKeys(r.Context())

	type modelEntry struct {
		ModelID    string `json:"model_id"`
		DeployedAt string `json:"deployed_at"`
	}
	var models []modelEntry
	for _, d := range deployments {
		if !deploymentTerminal(d.State) {
			models = append(models, modelEntry{
				ModelID:    d.ModelID,
				DeployedAt: d.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	if models == nil {
		models = []modelEntry{}
	}

	doc := map[string]any{
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
		"system_name":     "Purser AI Inference Gateway",
		"provider":        s.license.Licensee,
		"version":         buildVersion,
		"deployed_models": models,
		"human_oversight_measures": map[string]any{
			"deployment_approvals": s.licenseAllows("deployment_approvals"),
			"inference_audit":      s.licenseAllows(featureInferenceAudit),
			"policy_engine":        s.licenseAllows(featurePolicyEngine),
		},
		"data_governance": map[string]any{
			"prompt_content_stored": false,
			"pii_fields": []string{
				"api_key_hash (pseudonymised SHA-256)",
				"client_ip_prefix (/24 CIDR only)",
			},
			"retention_configurable": true,
		},
		"audit_log": map[string]any{
			"tamper_evident":  true,
			"algorithm":       "SHA-256 hash chain",
			"inference_chain": true,
		},
		"total_api_keys":   len(keys),
		"conformity_basis": "AI Act Art.11, Annex IV",
	}

	s.writeJSON(w, http.StatusOK, doc)
}

// handleGDPRRecordOfProcessing serves GET /api/v1/compliance/gdpr/record-of-processing.
//
// Returns a GDPR Art.30 record of processing activities for this Purser
// deployment. Enterprise-gated: the active license must include the
// "inference_audit" feature.
//
// Response shape (always 200 when licensed):
//
//	{
//	  "generated_at":         "2026-...",
//	  "controller":           "<licensee>",
//	  "processing_activities": [...]
//	}
func (s *Server) handleGDPRRecordOfProcessing(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureInferenceAudit) {
		s.writeLicenseRequired(w, featureInferenceAudit)
		return
	}

	record := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"controller":   s.license.Licensee,
		"processing_activities": []map[string]any{
			{
				"name":          "Inference Audit Logging",
				"legal_basis":   "GDPR Art.6(1)(c) - legal obligation (AI Act Art.12)",
				"data_subjects": []string{"API users", "end users of deployed AI systems"},
				"data_categories": []string{
					"usage patterns",
					"network identifiers (pseudonymised)",
				},
				"retention_period": "730 days (configurable)",
				"recipients":       []string{"internal compliance team"},
				"third_country_transfers": false,
				"technical_measures": []string{
					"pseudonymisation (SHA-256 hash of API key)",
					"hash chain integrity (SHA-256)",
					"TLS in transit",
					"IP prefix truncation (/24)",
				},
			},
			{
				"name":          "API Key Management",
				"legal_basis":   "GDPR Art.6(1)(b) - contract performance",
				"data_categories": []string{
					"API key hash (not reversible)",
					"team identifier",
				},
				"retention_period": "until revoked",
				"technical_measures": []string{
					"SHA-256 hash only (no plaintext stored)",
				},
			},
		},
	}

	s.writeJSON(w, http.StatusOK, record)
}
