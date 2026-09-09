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

// captureRunBoth calls run(args) capturing stdout and stderr separately, and
// returns (stdout, stderr, error). Needed for the paths that deliberately split
// machine output (the key, on stdout) from operator warnings (on stderr).
func captureRunBoth(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stdout): %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stderr): %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	// Drain both pipes concurrently: a writer that fills the 64 KiB pipe buffer
	// would otherwise block run() forever.
	type result struct{ s string }
	outCh, errCh := make(chan result, 1), make(chan result, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(outR)
		outCh <- result{b.String()}
	}()
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(errR)
		errCh <- result{b.String()}
	}()

	runErr := run(args)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return (<-outCh).s, (<-errCh).s, runErr
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
// gdpr" and verifies that both features appear in the decoded license. This is
// also the regression test that the signing crypto still round-trips through
// the feature-validation guard added in front of it.
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
		"--feature", "gdpr",
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
	if !lic.HasFeature("gdpr") {
		t.Errorf("expected feature 'gdpr', got: %v", lic.Features)
	}
	if lic.Licensee != "Test Corp" {
		t.Errorf("licensee = %q, want 'Test Corp'", lic.Licensee)
	}
}

// signArgs builds a minimal valid "sign" argv with the given trailing flags.
func signArgs(keyFile string, extra ...string) []string {
	base := []string{
		"sign",
		"--key", keyFile,
		"--licensee", "Test Corp",
		"--expires", "2027-01-01T00:00:00Z",
	}
	return append(base, extra...)
}

// withSigningKey generates an ephemeral keypair, points license.VerificationKey
// at the public half so Verify trusts what sign produces, and returns the path
// to a temp file holding the private half.
func withSigningKey(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prev := license.VerificationKey
	license.VerificationKey = pub
	t.Cleanup(func() { license.VerificationKey = prev })
	return writeKeyFile(t, priv)
}

// TestSignEveryKnownFeature is the happy path at the CLI boundary: each of the
// enforced flag strings signs successfully and survives a verify round-trip.
func TestSignEveryKnownFeature(t *testing.T) {
	for _, feat := range license.KnownFeatures() {
		t.Run(feat, func(t *testing.T) {
			keyFile := withSigningKey(t)
			out, runErr := captureRun(t, signArgs(keyFile, "--feature", feat))
			if runErr != nil {
				t.Fatalf("sign --feature %s returned error: %v\nstdout:\n%s", feat, runErr, out)
			}
			lic, err := license.Verify(strings.TrimSpace(out))
			if err != nil {
				t.Fatalf("verify signed key: %v", err)
			}
			if !lic.HasFeature(feat) {
				t.Errorf("signed key missing feature %q, got: %v", feat, lic.Features)
			}
		})
	}
}

