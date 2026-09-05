// Package importer provides model importers from external model registries.
// It implements source-specific clients (GCP Vertex AI, etc.) that resolve
// remote models to GCS artifact URIs, which the control-plane can then ingest
// into the local catalog.
//
// Auth is handled via standard OAuth2 / service-account JWT (no external SDKs):
// the service-account JSON key is parsed with crypto/rsa and x509 from the
// standard library; the resulting JWT is signed RS256 and exchanged at the
// Google OAuth2 token endpoint for a short-lived access token.
package importer

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// serviceAccountJSON is the subset of a Google service account JSON key file
// required for the JWT Bearer Grant (RFC 7523).
type serviceAccountJSON struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
}

// tokenState caches a bearer token and its expiry. get/set are guarded by mu
// so concurrent callers that miss the cache do not issue redundant fetches once
// the first one lands — the cache is written with set and subsequent gets hit.
type tokenState struct {
	mu     sync.Mutex
	token  string
	expiry time.Time
}

// get returns the cached token if it is still valid (with a 30 s safety
// margin), otherwise ("", false) so the caller fetches a new one.
func (ts *tokenState) get() (string, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token != "" && time.Now().Before(ts.expiry.Add(-30*time.Second)) {
		return ts.token, true
	}
	return "", false
}

func (ts *tokenState) set(token string, expiry time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.token = token
	ts.expiry = expiry
}

// VertexAIClient is an authenticated HTTP client for the Vertex AI Model
// Registry REST API. It uses OAuth2 Bearer tokens obtained via service-account
// JWT (GOOGLE_APPLICATION_CREDENTIALS) or the GCE instance metadata server
// (Application Default Credentials on GCE / GKE).
//
// No Google Cloud SDK is required: all auth and HTTP calls use the standard
// library (crypto/rsa, crypto/x509, net/http).
type VertexAIClient struct {
	// Project is the GCP project ID (from PURSER_VERTEX_PROJECT env var).
	Project string
	// Location is the Vertex AI region (from PURSER_VERTEX_LOCATION env var,
	// default "us-central1").
	Location string
	// BaseURL overrides the Vertex AI REST API base URL. When empty the default
	// regional endpoint "https://{location}-aiplatform.googleapis.com/v1" is
	// used. Set this in integration tests to point at an httptest server.
	BaseURL string
	// TokenURL overrides the OAuth2 token endpoint. When empty
	// "https://oauth2.googleapis.com/token" is used. Set this in integration
	// tests to avoid real network calls.
	TokenURL string
	// TokenProvider, if non-nil, is called instead of the built-in OAuth2 flow
	// to obtain a bearer token. Use this in unit tests to inject a static token
	// and bypass GOOGLE_APPLICATION_CREDENTIALS / metadata server access.
	TokenProvider func(ctx context.Context) (string, error)

	httpClient *http.Client
	cache      tokenState
}

// ModelVersion describes a single version of a model registered in Vertex AI.
type ModelVersion struct {
	// Name is the full Vertex AI resource name for this version, e.g.
	// "projects/{p}/locations/{l}/models/{m}@{version}".
	Name string
	// VersionID is the numeric or string version identifier (e.g. "1").
	VersionID string
	// DisplayName is the human-readable label for this version.
	DisplayName string
	// CreateTime is when this version was registered in Vertex AI.
	CreateTime time.Time
	// Description is the version-level description text.
	Description string
	// ArtifactURI is the GCS URI of the model artifact (e.g. "gs://bucket/path/").
	ArtifactURI string
}

// NewVertexAIClient constructs a VertexAIClient from environment variables.
//
//   - PURSER_VERTEX_PROJECT — GCP project ID (required unless the model is
//     referenced by its full resource name).
//   - PURSER_VERTEX_LOCATION — Vertex AI region; defaults to "us-central1".
//   - GOOGLE_APPLICATION_CREDENTIALS — path to a service-account JSON key;
//     if unset, the GCE metadata server is tried at request time.
func NewVertexAIClient() *VertexAIClient {
	loc := os.Getenv("PURSER_VERTEX_LOCATION")
	if loc == "" {
		loc = "us-central1"
	}
	return &VertexAIClient{
		Project:    os.Getenv("PURSER_VERTEX_PROJECT"),
		Location:   loc,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// do returns the configured HTTP client, falling back to a 30 s-timeout client
// when none was set (e.g. when a VertexAIClient is constructed in tests without
// calling NewVertexAIClient).
func (c *VertexAIClient) do() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *VertexAIClient) apiBase() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", c.Location)
}

