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

// TestHandleDeleteModel exercises the guarded DELETE /api/v1/models/{id}:
// a live delete (204 + gone from the catalog), a missing model (404), and a
// model pinned by a non-terminal deployment (409 model_in_use).
func TestHandleDeleteModel(t *testing.T) {
	ctx := context.Background()

	// Delete an existing, unreferenced model -> 204 and it disappears from
	// GET /api/v1/models.
	t.Run("existing", func(t *testing.T) {
		reg := newReg(t)
		if err := reg.CreateModel(ctx, &registry.Model{ID: "m-del", Family: "llama"}); err != nil {
			t.Fatalf("seed model: %v", err)
		}
		srv := server.New(reg, server.Config{})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/models/m-del", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("204 body = %q, want empty", rec.Body.String())
		}

		// Gone from the catalog listing.
		rec2 := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
		if rec2.Code != http.StatusOK || strings.Contains(rec2.Body.String(), "m-del") {
			t.Fatalf("model still listed: status=%d body=%s", rec2.Code, rec2.Body.String())
		}
		// ...and gone from the store.
		if _, err := reg.GetModel(ctx, "m-del"); err == nil {
			t.Error("GetModel still returns the deleted model")
		}
	})

	// Delete a model that was never created -> 404.
	t.Run("missing", func(t *testing.T) {
		reg := newReg(t)
		srv := server.New(reg, server.Config{})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/models/nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	// Delete a model pinned by a non-terminal (ACTIVE) deployment -> 409, the
	// blocking deployment id is echoed, and the model survives.
	t.Run("active deployment", func(t *testing.T) {
		reg := newReg(t)
		if err := reg.CreateModel(ctx, &registry.Model{ID: "m-busy", Family: "llama"}); err != nil {
			t.Fatalf("seed model: %v", err)
		}
		if err := reg.CreateDeployment(ctx, &registry.Deployment{ID: "d-live", ModelID: "m-busy", State: "DEPLOYMENT_STATE_ACTIVE"}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
		srv := server.New(reg, server.Config{})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/models/m-busy", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "model_in_use") || !strings.Contains(body, "d-live") {
			t.Errorf("409 body missing type or blocking id: %s", body)
		}
		// The model must NOT have been deleted.
		if _, err := reg.GetModel(ctx, "m-busy"); err != nil {
			t.Errorf("model was deleted despite active deployment: %v", err)
		}
	})

	// A terminal deployment (STOPPED) does not block the delete -> 204.
	t.Run("terminal deployment does not block", func(t *testing.T) {
		reg := newReg(t)
		if err := reg.CreateModel(ctx, &registry.Model{ID: "m-stopped", Family: "llama"}); err != nil {
			t.Fatalf("seed model: %v", err)
		}
		if err := reg.CreateDeployment(ctx, &registry.Deployment{ID: "d-stopped", ModelID: "m-stopped", State: "DEPLOYMENT_STATE_STOPPED"}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
		srv := server.New(reg, server.Config{})

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/models/m-stopped", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
		}
	})
}
