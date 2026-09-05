package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
)

// captureRun calls run(args), captures stdout, and returns (captured output,
// error). Stderr is not captured — flag parse errors and similar go to the
// test's stderr unchanged.
func captureRun(t *testing.T, args []string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := run(args)
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, readErr := buf.ReadFrom(r); readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	return buf.String(), runErr
}

// withEphemeralDevKey temporarily replaces license.DevVerificationKey with
// the public half of a freshly generated keypair and returns the private half
// for signing. The original key is restored via t.Cleanup.
func withEphemeralDevKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ephemeral dev key: %v", err)
	}
	prev := license.DevVerificationKey
	license.DevVerificationKey = pub
	t.Cleanup(func() { license.DevVerificationKey = prev })
	return priv
}

// writeKeyFile writes a base64-encoded ed25519 private key to a temp file
// inside t.TempDir() and returns the path.
func writeKeyFile(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "signing-*.key")
	if err != nil {
		t.Fatalf("create temp key file: %v", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv) + "\n"); err != nil {
		t.Fatalf("write temp key file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close temp key file: %v", err)
	}
	return f.Name()
}

// TestVerifySubcommand_ValidKey signs a license with an ephemeral dev key,
// verifies it via "verify --dev", and expects exit 0 plus a VALID output block
// containing the licensee, features, and "Valid now: yes".
func TestVerifySubcommand_ValidKey(t *testing.T) {
	priv := withEphemeralDevKey(t)

	keyStr, err := license.Sign(priv, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit", "ha", "rbac"},
		Issued:   time.Now().UTC().Add(-time.Minute),
		Expires:  time.Now().UTC().Add(365 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	out, runErr := captureRun(t, []string{"verify", "--dev", keyStr})
	if runErr != nil {
		t.Fatalf("verify --dev returned error: %v\nstdout:\n%s", runErr, out)
	}
	for _, want := range []string{"License: VALID", "Acme Corp", "audit", "ha", "Valid now: yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestVerifySubcommand_InvalidKey passes a garbage string to verify and expects
// a non-nil error return (exit 1 equivalent) and a INVALID output block.
func TestVerifySubcommand_InvalidKey(t *testing.T) {
	out, runErr := captureRun(t, []string{"verify", "garbage-not-a-license-key"})
	if runErr == nil {
		t.Fatal("verify(garbage) returned nil error, want non-nil (exit 1)")
	}
	if !strings.Contains(out, "License: INVALID") {
		t.Errorf("expected 'License: INVALID' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Error:") {
		t.Errorf("expected 'Error:' line in output, got:\n%s", out)
	}
}

// TestSignWithFeatureFlags signs a license via "sign --feature audit --feature
// ha" and verifies that both features appear in the decoded license.
func TestSignWithFeatureFlags(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyFile := writeKeyFile(t, priv)

	// Swap VerificationKey so license.Verify trusts what sign produces.
	prev := license.VerificationKey
	license.VerificationKey = pub
	t.Cleanup(func() { license.VerificationKey = prev })

	out, runErr := captureRun(t, []string{
		"sign",
		"--key", keyFile,
		"--licensee", "Test Corp",
		"--expires", "2027-01-01T00:00:00Z",
		"--feature", "audit",
		"--feature", "ha",
	})
	if runErr != nil {
		t.Fatalf("sign returned error: %v\nstdout:\n%s", runErr, out)
	}

	keyStr := strings.TrimSpace(out)
	if keyStr == "" {
		t.Fatal("sign produced empty output")
	}

	lic, err := license.Verify(keyStr)
	if err != nil {
		t.Fatalf("verify signed key: %v", err)
	}
	if !lic.HasFeature("audit") {
		t.Errorf("expected feature 'audit', got: %v", lic.Features)
	}
	if !lic.HasFeature("ha") {
		t.Errorf("expected feature 'ha', got: %v", lic.Features)
	}
	if lic.Licensee != "Test Corp" {
		t.Errorf("licensee = %q, want 'Test Corp'", lic.Licensee)
	}
}
