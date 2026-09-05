package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOtelMiddlewareTransparent verifies that the OTEL HTTP middleware does not
// break existing handlers when no real TracerProvider is configured. All
// requests routed through Handler() must produce the same status codes as they
// would without the middleware. It reuses the existing newTestServer helper
// (defined in server_test.go) which returns (*server.Server, registry.Registry).
func TestOtelMiddlewareTransparent(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/nodes", http.StatusOK},
		{http.MethodGet, "/api/v1/models", http.StatusOK},
		{http.MethodGet, "/api/v1/deployments", http.StatusOK},
		{http.MethodGet, "/api/v1/cluster/health", http.StatusOK},
		{http.MethodGet, "/api/v1/enterprise/status", http.StatusOK},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d; body=%s",
				tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestOtelMiddlewareStatusCapture verifies that the statusWriter correctly
// captures the status code set by handlers so span attributes are accurate.
// We check a known-404 path (unknown node) to confirm a non-200 code flows
// through correctly.
func TestOtelMiddlewareStatusCapture(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/does-not-exist", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/nodes/does-not-exist = %d, want 404", rec.Code)
	}
}

// TestAuditSpanEventNoOp verifies that AppendAudit (called via a normal API
// path) does not error when no active span is in the context — the OTEL audit
// bridge must be a silent no-op when tracing is not configured.
func TestAuditSpanEventNoOp(t *testing.T) {
	srv, _ := newTestServer(t)

	// POST /api/v1/apikeys calls AppendAudit with r.Context(). When OTEL is
	// not configured the context has no active span and the bridge must be a
	// silent no-op (IsRecording() == false).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/apikeys = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}