func (c *VertexAIClient) tokenEndpoint() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return "https://oauth2.googleapis.com/token"
}

// getToken returns a valid OAuth2 bearer token, using a cached copy when
// still valid. It checks TokenProvider first (for tests), then
// GOOGLE_APPLICATION_CREDENTIALS (service-account JWT), then the GCE metadata
// server.
func (c *VertexAIClient) getToken(ctx context.Context) (string, error) {
	if c.TokenProvider != nil {
		return c.TokenProvider(ctx)
	}
	if tok, ok := c.cache.get(); ok {
		return tok, nil
	}
	if creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); creds != "" {
		return c.tokenFromServiceAccount(ctx, creds)
	}
	return c.tokenFromMetadataServer(ctx)
}

// tokenFromServiceAccount reads a service-account JSON key file, builds a
// signed JWT, and exchanges it for an OAuth2 access token.
//
// JWT Bearer Grant flow (RFC 7523 / Google identity docs):
//  1. Build a JWT: header {"alg":"RS256","typ":"JWT"} + claims with iss, sub,
//     aud="https://oauth2.googleapis.com/token",
//     scope="https://www.googleapis.com/auth/cloud-platform", iat, exp.
//  2. Sign header.claims with RSA-PKCS1v15-SHA256 using the SA private key.
//  3. POST grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=<jwt>
//     to the token endpoint; parse access_token from the JSON response.
func (c *VertexAIClient) tokenFromServiceAccount(ctx context.Context, credPath string) (string, error) {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return "", fmt.Errorf("vertexai: read credentials %q: %w", credPath, err)
	}
	var sa serviceAccountJSON
	if err := json.Unmarshal(data, &sa); err != nil {
		return "", fmt.Errorf("vertexai: parse credentials: %w", err)
	}

	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("vertexai: decode PEM private key: empty block")
	}
	rawKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("vertexai: parse private key: %w", err)
	}
	rsaKey, ok := rawKey.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("vertexai: private key is not RSA")
	}

	now := time.Now()
	expiry := now.Add(time.Hour)

	header := base64url(mustJSON(map[string]string{"alg": "RS256", "typ": "JWT"}))
	claims := base64url(mustJSON(map[string]any{
		"iss":   sa.ClientEmail,
		"sub":   sa.ClientEmail,
		"aud":   "https://oauth2.googleapis.com/token",
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"iat":   now.Unix(),
		"exp":   expiry.Unix(),
	}))
	sigInput := header + "." + claims

	h := sha256.New()
	h.Write([]byte(sigInput))
	digest := h.Sum(nil)
	// crypto.SHA256 is registered by the crypto/sha256 init() above.
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest)
	if err != nil {
		return "", fmt.Errorf("vertexai: sign JWT: %w", err)
	}

	jwt := sigInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	return c.exchangeJWT(ctx, jwt, expiry)
}

