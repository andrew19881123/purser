package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleImport_S3 verifies the POST /api/v1/models/import endpoint for an
// S3 source: the model is created with a Source field carrying a resolved
// HTTPS download URL. AWS credential env vars are cleared so the deterministic
// public-URL path is exercised.
func TestHandleImport_S3(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("PURSER_S3_REGION", "")

	srv, reg := newTestServer(t)

	body := strings.NewReader(`{
		"source": "s3",
		"uri":    "s3://my-models-bucket/llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf",
		"name":   "llama-3.1-8b",
		"family": "llama",
		"size_gb": 4.8
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// Response must include model_id and source_type.
	var resp struct {
		ModelID     string `json:"model_id"`
		SourceType  string `json:"source_type"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rec.Body.String())
	}
	if resp.ModelID != "llama-3.1-8b" {
		t.Errorf("model_id = %q, want llama-3.1-8b", resp.ModelID)
	}
	if resp.SourceType != "s3" {
		t.Errorf("source_type = %q, want s3", resp.SourceType)
	}
	if !strings.HasPrefix(resp.DownloadURL, "https://my-models-bucket.s3.us-east-1.amazonaws.com/") {
		t.Errorf("download_url = %q, want https://my-models-bucket.s3.us-east-1.amazonaws.com/...", resp.DownloadURL)
	}

	// The stored model must have a Source blob with type and download_url set.
	m, err := reg.GetModel(context.Background(), "llama-3.1-8b")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	var src struct {
		Type        string `json:"type"`
		Bucket      string `json:"bucket"`
		Key         string `json:"key"`
		Region      string `json:"region"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(m.Source, &src); err != nil {
		t.Fatalf("decode Source: %v; raw=%s", err, string(m.Source))
	}
	if src.Type != "s3" {
		t.Errorf("Source.Type = %q, want s3", src.Type)
	}
	if src.Bucket != "my-models-bucket" {
		t.Errorf("Source.Bucket = %q, want my-models-bucket", src.Bucket)
	}
	if src.Key != "llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf" {
		t.Errorf("Source.Key = %q", src.Key)
	}
	if src.Region != "us-east-1" {
		t.Errorf("Source.Region = %q, want us-east-1", src.Region)
	}
	if !strings.HasPrefix(src.DownloadURL, "https://") {
		t.Errorf("Source.DownloadURL = %q, want https://...", src.DownloadURL)
	}
}

// TestHandleImport_DuplicateReturns409 verifies that a second import with the
// same name returns 409 Conflict.
func TestHandleImport_DuplicateReturns409(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	srv, _ := newTestServer(t)
	post := func() int {
		body := strings.NewReader(`{"uri":"s3://b/k.gguf","name":"dedup-model"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(); code != http.StatusCreated {
		t.Fatalf("first import: status = %d, want 201", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Fatalf("second import: status = %d, want 409", code)
	}
}

// TestHandleImport_MissingURIReturns400 verifies that a request without a URI
// field returns 400 Bad Request.
func TestHandleImport_MissingURIReturns400(t *testing.T) {
	srv, _ := newTestServer(t)
	body := strings.NewReader(`{"name":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
