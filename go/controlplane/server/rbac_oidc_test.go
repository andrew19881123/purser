package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// fakeClaimsVerifier is a test double for TokenVerifier + GroupClaimsVerifier.
// It returns a fixed set of claims on every call (or a fixed error). Use it to
// exercise oidcMiddleware's group-claim mapping without a live IdP.
type fakeClaimsVerifier struct {
	claims *server.TokenClaims
	err    error
}

// VerifyToken satisfies server.TokenVerifier.
func (f *fakeClaimsVerifier) VerifyToken(_ context.Context, _ string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return f.claims.Sub, f.claims.Email, nil
}

// VerifyClaims satisfies server.GroupClaimsVerifier.
func (f *fakeClaimsVerifier) VerifyClaims(_ context.Context, _ string) (*server.TokenClaims, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.claims, nil
}

// bearerReq constructs a request with a fake Bearer token. The token value
// itself is irrelevant because fakeClaimsVerifier ignores it; the header must
// be present for oidcMiddleware to attempt verification.
func bearerReq(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer fake-oidc-token")
	return r
}

// TestGroupClaimMappingAdminRole verifies that a token whose "groups" claim
// contains "purser-admins" (mapped to "admin") passes RBAC for mutating requests.
func TestGroupClaimMappingAdminRole(t *testing.T) {
	reg := newReg(t)
	mappings := map[string]string{"purser-admins": "admin", "purser-viewers": "viewer"}
	verifier := &fakeClaimsVerifier{claims: &server.TokenClaims{
		Sub:    "user-1",
		Email:  "admin@example.com",
		Groups: []string{"purser-admins"},
	}}
	srv := server.New(reg, server.Config{
		OIDCVerifier: verifier,
		OIDC:         &server.OIDCConfig{GroupMappings: mappings},
	})

	// POST /api/v1/apikeys — requires admin role (mutating endpoint).
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		bearerReqWithBody(http.MethodPost, "/api/v1/apikeys", `{"name":"test","tenant":"t1"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin group: POST /api/v1/apikeys: status=%d, want 201; body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestGroupClaimMappingViewerRole verifies that a token mapped to "viewer"
// allows GET requests but blocks POST/DELETE with 403 Forbidden.
func TestGroupClaimMappingViewerRole(t *testing.T) {
	reg := newReg(t)
	mappings := map[string]string{"purser-admins": "admin", "purser-viewers": "viewer"}
	verifier := &fakeClaimsVerifier{claims: &server.TokenClaims{
		Sub:    "user-2",
		Email:  "viewer@example.com",
		Groups: []string{"purser-viewers"},
	}}
	srv := server.New(reg, server.Config{
		OIDCVerifier: verifier,
		OIDC:         &server.OIDCConfig{GroupMappings: mappings},
	})

	// GET /api/v1/deployments — read-only; must succeed.
	getRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRec, bearerReq(http.MethodGet, "/api/v1/deployments"))
	if getRec.Code != http.StatusOK {
		t.Fatalf("viewer group: GET /api/v1/deployments: status=%d, want 200; body=%s",
			getRec.Code, getRec.Body.String())
	}

	// POST /api/v1/apikeys — mutating; must be blocked with 403.
	postRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(postRec,
		bearerReqWithBody(http.MethodPost, "/api/v1/apikeys", `{"name":"bad","tenant":"t1"}`))
	if postRec.Code != http.StatusForbidden {
		t.Fatalf("viewer group: POST /api/v1/apikeys: status=%d, want 403; body=%s",
			postRec.Code, postRec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(postRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403 body: %v", err)
	}
	if body["error"] != "forbidden" {
		t.Errorf("403 body.error = %q, want \"forbidden\"", body["error"])
	}
}

// TestGroupClaimNoMapping verifies that when the token's groups do not match
// any entry in GroupMappings, no OIDC role is injected into the context. The
// middleware falls through to API-key RBAC which — finding no matching key —
// passes the request to the handler (anonymous/unenforced path).
func TestGroupClaimNoMapping(t *testing.T) {
	reg := newReg(t)
	mappings := map[string]string{"purser-admins": "admin"} // "purser-other" not in map
	verifier := &fakeClaimsVerifier{claims: &server.TokenClaims{
		Sub:    "user-3",
		Email:  "other@example.com",
		Groups: []string{"purser-other"}, // no mapping for this group
	}}
	srv := server.New(reg, server.Config{
		OIDCVerifier: verifier,
		OIDC:         &server.OIDCConfig{GroupMappings: mappings},
	})

	// POST /api/v1/apikeys — with no OIDC role injected and no matching API key,
	// rbacMiddleware passes through (anonymous). The request reaches the handler.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		bearerReqWithBody(http.MethodPost, "/api/v1/apikeys", `{"name":"fallthrough","tenant":"t1"}`))
	// The handler should run (returns 201 or another non-403 status).
	if rec.Code == http.StatusForbidden {
		t.Fatalf("no-mapping: POST /api/v1/apikeys should not be blocked (fallthrough), got 403; body=%s",
			rec.Body.String())
	}
	// Confirm RBAC did not reject it — any 2xx or handler error is fine.
	t.Logf("no-mapping: POST /api/v1/apikeys status=%d (not 403, as expected)", rec.Code)
}

// TestTenantScopedListDeployments verifies that a viewer with an OIDC tenant
// claim "acme" only sees deployments whose Detail JSON carries "tenant":"acme".
// Deployments for other tenants are filtered out.
func TestTenantScopedListDeployments(t *testing.T) {
	reg := newReg(t)
	ctx := t.Context()

	// Seed two deployments: one for "acme", one for "beta".
	acmeDetail := json.RawMessage(`{"tenant":"acme","model_id":"m1"}`)
	betaDetail := json.RawMessage(`{"tenant":"beta","model_id":"m2"}`)
	for _, d := range []*registry.Deployment{
		{ID: "dep-acme", ModelID: "m1", PlanID: "p1", State: "DEPLOYMENT_STATE_ACTIVE", Detail: acmeDetail},
		{ID: "dep-beta", ModelID: "m2", PlanID: "p2", State: "DEPLOYMENT_STATE_ACTIVE", Detail: betaDetail},
	} {
		if err := reg.CreateDeployment(ctx, d); err != nil {
			t.Fatalf("seed deployment %s: %v", d.ID, err)
		}
	}

	mappings := map[string]string{"purser-viewers": "viewer"}
	verifier := &fakeClaimsVerifier{claims: &server.TokenClaims{
		Sub:    "acme-viewer",
		Email:  "viewer@acme.example.com",
		Groups: []string{"purser-viewers"},
		Tenant: "acme",
	}}
	srv := server.New(reg, server.Config{
		OIDCVerifier: verifier,
		OIDC:         &server.OIDCConfig{GroupMappings: mappings},
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, bearerReq(http.MethodGet, "/api/v1/deployments"))
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant scoped list: status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var respBody struct {
		Deployments []*registry.Deployment `json:"deployments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	if len(respBody.Deployments) != 1 {
		t.Fatalf("tenant scoped list: got %d deployments, want 1; ids=%v",
			len(respBody.Deployments), deploymentIDs(respBody.Deployments))
	}
	if respBody.Deployments[0].ID != "dep-acme" {
		t.Errorf("tenant scoped list: got deployment %q, want dep-acme", respBody.Deployments[0].ID)
	}
}

// bearerReqWithBody creates a POST/DELETE/PATCH request with a JSON body and a
// fake Bearer token.
func bearerReqWithBody(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer fake-oidc-token")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// deploymentIDs extracts the IDs from a deployment slice for test logging.
func deploymentIDs(deps []*registry.Deployment) []string {
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.ID
	}
	return ids
}
