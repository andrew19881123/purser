package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/server"
)

// fixedSecret returns a deterministic 32-byte session secret for tests so
// tokens issued within the same test can be verified.
func fixedSecret() []byte {
	s := make([]byte, 32)
	for i := range s {
		s[i] = byte(i + 1)
	}
	return s
}

// TestCreateServiceAccount_ReturnsClientSecret creates a service account and
// verifies the response contains non-empty client_id, client_secret, and id.
// client_secret must not reappear in subsequent listing responses.
func TestCreateServiceAccount_ReturnsClientSecret(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{SessionSecret: fixedSecret()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts",
		strings.NewReader(`{"name":"ci-bot","role":"inference"}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create SA: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	clientSecret, _ := body["client_secret"].(string)
	if clientSecret == "" {
		t.Errorf("client_secret must not be empty; body=%s", rec.Body.String())
	}
	clientID, _ := body["client_id"].(string)
	if clientID == "" {
		t.Errorf("client_id must not be empty; body=%s", rec.Body.String())
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Errorf("id must not be empty; body=%s", rec.Body.String())
	}

	// client_secret must not leak in the list response.
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec,
		httptest.NewRequest(http.MethodGet, "/api/v1/service-accounts", nil))
	if strings.Contains(listRec.Body.String(), clientSecret) {
		t.Errorf("list response contains plaintext client_secret: %s", listRec.Body.String())
	}
}

// TestTokenEndpoint_ValidCredentials exchanges valid client_id + client_secret
// for a Bearer access token via the client_credentials grant.
func TestTokenEndpoint_ValidCredentials(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{SessionSecret: fixedSecret()})

	// Create a service account.
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec,
		httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts",
			strings.NewReader(`{"name":"ci-bot","role":"inference"}`)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create SA: status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createResp map[string]any
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	clientID, _ := createResp["client_id"].(string)
	clientSecret, _ := createResp["client_secret"].(string)

	// Exchange credentials for a token.
	tokenRec := httptest.NewRecorder()
	tokenReq := httptest.NewRequest(http.MethodPost, "/auth/token",
		strings.NewReader("grant_type=client_credentials&client_id="+clientID+"&client_secret="+clientSecret))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(tokenRec, tokenReq)

	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token endpoint: status=%d body=%s", tokenRec.Code, tokenRec.Body.String())
	}
	var tokenResp map[string]any
	if err := json.Unmarshal(tokenRec.Body.Bytes(), &tokenResp); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		t.Errorf("access_token must not be empty; body=%s", tokenRec.Body.String())
	}
	if tokenResp["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", tokenResp["token_type"])
	}
	expiresIn, _ := tokenResp["expires_in"].(float64)
	if expiresIn != 900 {
		t.Errorf("expires_in = %v, want 900", expiresIn)
	}
}

// TestTokenEndpoint_InvalidSecret_Returns401 verifies that a wrong
// client_secret returns 401 and that the response takes at least 50ms
// (constant-time delay guard).
func TestTokenEndpoint_InvalidSecret_Returns401(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{SessionSecret: fixedSecret()})

	// Create a service account.
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec,
		httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts",
			strings.NewReader(`{"name":"ci-bot","role":"inference"}`)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create SA: status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createResp map[string]any
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	clientID, _ := createResp["client_id"].(string)

	// Attempt token with wrong secret and measure elapsed time.
	start := time.Now()
	tokenRec := httptest.NewRecorder()
	tokenReq := httptest.NewRequest(http.MethodPost, "/auth/token",
		strings.NewReader("grant_type=client_credentials&client_id="+clientID+"&client_secret=WRONG"))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(tokenRec, tokenReq)
	elapsed := time.Since(start)

	if tokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d; body=%s", tokenRec.Code, tokenRec.Body.String())
	}
	// Constant-time delay: response must take at least 50ms.
	if elapsed < 50*time.Millisecond {
		t.Errorf("response too fast (%v < 50ms); constant-time delay not enforced", elapsed)
	}
}

// TestServiceAccount_Token_AuthenticatesRequest verifies that a JWT obtained
// via the client_credentials flow is accepted as a Bearer token on subsequent
// management API requests. Uses an admin-role SA so GET /api/v1/nodes is allowed.
func TestServiceAccount_Token_AuthenticatesRequest(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{SessionSecret: fixedSecret()})

	// Seed an API key to enable fail-closed auth.
	adminToken := seedKeyWithRole(t, reg, "key-admin", "admin", "admin")

	// Create an admin-role service account using the admin key.
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/service-accounts",
		strings.NewReader(`{"name":"ci-admin","role":"admin"}`))
	createReq.Header.Set("Authorization", "Bearer "+adminToken)
	srv.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create SA: status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createResp map[string]any
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	clientID, _ := createResp["client_id"].(string)
	clientSecret, _ := createResp["client_secret"].(string)

	// Get a token.
	tokenRec := httptest.NewRecorder()
	tokenReq := httptest.NewRequest(http.MethodPost, "/auth/token",
		strings.NewReader("grant_type=client_credentials&client_id="+clientID+"&client_secret="+clientSecret))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token: status=%d body=%s", tokenRec.Code, tokenRec.Body.String())
	}
	var tokenResp map[string]any
	_ = json.Unmarshal(tokenRec.Body.Bytes(), &tokenResp)
	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		t.Fatalf("empty access_token; body=%s", tokenRec.Body.String())
	}

	// Use the SA JWT to call GET /api/v1/nodes — must not be 401 or 403.
	nodesRec := httptest.NewRecorder()
	nodesReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	nodesReq.Header.Set("Authorization", "Bearer "+accessToken)
	srv.Handler().ServeHTTP(nodesRec, nodesReq)
	if nodesRec.Code == http.StatusUnauthorized || nodesRec.Code == http.StatusForbidden {
		t.Fatalf("SA JWT should authenticate: got %d body=%s", nodesRec.Code, nodesRec.Body.String())
	}
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/v1/nodes, got %d; body=%s", nodesRec.Code, nodesRec.Body.String())
	}
}
