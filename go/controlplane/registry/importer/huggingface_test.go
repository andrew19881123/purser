package importer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/purser/purser/go/controlplane/registry/importer"
)

// mockHFResponse is a minimal HuggingFace /api/models/{repo}?blobs=true
// response used across tests.
type mockHFResponse struct {
	ModelID  string                   `json:"modelId"`
	Siblings []map[string]interface{} `json:"siblings"`
	CardData map[string]interface{}   `json:"cardData,omitempty"`
}

func TestHFClient_FetchMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models/meta-llama/Llama-3.1-8B-Instruct" {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		resp := mockHFResponse{
			ModelID: "meta-llama/Llama-3.1-8B-Instruct",
			Siblings: []map[string]interface{}{
				{"rfilename": "Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf", "size": float64(4938035200)},
				{"rfilename": "Meta-Llama-3.1-8B-Instruct-Q8_0.gguf", "size": float64(8539357184)},
				{"rfilename": "config.json", "size": float64(654)},
				{"rfilename": "tokenizer.json", "size": float64(9085593)},
			},
			CardData: map[string]interface{}{"license": "llama3.1"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := importer.NewHFClient("")
	client.BaseURL = srv.URL

	meta, err := client.FetchMetadata(context.Background(), "meta-llama/Llama-3.1-8B-Instruct", "main", "*.gguf")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if meta.Name != "Llama-3.1-8B-Instruct" {
		t.Errorf("Name = %q, want Llama-3.1-8B-Instruct", meta.Name)
	}
	if meta.Family != "llama" {
		t.Errorf("Family = %q, want llama", meta.Family)
	}
	if meta.License != "llama3.1" {
		t.Errorf("License = %q, want llama3.1", meta.License)
	}
	if len(meta.GGUFFiles) != 2 {
		t.Fatalf("GGUFFiles len = %d, want 2", len(meta.GGUFFiles))
	}
	var total int64
	for _, f := range meta.GGUFFiles {
		total += f.Size
	}
	wantTotal := int64(4938035200 + 8539357184)
	if total != wantTotal {
		t.Errorf("total size = %d, want %d", total, wantTotal)
	}
}

func TestHFClient_FetchMetadata_DefaultPattern(t *testing.T) {
	// Verify that an empty pattern defaults to "*.gguf".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mockHFResponse{
			ModelID: "org/SomeModel",
			Siblings: []map[string]interface{}{
				{"rfilename": "SomeModel-Q4_K_M.gguf", "size": float64(1000)},
				{"rfilename": "SomeModel.safetensors", "size": float64(2000)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := importer.NewHFClient("")
	client.BaseURL = srv.URL

	meta, err := client.FetchMetadata(context.Background(), "org/SomeModel", "main", "")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if len(meta.GGUFFiles) != 1 {
		t.Errorf("GGUFFiles len = %d, want 1", len(meta.GGUFFiles))
	}
}

func TestHFClient_ListGGUFFiles(t *testing.T) {
	// Mix of .gguf and .safetensors; verify only .gguf returned with correct sizes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mockHFResponse{
			ModelID: "mistralai/Mistral-7B-Instruct-v0.3",
			Siblings: []map[string]interface{}{
				{"rfilename": "Mistral-7B-Instruct-v0.3-Q4_K_M.gguf", "size": float64(4368438272)},
				{"rfilename": "Mistral-7B-Instruct-v0.3-Q8_0.gguf", "size": float64(7695286272)},
				{"rfilename": "model-00001-of-00003.safetensors", "size": float64(4999548928)},
				{"rfilename": "model-00002-of-00003.safetensors", "size": float64(4999548928)},
				{"rfilename": "model-00003-of-00003.safetensors", "size": float64(2096643072)},
				{"rfilename": "tokenizer.model", "size": float64(493443)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := importer.NewHFClient("")
	client.BaseURL = srv.URL

	files, err := client.ListGGUFFiles(context.Background(), "mistralai/Mistral-7B-Instruct-v0.3", "main", "*.gguf")
	if err != nil {
		t.Fatalf("ListGGUFFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	for _, f := range files {
		if f.Size <= 0 {
			t.Errorf("file %q has non-positive size %d", f.Name, f.Size)
		}
	}
}

func TestHFClient_ListGGUFFiles_SpecificPattern(t *testing.T) {
	// Verify that a specific pattern like "*.Q4_K_M.gguf" filters correctly.
	// Filenames use a dot before the quantisation suffix, e.g. "Phi-3-mini.Q4_K_M.gguf",
	// so that the pattern "*.Q4_K_M.gguf" matches (path.Match: * matches non-/).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mockHFResponse{
			ModelID: "org/Phi-3-mini",
			Siblings: []map[string]interface{}{
				{"rfilename": "Phi-3-mini.Q4_K_M.gguf", "size": float64(2100000000)},
				{"rfilename": "Phi-3-mini.Q8_0.gguf", "size": float64(3800000000)},
				{"rfilename": "Phi-3-mini.Q2_K.gguf", "size": float64(1200000000)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := importer.NewHFClient("")
	client.BaseURL = srv.URL

	files, err := client.ListGGUFFiles(context.Background(), "org/Phi-3-mini", "main", "*.Q4_K_M.gguf")
	if err != nil {
		t.Fatalf("ListGGUFFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Name != "Phi-3-mini.Q4_K_M.gguf" {
		t.Errorf("file name = %q, want Phi-3-mini.Q4_K_M.gguf", files[0].Name)
	}
}

func TestHFClient_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	client := importer.NewHFClient("")
	client.BaseURL = srv.URL

	_, err := client.FetchMetadata(context.Background(), "no-such/repo", "main", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !importer.IsNotFound(err) {
		t.Errorf("IsNotFound = false, want true; err = %v", err)
	}
	if importer.IsAuthRequired(err) {
		t.Errorf("IsAuthRequired = true, want false")
	}
}

func TestHFClient_AuthRequired(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"auth required"}`, code)
			}))
			defer srv.Close()

			client := importer.NewHFClient("")
			client.BaseURL = srv.URL

			_, err := client.FetchMetadata(context.Background(), "private/model", "main", "")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !importer.IsAuthRequired(err) {
				t.Errorf("IsAuthRequired = false, want true; err = %v", err)
			}
		})
	}
}
