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

func post(t *testing.T, srv *server.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// newLicensedAuditServer builds a server entitled to the "audit" feature and
// returns it alongside the concrete registry, so a test can both drive the API
// and reach the DB directly to simulate tampering.
func newLicensedAuditServer(t *testing.T) (*server.Server, *registry.SQLiteRegistry) {
	t.Helper()
	now := time.Now().UTC()
	lic := signedLicense(t, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit"},
		Issued:   now.Add(-time.Hour),
		Expires:  now.Add(time.Hour),
	})
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return server.New(reg, server.Config{Addr: ":0", License: lic}), reg
}

// auditLogBody is the decoded shape of the tamper-evident audit-log endpoint.
type auditLogBody struct {
	Feature  string `json:"feature"`
	Licensee string `json:"licensee"`
	Entries  []struct {
		Seq    uint64 `json:"seq"`
		Action string `json:"action"`
		Hash   string `json:"hash"`
	} `json:"entries"`
	Chain struct {
		Verified bool `json:"verified"`
		Length   int  `json:"length"`
		Break    *struct {
			Index int    `json:"index"`
			Seq   uint64 `json:"seq"`
			Kind  string `json:"kind"`
			Msg   string `json:"msg"`
		} `json:"break"`
	} `json:"chain"`
}

func TestAuditLogChainVerifiedAndTamperDetected(t *testing.T) {
	srv, reg := newLicensedAuditServer(t)

	// Emit several events through a normal API path: each POST mints an API key
	// and appends an "apikey.created" event through the hash chain.
	//
	// The first POST runs in dev/bootstrap mode (no keys exist yet); subsequent
	// POSTs use the token from the first key so fail-closed auth is satisfied.
	makeKey := func(tok string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	authAudit := func(tok string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/enterprise/audit-log", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// First key: no token (bootstrap). Extract the plaintext key for subsequent calls.
	rec0 := makeKey("")
	if rec0.Code != http.StatusCreated {
		t.Fatalf("create apikey 0 = %d; body=%s", rec0.Code, rec0.Body.String())
	}
	var resp0 map[string]any
	_ = json.Unmarshal(rec0.Body.Bytes(), &resp0)
	adminTok, _ := resp0["key"].(string)

	// Remaining 2 keys use the admin token.
	for i := 1; i < 3; i++ {
		if rec := makeKey(adminTok); rec.Code != http.StatusCreated {
			t.Fatalf("create apikey %d = %d; body=%s", i, rec.Code, rec.Body.String())
		}
	}

	// Untampered: 200, entries present in ascending seq, chain verified.
	rec := authAudit(adminTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-log = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body auditLogBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; raw=%s", err, rec.Body.String())
	}
	if body.Feature != "audit" || body.Licensee != "Acme Corp" {
		t.Errorf("feature/licensee = %q/%q, want audit/Acme Corp", body.Feature, body.Licensee)
	}
	if len(body.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(body.Entries))
	}
	if !body.Chain.Verified || body.Chain.Length != 3 || body.Chain.Break != nil {
		t.Fatalf("chain = %+v, want verified=true length=3 no break", body.Chain)
	}
	for i, e := range body.Entries {
		if e.Seq != uint64(i+1) {
			t.Errorf("entries[%d].seq = %d, want %d (ascending)", i, e.Seq, i+1)
		}
		if e.Action != "apikey.created" {
			t.Errorf("entries[%d].action = %q, want apikey.created", i, e.Action)
		}
		if e.Hash == "" {
			t.Errorf("entries[%d].hash empty", i)
		}
	}

	// Tamper: mutate a stored row's content WITHOUT recomputing its hash.
	if _, err := reg.DB().ExecContext(context.Background(),
		`UPDATE audit_log SET actor = ? WHERE seq = ?`, "mallory", 2); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	rec = authAudit(adminTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-log (post-tamper) = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body = auditLogBody{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Chain.Verified {
		t.Fatalf("chain.verified = true after tamper, want false; chain=%+v", body.Chain)
	}
	if body.Chain.Break == nil {
		t.Fatalf("chain.break missing after tamper")
	}
	if body.Chain.Break.Index != 1 || body.Chain.Break.Kind != "hash" || body.Chain.Break.Seq != 2 {
		t.Errorf("break = %+v, want index=1 seq=2 kind=hash", *body.Chain.Break)
	}
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
