package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// newEnterpriseServer builds a server whose license gate is configured with
// lic (nil = community edition).
func newEnterpriseServer(t *testing.T, lic *license.License) *server.Server {
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

// signedLicense injects an ephemeral verification key and returns a verified
// license for the given payload — the full sign→verify path, no private key on
// disk.
func signedLicense(t *testing.T, p license.Payload) *license.License {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prev := license.VerificationKey
	license.VerificationKey = pub
	t.Cleanup(func() { license.VerificationKey = prev })

	key, err := license.Sign(priv, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	lic, err := license.Verify(key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return lic
}

func get(t *testing.T, srv *server.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestEnterpriseStatusCommunity(t *testing.T) {
	// nil license => community fallback.
	srv := newEnterpriseServer(t, nil)
	rec := get(t, srv, "/api/v1/enterprise/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Edition  string   `json:"edition"`
		Licensee string   `json:"licensee"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Edition != "community" {
		t.Errorf("edition = %q, want community", body.Edition)
	}
	if len(body.Features) != 0 {
		t.Errorf("features = %v, want empty", body.Features)
	}
}

func TestEnterpriseStatusLicensed(t *testing.T) {
	now := time.Now().UTC()
	lic := signedLicense(t, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit", "rbac"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(365 * 24 * time.Hour),
	})
	srv := newEnterpriseServer(t, lic)

	rec := get(t, srv, "/api/v1/enterprise/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Edition  string   `json:"edition"`
		Licensee string   `json:"licensee"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Edition != "enterprise" || body.Licensee != "Acme Corp" {
		t.Errorf("got edition=%q licensee=%q, want enterprise/Acme Corp", body.Edition, body.Licensee)
	}
	if len(body.Features) != 2 {
		t.Errorf("features = %v, want [audit rbac]", body.Features)
	}
}

// assertLicenseRequired checks a 402 response carries the exact enterprise gate
// body shape.
func assertLicenseRequired(t *testing.T, rec *httptest.ResponseRecorder, feature string) {
	t.Helper()
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Feature string `json:"feature"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if body.Error.Message != "enterprise license required" {
		t.Errorf("message = %q, want 'enterprise license required'", body.Error.Message)
	}
	if body.Error.Feature != feature {
		t.Errorf("feature = %q, want %q", body.Error.Feature, feature)
	}
	if body.Error.Type != "license_required" {
		t.Errorf("type = %q, want license_required", body.Error.Type)
	}
}

func TestAuditLogGatedCommunity(t *testing.T) {
	srv := newEnterpriseServer(t, nil)
	rec := get(t, srv, "/api/v1/enterprise/audit-log")
	assertLicenseRequired(t, rec, "audit")
}

func TestAuditLogGatedMissingFeature(t *testing.T) {
	// Valid license, but entitles a different feature.
	now := time.Now().UTC()
	lic := signedLicense(t, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"rbac"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
	srv := newEnterpriseServer(t, lic)
	rec := get(t, srv, "/api/v1/enterprise/audit-log")
	assertLicenseRequired(t, rec, "audit")
}

func TestAuditLogGatedExpired(t *testing.T) {
	// Has the audit feature but the license is expired => still 402.
	now := time.Now().UTC()
	lic := signedLicense(t, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit"},
		Issued:   now.Add(-48 * time.Hour),
		Expires:  now.Add(-24 * time.Hour),
	})
	srv := newEnterpriseServer(t, lic)
	rec := get(t, srv, "/api/v1/enterprise/audit-log")
	assertLicenseRequired(t, rec, "audit")
}

func TestAuditLogAllowedWithLicense(t *testing.T) {
	now := time.Now().UTC()
	lic := signedLicense(t, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
	srv := newEnterpriseServer(t, lic)

	rec := get(t, srv, "/api/v1/enterprise/audit-log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Feature  string `json:"feature"`
		Licensee string `json:"licensee"`
		Entries  []any  `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Feature != "audit" || body.Licensee != "Acme Corp" {
		t.Errorf("got feature=%q licensee=%q, want audit/Acme Corp", body.Feature, body.Licensee)
	}
}
