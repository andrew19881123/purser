package importer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/registry/importer"
	"github.com/purser/purser/go/controlplane/server"
)

// mockAzureML returns an httptest.Server that serves:
//   - POST /* → OAuth2 token response {"access_token":"mock-token"}
//   - GET  /* → Azure ML model versions list with the supplied versions
func mockAzureML(t *testing.T, versions []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			// Token endpoint: return a bearer token unconditionally.
			json.NewEncoder(w).Encode(map[string]any{"access_token": "mock-token"}) //nolint:errcheck
			return
		}
		// Verify the auth header is present.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"value": versions}) //nolint:errcheck
	}))
}

// newTestReg opens a fresh in-memory-backed registry for each test.
func newTestReg(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("registry.Migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

// setAzureEnv sets the env vars needed by NewAzureMLClient for the duration of
// the test. mockBaseURL is used for both the management API and the token
// endpoint.
func setAzureEnv(t *testing.T, mockBaseURL string) {
	t.Helper()
	t.Setenv("PURSER_AZURE_SUBSCRIPTION_ID", "sub-test")
	t.Setenv("PURSER_AZURE_RESOURCE_GROUP", "rg-test")
	t.Setenv("PURSER_AZURE_ML_WORKSPACE", "ws-test")
	t.Setenv("PURSER_AZURE_TENANT_ID", "tenant-test")
	t.Setenv("PURSER_AZURE_CLIENT_ID", "client-test")
	t.Setenv("PURSER_AZURE_CLIENT_SECRET", "secret-test")
	t.Setenv("PURSER_AZURE_ML_BASE_URL", mockBaseURL)
	t.Setenv("PURSER_AZURE_TOKEN_URL", mockBaseURL+"/oauth2/v2.0/token")
}

// TestAzureMLClient_ListVersions verifies that ListModelVersions returns all
// versions in API order (newest first) and maps the API fields correctly.
func TestAzureMLClient_ListVersions(t *testing.T) {
	apiVersions := []map[string]any{
		{
			"name": "2",
			"properties": map[string]any{
				"modelUri":    "azureml://datastores/ws/paths/model/v2",
				"stage":       "Production",
				"description": "second version",
			},
		},
		{
			"name": "1",
			"properties": map[string]any{
				"modelUri":    "azureml://datastores/ws/paths/model/v1",
				"stage":       "Staging",
				"description": "first version",
			},
		},
	}
	mock := mockAzureML(t, apiVersions)
	defer mock.Close()

	setAzureEnv(t, mock.URL)

	client, err := importer.NewAzureMLClient("")
	if err != nil {
		t.Fatalf("NewAzureMLClient: %v", err)
	}

	versions, err := client.ListModelVersions(context.Background(), "llama-3-8b")
	if err != nil {
		t.Fatalf("ListModelVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("len(versions) = %d, want 2", len(versions))
	}

	// Verify newest-first ordering is preserved from the API response.
	if versions[0].Version != "2" {
		t.Errorf("versions[0].Version = %q, want 2 (latest first)", versions[0].Version)
	}
	if versions[1].Version != "1" {
		t.Errorf("versions[1].Version = %q, want 1", versions[1].Version)
	}
	if versions[0].Stage != "Production" {
		t.Errorf("versions[0].Stage = %q, want Production", versions[0].Stage)
	}
	if versions[1].Stage != "Staging" {
		t.Errorf("versions[1].Stage = %q, want Staging", versions[1].Stage)
	}

	// LatestVersion should pick the Production version (index 0).
	best, ok := importer.LatestVersion(versions)
	if !ok {
		t.Fatal("LatestVersion returned false for non-empty list")
	}
	if best.Version != "2" || best.Stage != "Production" {
		t.Errorf("LatestVersion = %+v, want version=2 stage=Production", best)
	}
}

// TestAzureMLClient_ExtractsArtifactURI verifies that the ArtifactURI field is
// correctly populated from the API's properties.modelUri field, covering both
// azureml:// and https:// URI schemes.
func TestAzureMLClient_ExtractsArtifactURI(t *testing.T) {
	const wantURI = "azureml://datastores/workspaceartifactstore/paths/ExperimentRun/dcid.abc123/outputs/model/"

	mock := mockAzureML(t, []map[string]any{
		{
			"name": "3",
			"properties": map[string]any{
				"modelUri": wantURI,
				"stage":    "Production",
			},
		},
	})
	defer mock.Close()

	setAzureEnv(t, mock.URL)

	client, err := importer.NewAzureMLClient("")
	if err != nil {
		t.Fatalf("NewAzureMLClient: %v", err)
	}
	versions, err := client.ListModelVersions(context.Background(), "my-model")
	if err != nil {
		t.Fatalf("ListModelVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("len(versions) = %d, want 1", len(versions))
	}
	if versions[0].ArtifactURI != wantURI {
		t.Errorf("ArtifactURI = %q, want %q", versions[0].ArtifactURI, wantURI)
	}
}

// TestHandleImport_AzureML is a full server integration test: it starts a mock
// Azure ML API, registers env vars pointing at it, then POSTs an azureml import
// request to the Purser control-plane server and asserts a 201 response that
// includes the model_id and source metadata.
func TestHandleImport_AzureML(t *testing.T) {
	mock := mockAzureML(t, []map[string]any{
		{
			"name": "5",
			"properties": map[string]any{
				"modelUri": "azureml://datastores/ws/paths/llama-3-8b/v5",
				"stage":    "Production",
			},
		},
	})
	defer mock.Close()

	setAzureEnv(t, mock.URL)

	reg := newTestReg(t)
	srv := server.New(reg, server.Config{})

	body := bytes.NewBufferString(`{"source":"azureml","model":"llama-3-8b"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rec.Body.String())
	}

	if resp["model_id"] != "llama-3-8b" {
		t.Errorf("model_id = %v, want llama-3-8b", resp["model_id"])
	}

	src, ok := resp["source"].(map[string]any)
	if !ok {
		t.Fatalf("source field missing or wrong type; resp=%v", resp)
	}
	if src["type"] != "azureml" {
		t.Errorf("source.type = %v, want azureml", src["type"])
	}
	if src["model"] != "llama-3-8b" {
		t.Errorf("source.model = %v, want llama-3-8b", src["model"])
	}
	if src["version"] != "5" {
		t.Errorf("source.version = %v, want 5", src["version"])
	}
	if src["artifact_uri"] != "azureml://datastores/ws/paths/llama-3-8b/v5" {
		t.Errorf("source.artifact_uri = %v", src["artifact_uri"])
	}
	if src["workspace"] != "ws-test" {
		t.Errorf("source.workspace = %v, want ws-test", src["workspace"])
	}
}

// TestHandleImport_AzureML_MissingWorkspace verifies that the server returns
// 400 Bad Request when no workspace is available (neither via the request body
// nor via the PURSER_AZURE_ML_WORKSPACE environment variable).
func TestHandleImport_AzureML_MissingWorkspace(t *testing.T) {
	// Provide subscription and resource group but deliberately omit the workspace.
	t.Setenv("PURSER_AZURE_SUBSCRIPTION_ID", "sub-test")
	t.Setenv("PURSER_AZURE_RESOURCE_GROUP", "rg-test")
	// PURSER_AZURE_ML_WORKSPACE is intentionally not set.

	reg := newTestReg(t)
	srv := server.New(reg, server.Config{})

	// Request body has no "workspace" field.
	body := bytes.NewBufferString(`{"source":"azureml","model":"llama-3-8b"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] == nil {
		t.Error("expected error field in 400 response")
	}
}
