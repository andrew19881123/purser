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

// mockHFServer builds a minimal httptest.Server that simulates the
// HuggingFace Hub /api/models/{repo}?blobs=true endpoint.
func mockHFServer(t *testing.T, statusCode int, payload interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if payload != nil {
			_ = json.NewEncoder(w).Encode(payload)
		}
	}))
}

// sampleHFPayload is a realistic HuggingFace API response for
// meta-llama/Llama-3.1-8B-Instruct.
var sampleHFPayload = map[string]interface{}{
	"modelId": "meta-llama/Llama-3.1-8B-Instruct",
	"siblings": []map[string]interface{}{
		{"rfilename": "Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf", "size": float64(4938035200)},
		{"rfilename": "Meta-Llama-3.1-8B-Instruct-Q8_0.gguf", "size": float64(8539357184)},
		{"rfilename": "config.json", "size": float64(654)},
	},
	"cardData": map[string]interface{}{"license": "llama3.1"},
}

// TestHandleImport_HuggingFace verifies the happy path: 201 + model in
// registry with the correct Source field.
func TestHandleImport_HuggingFace(t *testing.T) {
	hfSrv := mockHFServer(t, http.StatusOK, sampleHFPayload)
	defer hfSrv.Close()

	reg := newReg(t)
	srv := server.New(reg, server.Config{HFBaseURL: hfSrv.URL})

	body := `{"source":"huggingface","repo":"meta-llama/Llama-3.1-8B-Instruct","revision":"main"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var m registry.Model
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if m.ID != "Llama-3.1-8B-Instruct" {
		t.Errorf("id = %q, want Llama-3.1-8B-Instruct", m.ID)
	}
	if m.Family != "llama" {
		t.Errorf("family = %q, want llama", m.Family)
	}
	if len(m.Source) == 0 {
		t.Fatal("Source is empty, want HuggingFace provenance JSON")
	}

	// Verify the Source JSON shape.
	var src map[string]interface{}
	if err := json.Unmarshal(m.Source, &src); err != nil {
		t.Fatalf("decode Source: %v; raw=%s", err, m.Source)
	}
	if src["type"] != "huggingface" {
		t.Errorf("source.type = %v, want huggingface", src["type"])
	}
	if src["repo"] != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Errorf("source.repo = %v", src["repo"])
	}
	if src["filename"] == "" || src["filename"] == nil {
		t.Errorf("source.filename is empty")
	}
	if src["size_bytes_total"] == nil {
		t.Errorf("source.size_bytes_total is nil")
	}

	// Verify the model is stored in the registry.
	stored, err := reg.GetModel(context.Background(), "Llama-3.1-8B-Instruct")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if stored.Family != "llama" {
		t.Errorf("stored family = %q", stored.Family)
	}
	if len(stored.Source) == 0 {
		t.Error("stored Source is empty")
	}
}

// TestHandleImport_NotFound verifies that a 404 from HuggingFace produces a
// 404 response with the not_found error code.
func TestHandleImport_NotFound(t *testing.T) {
	hfSrv := mockHFServer(t, http.StatusNotFound, map[string]string{"error": "not found"})
	defer hfSrv.Close()

	reg := newReg(t)
	srv := server.New(reg, server.Config{HFBaseURL: hfSrv.URL})

	body := `{"source":"huggingface","repo":"no-such/repo","revision":"main"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "not_found" {
		t.Errorf("error = %v, want not_found", resp["error"])
	}
}

// TestHandleImport_AuthRequired verifies that a 401/403 from HuggingFace
// produces a 401 with the hf_auth_required error code.
func TestHandleImport_AuthRequired(t *testing.T) {
	for _, hfStatus := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		hfStatus := hfStatus
		t.Run(http.StatusText(hfStatus), func(t *testing.T) {
			hfSrv := mockHFServer(t, hfStatus, map[string]string{"error": "auth required"})
			defer hfSrv.Close()

			reg := newReg(t)
			srv := server.New(reg, server.Config{HFBaseURL: hfSrv.URL})

			body := `{"source":"huggingface","repo":"private/model","revision":"main"}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(body))
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
			}
			var resp map[string]interface{}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp["error"] != "hf_auth_required" {
				t.Errorf("error = %v, want hf_auth_required", resp["error"])
			}
		})
	}
}

// TestHandleImport_NoGGUFFiles verifies that a repo with no matching GGUF
// files returns 400 no_matching_files.
func TestHandleImport_NoGGUFFiles(t *testing.T) {
	payload := map[string]interface{}{
		"modelId": "org/SafetensorsOnly",
		"siblings": []map[string]interface{}{
			{"rfilename": "model.safetensors", "size": float64(5000000000)},
		},
	}
	hfSrv := mockHFServer(t, http.StatusOK, payload)
	defer hfSrv.Close()

	reg := newReg(t)
	srv := server.New(reg, server.Config{HFBaseURL: hfSrv.URL})

	body := `{"source":"huggingface","repo":"org/SafetensorsOnly","revision":"main"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no_matching_files") {
		t.Errorf("body missing no_matching_files: %s", rec.Body.String())
	}
}

// TestHandleImport_XHFTokenHeader verifies that the X-HF-Token header is
// forwarded to the HuggingFace API as the Authorization Bearer token.
func TestHandleImport_XHFTokenHeader(t *testing.T) {
	var gotAuth string
	hfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		payload := map[string]interface{}{
			"modelId": "org/GatedModel",
			"siblings": []map[string]interface{}{
				{"rfilename": "GatedModel-Q4_K_M.gguf", "size": float64(3000000000)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer hfSrv.Close()

	reg := newReg(t)
	srv := server.New(reg, server.Config{HFBaseURL: hfSrv.URL})

	body := `{"source":"huggingface","repo":"org/GatedModel","revision":"main"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(body))
	req.Header.Set("X-HF-Token", "hf_test_token_abc")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer hf_test_token_abc" {
		t.Errorf("Authorization = %q, want Bearer hf_test_token_abc", gotAuth)
	}
}
