package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newComplianceServer builds a server with the given license for compliance endpoint tests.
func newComplianceServer(t *testing.T, lic *license.License) *server.Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return server.New(reg, server.Config{Addr: ":0", License: lic})
}

// signedInferenceAuditComplianceLicense returns a valid enterprise license
// with the "inference_audit" feature enabled (gates both GDPR and AI Act endpoints).
func signedInferenceAuditComplianceLicense(t *testing.T) *license.License {
	t.Helper()
	now := time.Now().UTC()
	return signedLicense(t, license.Payload{
		Licensee: "Compliance Corp",
		Features: []string{"inference_audit"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
}

// TestHandleAIActTechnicalDoc_EnterpriseGated verifies that
// GET /api/v1/compliance/ai-act/technical-doc returns 402 without a license.
func TestHandleAIActTechnicalDoc_EnterpriseGated(t *testing.T) {
	srv := newComplianceServer(t, nil) // community license
	rec := get(t, srv, "/api/v1/compliance/ai-act/technical-doc")
	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("want 402, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj == nil {
		t.Fatalf("expected error object in body, got %v", body)
	}
	if ft, _ := errObj["feature"].(string); ft != "ai_act_compliance" {
		t.Errorf("error.feature = %q, want ai_act_compliance", ft)
	}
}

// TestHandleAIActTechnicalDoc_WithLicense verifies that the endpoint returns
// a well-formed AI Act Art.11 technical documentation document when licensed.
func TestHandleAIActTechnicalDoc_WithLicense(t *testing.T) {
	lic := signedInferenceAuditComplianceLicense(t)
	srv := newComplianceServer(t, lic)

	rec := get(t, srv, "/api/v1/compliance/ai-act/technical-doc")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// System metadata
	if v, _ := body["system_name"].(string); v != "Purser AI Inference Gateway" {
		t.Errorf("system_name = %q, want 'Purser AI Inference Gateway'", v)
	}
	if v, _ := body["provider"].(string); v != "Compliance Corp" {
		t.Errorf("provider = %q, want 'Compliance Corp'", v)
	}
	if v, _ := body["conformity_basis"].(string); v != "AI Act Art.11, Annex IV" {
		t.Errorf("conformity_basis = %q, want 'AI Act Art.11, Annex IV'", v)
	}

	// data_governance must declare prompt_content_stored: false
	dg, ok := body["data_governance"].(map[string]any)
	if !ok {
		t.Fatalf("data_governance missing or wrong type: %T", body["data_governance"])
	}
	if stored, _ := dg["prompt_content_stored"].(bool); stored {
		t.Error("data_governance.prompt_content_stored must be false")
	}

	// deployed_models must be present (empty slice for a fresh DB, not null)
	if _, ok := body["deployed_models"]; !ok {
		t.Error("deployed_models field missing")
	}

	// human_oversight_measures must be present
	if _, ok := body["human_oversight_measures"]; !ok {
		t.Error("human_oversight_measures field missing")
	}

	// audit_log.tamper_evident must be true
	al, ok := body["audit_log"].(map[string]any)
	if !ok {
		t.Fatalf("audit_log missing or wrong type")
	}
	if te, _ := al["tamper_evident"].(bool); !te {
		t.Error("audit_log.tamper_evident must be true")
	}
}

// TestHandleGDPRRecordOfProcessing_WithLicense verifies that the GDPR Art.30
// record of processing endpoint returns a well-formed document when licensed.
func TestHandleGDPRRecordOfProcessing_WithLicense(t *testing.T) {
	lic := signedInferenceAuditComplianceLicense(t)
	srv := newComplianceServer(t, lic)

	rec := get(t, srv, "/api/v1/compliance/gdpr/record-of-processing")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if v, _ := body["controller"].(string); v != "Compliance Corp" {
		t.Errorf("controller = %q, want 'Compliance Corp'", v)
	}

	activities, ok := body["processing_activities"].([]any)
	if !ok || len(activities) == 0 {
		t.Fatalf("processing_activities missing or empty: %v", body["processing_activities"])
	}

	// First activity must be inference audit logging
	first, ok := activities[0].(map[string]any)
	if !ok {
		t.Fatalf("processing_activities[0] is not an object")
	}
	if name, _ := first["name"].(string); name != "Inference Audit Logging" {
		t.Errorf("activities[0].name = %q, want 'Inference Audit Logging'", name)
	}
	// third_country_transfers must be false
	if xfer, _ := first["third_country_transfers"].(bool); xfer {
		t.Error("third_country_transfers must be false")
	}
}

// TestHandleGDPRRecordOfProcessing_EnterpriseGated verifies 402 without a license.
func TestHandleGDPRRecordOfProcessing_EnterpriseGated(t *testing.T) {
	srv := newComplianceServer(t, nil)
	rec := get(t, srv, "/api/v1/compliance/gdpr/record-of-processing")
	assertLicenseRequired(t, rec, "inference_audit")
}
