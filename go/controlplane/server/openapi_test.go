package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleOpenAPISpec verifies that GET /api/v1/openapi.json:
//   - returns HTTP 200 with Content-Type application/json;
//   - returns a non-empty body that is valid JSON;
//   - the JSON document contains an "openapi" field equal to "3.0.3".
func TestHandleOpenAPISpec(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("response body is empty; embedded openapi.json must be non-empty")
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not valid JSON: %v; raw (first 200 bytes)=%.200s", err, body)
	}

	openapi, ok := doc["openapi"].(string)
	if !ok {
		t.Fatalf("\"openapi\" field missing or not a string; doc keys: %v", keys(doc))
	}
	if openapi != "3.0.3" {
		t.Errorf("openapi = %q, want \"3.0.3\"", openapi)
	}
}

// keys returns the top-level keys of a map (for diagnostic messages).
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
