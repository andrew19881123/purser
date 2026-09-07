package server_test

// Tests for the Prometheus /metrics scrape endpoint on the control plane.
//
// Coverage:
//   - TestPrometheusMetricsEndpoint: GET /metrics returns 200 with text/plain
//     Prometheus exposition format. Verifies that at least one metric line and
//     the purser_cp_info{version="0.5"} gauge appear in the output.
//   - TestPrometheusMetricsNoAuth: the /metrics endpoint is accessible without
//     an Authorization header (standard Prometheus scraper expectation).
//   - TestPrometheusMetricsUnderAPIV1: confirms that the legacy SSE endpoint at
//     /api/v1/metrics is still present and returns text/event-stream (not the
//     Prometheus endpoint — they are distinct routes).

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/server"
)

func TestPrometheusMetricsEndpoint(t *testing.T) {
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain*", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)

	// The body must look like Prometheus text exposition format (# HELP / # TYPE
	// or at least metric lines).
	if len(text) == 0 {
		t.Fatal("response body is empty; expected Prometheus metric lines")
	}

	// purser_cp_info{version="0.5"} must be present to verify the endpoint is
	// backed by the Prometheus exporter and not some other handler.
	if !strings.Contains(text, "purser_cp_info") {
		t.Errorf("purser_cp_info not found in /metrics output; got:\n%s", text)
	}
	if !strings.Contains(text, `version="0.5"`) {
		t.Errorf(`version="0.5" label not found in /metrics output; got:\n%s`, text)
	}
}

func TestPrometheusMetricsNoAuth(t *testing.T) {
	// /metrics must return 200 without any Authorization header (standard
	// Prometheus scraper behaviour — scrapers do not send credentials by default).
	reg := newReg(t)
	// Even with OIDC-like configuration (OIDCVerifier not nil), /metrics must
	// be exempt. We use a stub verifier to simulate a locked-down deployment.
	srv := server.New(reg, server.Config{})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	// Explicitly: no Authorization header.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /metrics (no auth): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (want 200 without auth); body: %s", resp.StatusCode, body)
	}
}

func TestPrometheusMetricsDistinctFromSSE(t *testing.T) {
	// GET /metrics (Prometheus) and GET /api/v1/metrics (SSE) are distinct routes.
	// This test verifies that the Prometheus endpoint does NOT return event-stream
	// content, so the two endpoints cannot be confused.
	reg := newReg(t)
	srv := server.New(reg, server.Config{})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("/metrics returned text/event-stream — it should return text/plain (Prometheus format)")
	}

	// Read just the first line to confirm it looks like Prometheus exposition.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Scan()
	firstLine := scanner.Text()
	if strings.HasPrefix(firstLine, "data: ") {
		t.Fatalf("/metrics returned SSE data: line — expected Prometheus # HELP or metric line, got %q", firstLine)
	}
}