// tokenFromMetadataServer fetches a short-lived access token from the GCE
// instance metadata server (Application Default Credentials path on GCE/GKE).
func (c *VertexAIClient) tokenFromMetadataServer(ctx context.Context) (string, error) {
	const metaURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := c.do().Do(req)
	if err != nil {
		return "", fmt.Errorf("vertexai: metadata server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vertexai: metadata server %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("vertexai: decode metadata token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("vertexai: empty access_token from metadata server")
	}
	expiry := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	c.cache.set(tok.AccessToken, expiry)
	return tok.AccessToken, nil
}

// exchangeJWT posts a signed JWT assertion to the OAuth2 token endpoint and
// returns the resulting access_token, caching it along with its expiry.
func (c *VertexAIClient) exchangeJWT(ctx context.Context, jwt string, expiry time.Time) (string, error) {
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {jwt},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.do().Do(req)
	if err != nil {
		return "", fmt.Errorf("vertexai: token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vertexai: token endpoint %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("vertexai: decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("vertexai: empty access_token in exchange response")
	}
	if tok.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	c.cache.set(tok.AccessToken, expiry)
	return tok.AccessToken, nil
}

// resolveModel extracts (project, location, modelID) from either a full
// Vertex AI resource name ("projects/p/locations/l/models/m") or a bare model
// ID (uses c.Project and c.Location). A "@version" suffix is stripped before
// parsing.
func (c *VertexAIClient) resolveModel(name string) (project, location, modelID string) {
	// Strip @version if present: "@" must appear after the last "/" for it to
	// be a version suffix rather than part of a path segment.
	if at := strings.LastIndex(name, "@"); at > strings.LastIndex(name, "/") {
		name = name[:at]
	}
	parts := strings.Split(name, "/")
	if len(parts) == 6 &&
		parts[0] == "projects" && parts[2] == "locations" && parts[4] == "models" {
		return parts[1], parts[3], parts[5]
	}
	// Bare model ID: use the client's configured project and location.
	return c.Project, c.Location, name
}

// ListModelVersions returns all versions of the named model, sorted newest
// first (by createTime). modelName may be a full Vertex AI resource name
// ("projects/p/locations/l/models/m") or a bare model ID.
//
// The Vertex AI REST endpoint used is:
//
//	GET /v1/projects/{project}/locations/{location}/models/{model}/versions
func (c *VertexAIClient) ListModelVersions(ctx context.Context, modelName string) ([]ModelVersion, error) {
	tok, err := c.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("vertexai: get token: %w", err)
	}
	project, location, modelID := c.resolveModel(modelName)
	apiURL := fmt.Sprintf("%s/projects/%s/locations/%s/models/%s/versions",
		c.apiBase(), project, location, modelID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.do().Do(req)
	if err != nil {
		return nil, fmt.Errorf("vertexai: list versions: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vertexai: list versions %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Models []struct {
			Name               string `json:"name"`
			VersionID          string `json:"versionId"`
			DisplayName        string `json:"displayName"`
			CreateTime         string `json:"createTime"`
			VersionDescription string `json:"versionDescription"`
			ArtifactURI        string `json:"artifactUri"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vertexai: decode version list: %w", err)
	}

	versions := make([]ModelVersion, 0, len(result.Models))
	for _, m := range result.Models {
		t, _ := time.Parse(time.RFC3339, m.CreateTime)
		versions = append(versions, ModelVersion{
			Name:        m.Name,
			VersionID:   m.VersionID,
			DisplayName: m.DisplayName,
			CreateTime:  t,
			Description: m.VersionDescription,
			ArtifactURI: m.ArtifactURI,
		})
	}

	// Sort newest first.
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].CreateTime.After(versions[j].CreateTime)
	})

	return versions, nil
}

// GetArtifactURI returns the GCS artifact URI for a specific version of a
// model by fetching the versioned model resource directly.
//
// The Vertex AI REST endpoint used is:
//
//	GET /v1/projects/{project}/locations/{location}/models/{model}@{version}
func (c *VertexAIClient) GetArtifactURI(ctx context.Context, modelName, versionID string) (string, error) {
	tok, err := c.getToken(ctx)
	if err != nil {
		return "", fmt.Errorf("vertexai: get token: %w", err)
	}
	project, location, modelID := c.resolveModel(modelName)
	apiURL := fmt.Sprintf("%s/projects/%s/locations/%s/models/%s@%s",
		c.apiBase(), project, location, modelID, versionID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.do().Do(req)
	if err != nil {
		return "", fmt.Errorf("vertexai: get model: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vertexai: get model %d: %s", resp.StatusCode, body)
	}
	var model struct {
		ArtifactURI string `json:"artifactUri"`
	}
	if err := json.Unmarshal(body, &model); err != nil {
		return "", fmt.Errorf("vertexai: decode model: %w", err)
	}
	return model.ArtifactURI, nil
}

// base64url encodes data with unpadded base64url encoding (RFC 4648 §5).
func base64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// mustJSON marshals v to JSON. It panics on error — only used for static,
// schema-fixed inputs (JWT header and claims) that cannot produce marshal errors
// in practice.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("vertexai: mustJSON: " + err.Error())
	}
	return b
}
