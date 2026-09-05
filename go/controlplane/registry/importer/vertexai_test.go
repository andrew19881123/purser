package importer_test

import (
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

// newTestRegistry opens a fresh in-memory SQLite registry for a single test.
func newTestRegistry(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

// staticToken returns a TokenProvider that always supplies the given token.
func staticToken(tok string) func(context.Context) (string, error) {
	return func(_ context.Context) (string, error) { return tok, nil }
}

// TestVertexAIClient_ListVersions verifies that ListModelVersions returns
// versions sorted newest first and correctly maps the API response fields.
func TestVertexAIClient_ListVersions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/versions") {
			http.NotFound(w, r)
			return
		}
		// Verify the Bearer token is forwarded.
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				// Older version registered first.
				{
					"name":               "projects/p/locations/us-central1/models/llama-3@1",
					"versionId":          "1",
					"displayName":        "LLaMA 3 v1",
					"createTime":         "2024-01-01T00:00:00Z",
					"versionDescription": "initial",
					"artifactUri":        "gs://bucket/llama-3/v1/",
				},
				// Newer version registered second.
				{
					"name":               "projects/p/locations/us-central1/models/llama-3@2",
					"versionId":          "2",
					"displayName":        "LLaMA 3 v2",
					"createTime":         "2024-06-15T12:00:00Z",
					"versionDescription": "updated weights",
					"artifactUri":        "gs://bucket/llama-3/v2/",
				},
			},
		})
	}))
	defer ts.Close()

	client := &importer.VertexAIClient{
		Project:       "p",
		Location:      "us-central1",
		BaseURL:       ts.URL,
		TokenProvider: staticToken("test-token"),
	}

	versions, err := client.ListModelVersions(context.Background(), "llama-3")
	if err != nil {
		t.Fatalf("ListModelVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("len(versions) = %d, want 2", len(versions))
	}

	// Newest first: version 2 (June 2024) must precede version 1 (January 2024).
	if versions[0].VersionID != "2" {
		t.Errorf("versions[0].VersionID = %q, want 2 (newest)", versions[0].VersionID)
	}
	if versions[1].VersionID != "1" {
		t.Errorf("versions[1].VersionID = %q, want 1 (oldest)", versions[1].VersionID)
	}
	if versions[0].ArtifactURI != "gs://bucket/llama-3/v2/" {
		t.Errorf("versions[0].ArtifactURI = %q, want gs://bucket/llama-3/v2/", versions[0].ArtifactURI)
	}
	if versions[0].DisplayName != "LLaMA 3 v2" {
		t.Errorf("versions[0].DisplayName = %q, want LLaMA 3 v2", versions[0].DisplayName)
	}
}

// TestVertexAIClient_ExtractsGCSURI verifies that GetArtifactURI calls the
// versioned model endpoint and returns the artifactUri field.
func TestVertexAIClient_ExtractsGCSURI(t *testing.T) {
	const wantURI = "gs://my-bucket/models/llama-3/v3/"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Expect GET .../models/llama-3@3
		if !strings.Contains(r.URL.Path, "llama-3@3") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":        "projects/p/locations/us-central1/models/llama-3@3",
			"versionId":   "3",
			"artifactUri": wantURI,
		})
	}))
	defer ts.Close()

	client := &importer.VertexAIClient{
		Project:       "p",
		Location:      "us-central1",
		BaseURL:       ts.URL,
		TokenProvider: staticToken("tok"),
	}

	uri, err := client.GetArtifactURI(context.Background(), "llama-3", "3")
	if err != nil {
		t.Fatalf("GetArtifactURI: %v", err)
	}
	if uri != wantURI {
		t.Errorf("ArtifactURI = %q, want %q", uri, wantURI)
	}
}

// TestHandleImport_VertexAI is an end-to-end server test: it starts a mock
// Vertex AI API, imports a model via POST /api/v1/models/import, and verifies
// the 201 response contains the expected source JSON.
func TestHandleImport_VertexAI(t *testing.T) {
	const (
		modelName   = "projects/test-project/locations/us-central1/models/llama-3"
		wantGCSURI  = "gs://my-bucket/llama-3/v1/"
		wantVersion = "1"
	)

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/versions"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{
						"name":        "projects/test-project/locations/us-central1/models/llama-3@1",
						"versionId":   "1",
						"createTime":  "2024-03-01T00:00:00Z",
						"displayName": "LLaMA 3",
						"artifactUri": wantGCSURI,
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockAPI.Close()

	reg := newTestRegistry(t)
	client := &importer.VertexAIClient{
		Project:       "test-project",
		Location:      "us-central1",
		BaseURL:       mockAPI.URL,
		TokenProvider: staticToken("test-token"),
	}
	srv := server.New(reg, server.Config{VertexAI: client})

	reqBody := `{"source":"vertexai","model":"` + modelName + `","vertex_version":""}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(reqBody))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}

	src, ok := resp["source"].(map[string]any)
	if !ok {
		t.Fatalf("response missing 'source' object; got: %v", resp)
	}
	if src["type"] != "vertexai" {
		t.Errorf("source.type = %v, want vertexai", src["type"])
	}
	if src["gcs_uri"] != wantGCSURI {
		t.Errorf("source.gcs_uri = %v, want %q", src["gcs_uri"], wantGCSURI)
	}
	if src["version"] != wantVersion {
		t.Errorf("source.version = %v, want %q", src["version"], wantVersion)
	}
	if src["model"] != modelName {
		t.Errorf("source.model = %v, want %q", src["model"], modelName)
	}

	// The model must be persisted in the registry.
	if resp["model_id"] == nil || resp["model_id"] == "" {
		t.Errorf("response missing model_id; got: %v", resp)
	}
}

// TestHandleImport_VertexAI_MissingProject verifies that importing a model
// with a bare model name (not a full resource path) and no PURSER_VERTEX_PROJECT
// configured returns 400.
func TestHandleImport_VertexAI_MissingProject(t *testing.T) {
	reg := newTestRegistry(t)
	// Client with no project — the model name in the request is also bare
	// (no "projects/..." prefix), so there is no way to resolve the project.
	client := &importer.VertexAIClient{
		Location: "us-central1",
		// Project intentionally empty.
		TokenProvider: staticToken("tok"),
	}
	srv := server.New(reg, server.Config{VertexAI: client})

	reqBody := `{"source":"vertexai","model":"my-model","vertex_version":""}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(reqBody))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Errorf("response missing error field; got: %v", resp)
	}
}
