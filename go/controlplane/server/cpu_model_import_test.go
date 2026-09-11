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

// TestCPUModelImport_KnownModel verifies that importing a known model
// (tinyllama-1b) returns 201 and a correct ModelSpec with CPU engine,
// expected family, and source provenance pointing to the GGUF path.
func TestCPUModelImport_KnownModel(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	body := `{
		"model_id":    "tinyllama-1.1b",
		"gguf_path":   "/home/user/.purser/models/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf",
		"context_max": 2048
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import/cpu", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var m registry.Model
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, rec.Body.String())
	}
	if m.Engine != "cpu" {
		t.Errorf("engine = %q, want cpu", m.Engine)
	}
	if m.Family != "llama" {
		t.Errorf("family = %q, want llama", m.Family)
	}
	if m.ParamsTotalB < 1.0 || m.ParamsTotalB > 1.5 {
		t.Errorf("params_total_b = %.2f, want ~1.1", m.ParamsTotalB)
	}

	// Source provenance must carry the gguf_path and type=cpu.
	var src map[string]interface{}
	if err := json.Unmarshal(m.Source, &src); err != nil {
		t.Fatalf("decode Source: %v; raw=%s", err, m.Source)
	}
	if src["type"] != "cpu" {
		t.Errorf("source.type = %v, want cpu", src["type"])
	}
	if src["gguf_path"] == "" || src["gguf_path"] == nil {
		t.Error("source.gguf_path is empty")
	}

	// Verify the model is stored in the registry.
	stored, err := reg.GetModel(context.Background(), "tinyllama-1.1b")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if stored.Engine != "cpu" {
		t.Errorf("stored engine = %q, want cpu", stored.Engine)
	}
}

// TestCPUModelImport_UnknownModel verifies that an unrecognised model_id
// returns 422 Unprocessable Entity with the unknown_model error code.
func TestCPUModelImport_UnknownModel(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	body := `{
		"model_id":  "no-such-model-xyz",
		"gguf_path": "/tmp/model.gguf"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import/cpu", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "unknown_model" {
		t.Errorf("error = %v, want unknown_model", resp["error"])
	}
}

// TestCPUModelImport_MissingGgufPath verifies that omitting gguf_path returns
// 400 Bad Request.
func TestCPUModelImport_MissingGgufPath(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	body := `{"model_id": "tinyllama-1.1b"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import/cpu", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "bad_request" {
		t.Errorf("error = %v, want bad_request", resp["error"])
	}
}