// TestSignRejectsUnknownFeature is the core guard: an unrecognised flag string
// must abort signing, name the offending value, and emit no key on stdout.
func TestSignRejectsUnknownFeature(t *testing.T) {
	keyFile := withSigningKey(t)
	out, runErr := captureRun(t, signArgs(keyFile, "--feature", "totally_made_up"))
	if runErr == nil {
		t.Fatal("sign with an unknown feature returned nil error, want rejection")
	}
	if !strings.Contains(runErr.Error(), "totally_made_up") {
		t.Errorf("error must name the offending value, got: %v", runErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a rejected sign must not print a key to stdout, got:\n%s", out)
	}
}

// TestSignRejectsUnknownFeatureViaDeprecatedFlag ensures the comma-separated
// --features path is validated too, not just the repeatable --feature.
func TestSignRejectsUnknownFeatureViaDeprecatedFlag(t *testing.T) {
	keyFile := withSigningKey(t)
	out, runErr := captureRun(t, signArgs(keyFile, "--features", "audit,chargeback"))
	if runErr == nil {
		t.Fatal("sign --features with an unknown string returned nil error, want rejection")
	}
	if !strings.Contains(runErr.Error(), "chargeback") {
		t.Errorf("error must name the offending value, got: %v", runErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a rejected sign must not print a key to stdout, got:\n%s", out)
	}
}

// TestSignSuggestsCorrectFlagForProductName covers the two confusions that
// shipped in the docs: the error must point at the real flag string.
func TestSignSuggestsCorrectFlagForProductName(t *testing.T) {
	cases := map[string]string{
		"chargeback":   "billing",
		"opa_policies": "policy_engine",
	}
	for bad, want := range cases {
		t.Run(bad, func(t *testing.T) {
			keyFile := withSigningKey(t)
			_, runErr := captureRun(t, signArgs(keyFile, "--feature", bad))
			if runErr == nil {
				t.Fatalf("sign --feature %s returned nil error, want rejection", bad)
			}
			msg := runErr.Error()
			if !strings.Contains(msg, bad) {
				t.Errorf("error must name the offending value %q, got: %v", bad, runErr)
			}
			if !strings.Contains(msg, want) {
				t.Errorf("error must name the correct flag %q, got: %v", want, runErr)
			}
		})
	}
}

// TestSignSuggestsNearestMatchForTypo exercises the nearest-match path through
// the CLI.
func TestSignSuggestsNearestMatchForTypo(t *testing.T) {
	keyFile := withSigningKey(t)
	_, runErr := captureRun(t, signArgs(keyFile, "--feature", "inference_audi"))
	if runErr == nil {
		t.Fatal("sign with a typo'd feature returned nil error, want rejection")
	}
	msg := runErr.Error()
	if !strings.Contains(msg, "inference_audi") {
		t.Errorf("error must name the offending value, got: %v", runErr)
	}
	if !strings.Contains(msg, "inference_audit") {
		t.Errorf("error must suggest the nearest match, got: %v", runErr)
	}
}

// TestSignOverridePermitsUnknownFeature verifies the deliberate escape hatch:
// --allow-unknown-feature signs an unrecognised string, the resulting key still
// verifies and carries the string verbatim, and a loud warning is printed to
// STDERR (never stdout, which must stay a clean pipeable key).
func TestSignOverridePermitsUnknownFeature(t *testing.T) {
	keyFile := withSigningKey(t)
	out, errOut, runErr := captureRunBoth(t, signArgs(keyFile,
		"--feature", "audit",
		"--feature", "unreleased_thing",
		"--allow-unknown-feature",
	))
	if runErr != nil {
		t.Fatalf("sign --allow-unknown-feature returned error: %v\nstdout:\n%s\nstderr:\n%s", runErr, out, errOut)
	}

	keyStr := strings.TrimSpace(out)
	if keyStr == "" {
		t.Fatal("sign produced no key on stdout")
	}
	// stdout must be the key and nothing else, so it stays pipeable.
	if strings.Contains(keyStr, "\n") {
		t.Errorf("stdout must contain only the key line, got:\n%s", out)
	}

	lic, err := license.Verify(keyStr)
	if err != nil {
		t.Fatalf("verify overridden key: %v", err)
	}
	if !lic.HasFeature("unreleased_thing") {
		t.Errorf("override should sign the string verbatim, got: %v", lic.Features)
	}
	if !lic.HasFeature("audit") {
		t.Errorf("override must not drop the valid features, got: %v", lic.Features)
	}

	// The warning must be loud and must name the unvalidated string.
	if !strings.Contains(errOut, "WARNING") {
		t.Errorf("expected a loud WARNING on stderr, got:\n%s", errOut)
	}
	if !strings.Contains(errOut, "unreleased_thing") {
		t.Errorf("warning must name the unvalidated string, got:\n%s", errOut)
	}
	// The valid feature should not be reported as unvalidated.
	if strings.Contains(errOut, "audit") {
		t.Errorf("warning should only name unknown strings, not 'audit':\n%s", errOut)
	}
}

// TestSignOverrideIsSilentWhenAllFeaturesValid ensures the escape hatch does not
// cry wolf: passing it with an entirely valid feature set prints no warning.
func TestSignOverrideIsSilentWhenAllFeaturesValid(t *testing.T) {
	keyFile := withSigningKey(t)
	_, errOut, runErr := captureRunBoth(t, signArgs(keyFile,
		"--feature", "audit",
		"--allow-unknown-feature",
	))
	if runErr != nil {
		t.Fatalf("sign returned error: %v", runErr)
	}
	if strings.Contains(errOut, "WARNING") {
		t.Errorf("no warning expected when every feature is valid, got:\n%s", errOut)
	}
}

// TestVerifyWarnsOnUnknownFeature covers the diagnostic path for a key that was
// already issued with a bad string: verify must still exit 0 (the signature is
// genuinely valid) but must flag the string that unlocks nothing.
func TestVerifyWarnsOnUnknownFeature(t *testing.T) {
	priv := withEphemeralDevKey(t)

	keyStr, err := license.Sign(priv, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit", "chargeback"},
		Issued:   time.Now().UTC().Add(-time.Minute),
		Expires:  time.Now().UTC().Add(365 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	out, runErr := captureRun(t, []string{"verify", "--dev", keyStr})
	// A valid signature must keep exit 0 — scripts depend on that contract.
	if runErr != nil {
		t.Fatalf("verify of a validly-signed key must exit 0, got: %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "License: VALID") {
		t.Errorf("expected VALID block, got:\n%s", out)
	}
	if !strings.Contains(out, "chargeback") {
		t.Errorf("warning must name the offending feature, got:\n%s", out)
	}
	if !strings.Contains(out, "billing") {
		t.Errorf("warning should point at the correct flag 'billing', got:\n%s", out)
	}
	if !strings.Contains(strings.ToUpper(out), "WARNING") {
		t.Errorf("expected a WARNING line for the unknown feature, got:\n%s", out)
	}
}

// TestVerifyQuietForFullyValidKey ensures verify does not emit a spurious
// warning when every feature in the key is a real gate.
func TestVerifyQuietForFullyValidKey(t *testing.T) {
	priv := withEphemeralDevKey(t)

	keyStr, err := license.Sign(priv, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit", "billing"},
		Issued:   time.Now().UTC().Add(-time.Minute),
		Expires:  time.Now().UTC().Add(365 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	out, runErr := captureRun(t, []string{"verify", "--dev", keyStr})
	if runErr != nil {
		t.Fatalf("verify returned error: %v\n%s", runErr, out)
	}
	if strings.Contains(strings.ToUpper(out), "WARNING") {
		t.Errorf("no warning expected for a fully valid key, got:\n%s", out)
	}
}
