package license_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/enterprise/license"
)

// withEphemeralKey generates an in-memory ed25519 keypair, points the package
// verifier at the public half for the duration of the test, and returns the
// private half for signing. No private key ever touches disk or the repo.
func withEphemeralKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prev := license.VerificationKey
	license.VerificationKey = pub
	t.Cleanup(func() { license.VerificationKey = prev })
	return priv
}

func TestSignVerifyRoundTrip(t *testing.T) {
	priv := withEphemeralKey(t)

	now := time.Now().UTC().Truncate(time.Second)
	key, err := license.Sign(priv, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit", "rbac"},
		Issued:   now,
		Expires:  now.Add(365 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Format: base64url(payload) "." base64url(sig), two non-empty segments.
	parts := strings.Split(key, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("key format = %q, want two dot-separated segments", key)
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil {
		t.Errorf("payload segment is not base64url: %v", err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		t.Errorf("signature segment is not base64url: %v", err)
	}

	lic, err := license.Verify(key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if lic.Licensee != "Acme Corp" {
		t.Errorf("licensee = %q, want Acme Corp", lic.Licensee)
	}
	if !lic.HasFeature("audit") || !lic.HasFeature("rbac") {
		t.Errorf("features = %v, want audit+rbac", lic.Features)
	}
	if lic.HasFeature("sso") {
		t.Errorf("unexpectedly has feature sso")
	}
	if !lic.ValidAt(now.Add(time.Hour)) {
		t.Errorf("license should be valid one hour after issue")
	}
}

func TestVerifyTamperedSignatureFails(t *testing.T) {
	priv := withEphemeralKey(t)

	key, err := license.Sign(priv, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit"},
		Issued:   time.Now().UTC(),
		Expires:  time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Decode the signature, flip a bit in its first byte, and re-encode. Working
	// on the decoded bytes (rather than a base64 char) guarantees a genuinely
	// different, still 64-byte signature.
	parts := strings.SplitN(key, ".", 2)
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	sig[0] ^= 0x01
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(sig)

	if _, err := license.Verify(tampered); !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("verify(tampered sig) err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyTamperedPayloadFails(t *testing.T) {
	priv := withEphemeralKey(t)

	key, err := license.Sign(priv, license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit"},
		Issued:   time.Now().UTC(),
		Expires:  time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Re-encode a payload that grants an extra feature, keep the old signature.
	forged := license.Payload{
		Licensee: "Acme Corp",
		Features: []string{"audit", "rbac", "sso"},
		Issued:   time.Now().UTC(),
		Expires:  time.Now().UTC().Add(time.Hour),
	}
	forgedJSON, _ := json.Marshal(forged)
	parts := strings.SplitN(key, ".", 2)
	spoofed := base64.RawURLEncoding.EncodeToString(forgedJSON) + "." + parts[1]

	if _, err := license.Verify(spoofed); !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("verify(tampered payload) err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyWrongKeyFails(t *testing.T) {
	// Sign with one keypair, verify against a different embedded key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate verify key: %v", err)
	}
	prev := license.VerificationKey
	license.VerificationKey = otherPub
	t.Cleanup(func() { license.VerificationKey = prev })

	key, err := license.Sign(priv, license.Payload{
		Licensee: "Mallory",
		Expires:  time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := license.Verify(key); !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("verify(wrong key) err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"no dot":         "abc",
		"three segments": "a.b.c",
		"empty payload":  ".sig",
		"empty sig":      "payload.",
		"bad base64":     "!!!.???",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := license.Verify(key); err == nil {
				t.Fatalf("verify(%q) = nil error, want malformed/bad-signature", key)
			}
		})
	}
}

func TestValidAtExpiry(t *testing.T) {
	priv := withEphemeralKey(t)

	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := issued.Add(24 * time.Hour)
	key, err := license.Sign(priv, license.Payload{
		Licensee: "Acme",
		Features: []string{"audit"},
		Issued:   issued,
		Expires:  expires,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	lic, err := license.Verify(key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if lic.ValidAt(issued.Add(-time.Second)) {
		t.Errorf("should not be valid before issue")
	}
	if !lic.ValidAt(issued.Add(time.Hour)) {
		t.Errorf("should be valid mid-window")
	}
	if lic.ValidAt(expires) {
		t.Errorf("should not be valid at the exact expiry instant")
	}
	if lic.ValidAt(expires.Add(time.Hour)) {
		t.Errorf("expired license reported valid")
	}
}

func TestFromEnv(t *testing.T) {
	priv := withEphemeralKey(t)

	t.Run("absent yields community", func(t *testing.T) {
		t.Setenv(license.EnvVar, "")
		lic, err := license.FromEnv()
		if err != nil {
			t.Fatalf("FromEnv (unset) err = %v, want nil", err)
		}
		if !lic.IsCommunity() {
			t.Errorf("unset env should yield community license, got %+v", lic)
		}
		if lic.HasFeature("audit") {
			t.Errorf("community license must not grant features")
		}
	})

	t.Run("valid key parsed", func(t *testing.T) {
		key, err := license.Sign(priv, license.Payload{
			Licensee: "Acme",
			Features: []string{"audit"},
			Issued:   time.Now().UTC(),
			Expires:  time.Now().UTC().Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		t.Setenv(license.EnvVar, key)
		lic, err := license.FromEnv()
		if err != nil {
			t.Fatalf("FromEnv (valid) err = %v", err)
		}
		if lic.IsCommunity() || !lic.HasFeature("audit") {
			t.Errorf("expected enterprise license with audit, got %+v", lic)
		}
	})

	t.Run("invalid key errors", func(t *testing.T) {
		t.Setenv(license.EnvVar, "garbage.notasignature")
		if _, err := license.FromEnv(); err == nil {
			t.Fatalf("FromEnv (invalid) err = nil, want error")
		}
	})
}

func TestCommunityHelpers(t *testing.T) {
	c := license.Community()
	if !c.IsCommunity() {
		t.Errorf("Community() should report IsCommunity")
	}
	if c.HasFeature("audit") {
		t.Errorf("Community() should have no features")
	}
	var nilLic *license.License
	if !nilLic.IsCommunity() {
		t.Errorf("nil license should be community")
	}
	if nilLic.ValidAt(time.Now()) {
		t.Errorf("nil license should never be valid")
	}
}

// TestProductionKeyRoundTrip verifies that the production trust root is a REAL
// keypair — a license signed with the production private key (embedded in
// ProductionPublicKeyBase64) validates against the package-level VerificationKey
// without any key swapping. This test would fail if ProductionPublicKeyBase64
// were still a placeholder whose private half was discarded.
//
// The private key is read from the well-known .gitignored file
// purser-license-signing.key. When that file is absent (e.g. CI without the
// secret) the test is skipped — it is a maintainer-only sanity check that
// the embedded public key matches the stored private key, not a required-on-
// every-build proof.
func TestProductionKeyRoundTrip(t *testing.T) {
	privB64Raw, err := os.ReadFile("purser-license-signing.key")
	if err != nil {
		t.Skipf("purser-license-signing.key not present — skipping production key round-trip (%v)", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(privB64Raw)))
	if err != nil {
		t.Fatalf("decode private key: %v", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		t.Fatalf("private key is %d bytes, want %d", len(decoded), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(decoded)

	// Derive the expected public key from the embedded constant and confirm it
	// matches the public half of the loaded private key.
	embeddedPub, err := base64.StdEncoding.DecodeString(license.ProductionPublicKeyBase64)
	if err != nil {
		t.Fatalf("decode ProductionPublicKeyBase64: %v", err)
	}
	derivedPub := priv.Public().(ed25519.PublicKey)
	if !bytes.Equal([]byte(derivedPub), embeddedPub) {
		t.Fatalf("ProductionPublicKeyBase64 does not match the public half of purser-license-signing.key — key mismatch")
	}

	// Sign a license with the production private key and verify it against the
	// default VerificationKey (which is initialized from ProductionPublicKeyBase64).
	// No key swapping needed — this is the real production path.
	now := time.Now().UTC().Truncate(time.Second)
	key, err := license.Sign(priv, license.Payload{
		Licensee: "Purser Production Test",
		Features: []string{"audit", "ha", "rbac"},
		Issued:   now,
		Expires:  now.Add(8760 * time.Hour),
	})
	if err != nil {
		t.Fatalf("sign with production key: %v", err)
	}

	lic, err := license.Verify(key)
	if err != nil {
		t.Fatalf("verify with production key: %v", err)
	}
	if lic.Licensee != "Purser Production Test" {
		t.Errorf("licensee = %q, want \"Purser Production Test\"", lic.Licensee)
	}
	if !lic.HasFeature("audit") || !lic.HasFeature("ha") || !lic.HasFeature("rbac") {
		t.Errorf("features = %v, want audit+ha+rbac", lic.Features)
	}
	if !lic.ValidAt(now.Add(time.Hour)) {
		t.Errorf("license should be valid one hour after issue")
	}
}
