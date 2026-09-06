package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/server"
)

// TestRBACFailsClosedWhenAPIKeyExists verifies that unauthenticated requests to
// /api/v1/* are rejected with 401 once at least one API key exists in the registry
// (fail-closed behaviour — GAP-02).
func TestRBACFailsClosedWhenAPIKeyExists(t *testing.T) {
	reg := newReg(t)
	// Seed one API key so the registry is not empty.
	seedKeyWithRole(t, reg, "key-admin", "admin-key", "admin")
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	// Deliberately no Authorization header — should be rejected.
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when API key exists and no token provided, got %d; body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestRBACPassthroughWhenNoKeysAndNoOIDC verifies that an unauthenticated request
// passes through when no API keys exist and OIDC is not configured (pure dev mode).
func TestRBACPassthroughWhenNoKeysAndNoOIDC(t *testing.T) {
	reg := newReg(t)
	// No API keys seeded, no OIDC → dev mode should allow unauthenticated access.
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	srv.Handler().ServeHTTP(rec, req)

	// 200 OK (empty list) is the expected outcome.
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("unexpected 401 in dev mode (no keys, no OIDC); body=%s", rec.Body.String())
	}
}

// TestBodySizeLimitRejectsLargePayload verifies that POST /api/v1/models with a
// body larger than 1 MB is rejected with 413 Request Entity Too Large (GAP-09).
func TestBodySizeLimitRejectsLargePayload(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	// Construct a body that is just over 1 MB.
	largeBody := strings.Repeat("x", 1<<20+512)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models",
		strings.NewReader(largeBody))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCORSHeadersForAllowedOrigin verifies that a request from an allowed origin
// receives the Access-Control-Allow-Origin header (GAP-10).
func TestCORSHeadersForAllowedOrigin(t *testing.T) {
	t.Setenv("PURSER_ALLOWED_ORIGINS", "https://app.example.com")

	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("Origin", "https://app.example.com")
	srv.Handler().ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao == "" {
		t.Fatalf("expected Access-Control-Allow-Origin header for allowed origin, got none; headers=%v",
			rec.Header())
	}
}

// TestCORSHeadersAbsentForUnknownOrigin verifies that a request from an unknown
// origin does NOT receive the Access-Control-Allow-Origin header (GAP-10).
func TestCORSHeadersAbsentForUnknownOrigin(t *testing.T) {
	t.Setenv("PURSER_ALLOWED_ORIGINS", "https://app.example.com")

	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	srv.Handler().ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("expected NO Access-Control-Allow-Origin for unknown origin, got %q; headers=%v",
			acao, rec.Header())
	}
}

// TestCORSPreflightAllowedOrigin verifies that an OPTIONS preflight request for
// an allowed origin receives 204 No Content with the appropriate CORS headers.
func TestCORSPreflightAllowedOrigin(t *testing.T) {
	t.Setenv("PURSER_ALLOWED_ORIGINS", "https://app.example.com")

	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/models", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS preflight, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("expected Access-Control-Allow-Methods header in preflight response")
	}
}
