package server_test

// Tests for E3 (optional TLS) and E4 (rate-limiting middleware).
//
// TLS tests exercise the handler chain via httptest.NewRecorder (no real
// listener needed — actual TLS negotiation is a stdlib concern). A separate
// sub-test uses a real net.Listener + tls.Dial to prove that an HTTPS listener
// is actually brought up when TLSCertPEM/TLSKeyPEM are configured.
//
// Rate-limit tests use httptest.NewRecorder and a tiny RPS ceiling so 429s
// are reliably triggered within a small burst.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
)

// ---------------------------------------------------------------------------
// Helper: generate a self-signed ECDSA cert/key PEM pair
// ---------------------------------------------------------------------------

// makeSelfSignedCert returns a PEM-encoded self-signed certificate and private
// key valid for "localhost" / 127.0.0.1 for one hour. No external deps.
func makeSelfSignedCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("selfSignedCert: generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("selfSignedCert: serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "test-purser-mgmt"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("selfSignedCert: create: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("selfSignedCert: marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// newSrvWithCfg creates a test server backed by a temp SQLite registry using
// the supplied Config. Addr defaults to ":0" when empty.
func newSrvWithCfg(t *testing.T, cfg server.Config) (*server.Server, registry.Registry) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	if cfg.Addr == "" {
		cfg.Addr = ":0"
	}
	return server.New(reg, cfg), reg
}

// ---------------------------------------------------------------------------
// E3: TLS — handler chain works with TLS configured
// ---------------------------------------------------------------------------

// TestTLSInMemoryHandlerChain confirms that the server is constructable and
// routes correctly when TLSCertPEM / TLSKeyPEM are set. The handler chain is
// tested through httptest.NewRecorder (no real TCP/TLS needed for this).
func TestTLSInMemoryHandlerChain(t *testing.T) {
	certPEM, keyPEM := makeSelfSignedCert(t)
	srv, _ := newSrvWithCfg(t, server.Config{
		TLSCertPEM: certPEM,
		TLSKeyPEM:  keyPEM,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// 200 OK (registry reachable) or 503 (registry empty but reachable) — both
	// are correct; 404/500 or any panic means the handler chain is broken.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("health status = %d; want 200 or 503 (got: %s)", rec.Code, rec.Body.String())
	}
}

// TestTLSInMemoryRealListen starts a real HTTPS listener using the in-memory
// PEM path, makes a TLS client connection, and verifies the server certificate
// is the one we generated. This proves that ListenAndServeTLS is called (not
// plain ListenAndServe) when TLSCertPEM/TLSKeyPEM are set.
func TestTLSInMemoryRealListen(t *testing.T) {
	certPEM, keyPEM := makeSelfSignedCert(t)

	// Build a client trust pool from the self-signed cert.
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	// Find a free port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	srv, _ := newSrvWithCfg(t, server.Config{
		Addr:       addr,
		TLSCertPEM: certPEM,
		TLSKeyPEM:  keyPEM,
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	// Wait until the port is accepting connections (max 2 s).
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	for {
		conn, err = net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start in time: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Perform a TLS handshake to verify the server presents our certificate.
	tlsConn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: time.Second},
		"tcp", addr,
		&tls.Config{RootCAs: pool, ServerName: "localhost"},
	)
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer tlsConn.Close()

	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("no peer certificates in TLS handshake")
	}
	if certs[0].Subject.CommonName != "test-purser-mgmt" {
		t.Errorf("peer cert CN = %q; want %q", certs[0].Subject.CommonName, "test-purser-mgmt")
	}

	// Shutdown cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// TestTLSFilePath verifies that when TLSCert and TLSKey point to PEM files the
// server is constructed without panic and the handler chain still works.
func TestTLSFilePath(t *testing.T) {
	certPEM, keyPEM := makeSelfSignedCert(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	srv, _ := newSrvWithCfg(t, server.Config{
		TLSCert: certPath,
		TLSKey:  keyPath,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("health status = %d; want 200 or 503", rec.Code)
	}
}

// TestNoTLS verifies plain HTTP mode (the default).
func TestNoTLS(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("health status = %d; want 200 or 503", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// E4: Rate limiting — per-IP
// ---------------------------------------------------------------------------

// TestRateLimitPerIPExceeded fires N requests from the same IP with
// RateLimitRPS=1 (burst=1). All requests after the first must eventually
// produce a 429. We stop at the first 429 to keep the test fast.
func TestRateLimitPerIPExceeded(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS: 1, // burst=1; second request → 429
	})

	hit429 := false
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		req.RemoteAddr = "10.0.0.1:54321" // same IP for all requests
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			hit429 = true
			if ra := rec.Header().Get("Retry-After"); ra != "1" {
				t.Errorf("Retry-After = %q; want \"1\"", ra)
			}
			break
		}
	}
	if !hit429 {
		t.Error("expected at least one 429 from per-IP rate limiter")
	}
}

// TestRateLimitPerKeyExceeded verifies the per-API-key limiter by sending
// requests from different IPs (so the IP limiter does not trigger) but all
// using the same bearer token.
func TestRateLimitPerKeyExceeded(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS:    1000, // generous — won't trigger
		RateLimitKeyRPS: 1,    // burst=1; second same-key request → 429
	})

	hit429 := false
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		req.RemoteAddr = fmt.Sprintf("10.0.%d.1:12345", i) // different IPs
		req.Header.Set("Authorization", "Bearer psk_same-key-for-all-requests")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			hit429 = true
			break
		}
	}
	if !hit429 {
		t.Error("expected at least one 429 from per-key rate limiter")
	}
}

