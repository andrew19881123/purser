package transport_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/transport"
)

// TestDefault_NoEnv verifies that Default() succeeds when PURSER_CA_BUNDLE is
// not set and returns a transport that uses ProxyFromEnvironment.
func TestDefault_NoEnv(t *testing.T) {
	t.Setenv("PURSER_CA_BUNDLE", "")

	tr, err := transport.Default()
	if err != nil {
		t.Fatalf("Default() returned unexpected error: %v", err)
	}
	if tr == nil {
		t.Fatal("Default() returned nil transport")
	}
	// Verify that the transport uses ProxyFromEnvironment.
	if tr.Proxy == nil {
		t.Error("transport.Proxy must not be nil (expected ProxyFromEnvironment)")
	}
}

// TestDefault_NonexistentCABundle verifies that Default() returns an error when
// PURSER_CA_BUNDLE points to a file that does not exist.
func TestDefault_NonexistentCABundle(t *testing.T) {
	t.Setenv("PURSER_CA_BUNDLE", "/nonexistent/ca.pem")

	_, err := transport.Default()
	if err == nil {
		t.Fatal("expected an error for a non-existent CA bundle, got nil")
	}
}

// TestDefault_ValidCABundle verifies that Default() succeeds and returns a
// transport when PURSER_CA_BUNDLE points to a valid self-signed CA cert.
func TestDefault_ValidCABundle(t *testing.T) {
	// Generate a minimal self-signed CA certificate in PEM form.
	certPEM := generateSelfSignedCACert(t)

	tmp := t.TempDir()
	caFile := filepath.Join(tmp, "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("writing temp CA file: %v", err)
	}

	t.Setenv("PURSER_CA_BUNDLE", caFile)

	tr, err := transport.Default()
	if err != nil {
		t.Fatalf("Default() returned unexpected error with valid CA bundle: %v", err)
	}
	if tr == nil {
		t.Fatal("Default() returned nil transport")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("expected RootCAs to be populated when a CA bundle is provided")
	}
}

// TestDefault_EmptyPEMFile verifies that Default() returns an error when
// PURSER_CA_BUNDLE exists but contains no valid PEM certificates.
func TestDefault_EmptyPEMFile(t *testing.T) {
	tmp := t.TempDir()
	caFile := filepath.Join(tmp, "empty.pem")
	if err := os.WriteFile(caFile, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}

	t.Setenv("PURSER_CA_BUNDLE", caFile)

	_, err := transport.Default()
	if err == nil {
		t.Fatal("expected an error for a PEM file with no valid certificates, got nil")
	}
}

// TestDefault_SetsDefaultTransport verifies the intended usage pattern: the
// caller can assign the result to http.DefaultTransport.
func TestDefault_SetsDefaultTransport(t *testing.T) {
	t.Setenv("PURSER_CA_BUNDLE", "")

	tr, err := transport.Default()
	if err != nil {
		t.Fatalf("Default() error: %v", err)
	}

	// Save and restore the original DefaultTransport.
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	http.DefaultTransport = tr // must not panic
}

// generateSelfSignedCACert creates a minimal self-signed CA certificate and
// returns its PEM encoding.
func generateSelfSignedCACert(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}
