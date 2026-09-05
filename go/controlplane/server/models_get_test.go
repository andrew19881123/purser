package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// TestHandleGetModel exercises GET /api/v1/models/{id}:
// 200 for an existing model, 404 for a missing one.
func TestHandleGetModel(t *testing.T) {
	ctx := context.Background()

	// GET an existing model -> 200 with the model JSON.
	t.Run("existing", func(t *testing.T) {
		reg := newReg(t)
		if err := reg.CreateModel(ctx, &registry.Model{ID: "m-get", Family: "llama", Architecture: "llama"}); err != nil {
			t.Fatalf("seed model: %v", err)
		}
		srv := server.New(reg, server.Config{})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/m-get", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"m-get"`) {
			t.Errorf("body missing model id: %s", body)
		}
		if !strings.Contains(body, `"llama"`) {
			t.Errorf("body missing family: %s", body)
		}
	})

	// GET a model that was never created -> 404.
	t.Run("missing", func(t *testing.T) {
		reg := newReg(t)
		srv := server.New(reg, server.Config{})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "not_found") {
			t.Errorf("404 body missing error type: %s", body)
		}
	})

	// GET /models/{id} must not shadow the more-specific sub-paths.
	// A request to /models/{id}/health must still reach handleModelHealth
	// (which returns a 404 for a model without deployments, not a model JSON).
	t.Run("does not shadow sub-paths", func(t *testing.T) {
		reg := newReg(t)
		if err := reg.CreateModel(ctx, &registry.Model{ID: "m-sub", Family: "llama"}); err != nil {
			t.Fatalf("seed model: %v", err)
		}
		srv := server.New(reg, server.Config{})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models/m-sub/health", nil))
		// handleModelHealth returns 200 (with status "unavailable") for an
		// existing model that has no deployments — not the model JSON itself.
		if rec.Code != http.StatusOK {
			t.Fatalf("health sub-path: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"family"`) {
			t.Errorf("health endpoint returned model JSON — handleGetModel shadowed the sub-path: %s", rec.Body.String())
		}
	})
}