// ---------------------------------------------------------------------------
// E4: Rate limiting — exempt endpoints
// ---------------------------------------------------------------------------

// TestRateLimitExemptHealth verifies that GET /api/v1/cluster/health is never
// rate-limited regardless of how tight the per-IP limit is.
func TestRateLimitExemptHealth(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS: 0.001, // near-zero; any non-exempt endpoint would 429
	})

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/health", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("iteration %d: health endpoint got 429; must be exempt", i)
		}
	}
}

// TestRateLimitExemptOpenAPI verifies that GET /api/v1/openapi.json is exempt.
func TestRateLimitExemptOpenAPI(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS: 0.001,
	})

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("iteration %d: openapi endpoint got 429; must be exempt", i)
		}
	}
}

// ---------------------------------------------------------------------------
// E4: Rate limiting — edge cases
// ---------------------------------------------------------------------------

// TestRateLimitInternalTokenNotSubjectToKeyLimit verifies that the internal
// gateway token is not counted against the per-key limiter.
func TestRateLimitInternalTokenNotSubjectToKeyLimit(t *testing.T) {
	const intTok = "purser-internal-secret"
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS:    1000,
		RateLimitKeyRPS: 1, // would 429 a regular key on second request
		InternalToken:   intTok,
	})

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		req.Header.Set("Authorization", "Bearer "+intTok)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("iteration %d: internal token got 429; must be exempt from key limit", i)
		}
	}
}

// TestRateLimitDisabledWhenNegativeRPS verifies that RateLimitRPS=-1 turns
// off per-IP rate limiting entirely.
func TestRateLimitDisabledWhenNegativeRPS(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS: -1, // explicitly disabled
	})

	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("iteration %d: got 429 but rate limiting is disabled", i)
		}
	}
}

// TestRateLimitDifferentIPsNotThrottled ensures that different source IPs each
// get their own independent token bucket (one IP being throttled does not
// affect a different IP).
func TestRateLimitDifferentIPsNotThrottled(t *testing.T) {
	srv, _ := newSrvWithCfg(t, server.Config{
		RateLimitRPS: 1, // burst=1
	})

	// Exhaust the bucket for IP A.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
		req.RemoteAddr = "10.0.0.1:1111"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
	}

	// IP B should get through on its first request (fresh bucket).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.RemoteAddr = "10.0.0.2:2222"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Error("different IP was rate-limited by another IP's exhausted bucket")
	}
}
