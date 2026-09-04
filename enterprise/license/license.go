// Package license implements Purser's open-core license gate.
//
// Purser is AGPL-3.0 community software; a handful of enterprise capabilities
// (see LICENSING.md) are compiled into the public tree but only *enabled* in
// production by a valid, cryptographically signed license key. Verification is
// entirely OFFLINE — Purser is designed to run air-gapped, so there is no
// phone-home, no license server, and no network dependency of any kind. A key
// is a self-contained, ed25519-signed token that any node can validate against
// an embedded public key.
//
// # Key format
//
//	base64url(payloadJSON) "." base64url(ed25519_signature)
//
// where payloadJSON is:
//
//	{"licensee":"Acme Corp","features":["audit","rbac"],
//	 "issued":"2026-01-01T00:00:00Z","expires":"2027-01-01T00:00:00Z"}
//
// The signature is computed over the raw payload JSON bytes (not the base64
// text). base64url is RFC 4648 URL-safe base64 without padding, so a key is a
// single copy-pasteable, shell-safe string.
//
// # Trust root
//
// Verify checks signatures against [VerificationKey], which is initialized from
// the embedded [DevPublicKeyBase64]. The shipped value is a DEVELOPMENT key
// whose private half was discarded at generation time — no one can mint a key
// that validates against it. To issue real licenses a maintainer runs
// `purser-license keygen`, replaces DevPublicKeyBase64 with the printed public
// key, and keeps the generated private key OFF the repository (it is
// .gitignored). See cmd/purser-license.
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// EnvVar is the environment variable the control plane reads a license key
// from at startup (see [FromEnv]).
const EnvVar = "PURSER_LICENSE_KEY"

// DevPublicKeyBase64 is the standard-base64 ed25519 public key that Verify
// trusts by default.
//
// SECURITY: this is a placeholder DEVELOPMENT key. It was generated with
// `purser-license keygen` and its PRIVATE half was intentionally discarded, so
// no key validates against it in a stock build — enterprise features stay off
// until a maintainer provisions their own trust root. To go to production:
//
//  1. run `purser-license keygen` (writes a gitignored private key file and
//     prints the public key);
//  2. replace the value below with the printed public key;
//  3. store the private key in a secret manager — NEVER commit it.
const DevPublicKeyBase64 = "u0M8yOk/o4XkSNTnHY5+ZcmGrv/f8+cVh61uuO33p68="

// VerificationKey is the ed25519 public key [Verify] checks signatures against.
// It is initialized to the key decoded from [DevPublicKeyBase64].
//
// It is an exported package variable purely so tests can inject an ephemeral
// key: generate a keypair in-memory, sign a license with [Sign], swap this
// variable, verify, then restore it — all without any private key touching the
// repository. Production code should leave it at its embedded default.
var VerificationKey = mustDecodeKey(DevPublicKeyBase64)

func mustDecodeKey(b64 string) ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		panic("license: DevPublicKeyBase64 is not valid base64: " + err.Error())
	}
	if len(raw) != ed25519.PublicKeySize {
		panic(fmt.Sprintf("license: embedded public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize))
	}
	return ed25519.PublicKey(raw)
}

// Sentinel errors returned by Verify. Callers can match with errors.Is.
var (
	// ErrMalformed indicates the key is not a "<payload>.<signature>" pair or a
	// segment is not valid base64url.
	ErrMalformed = errors.New("license: malformed key")
	// ErrBadSignature indicates the signature does not verify against
	// VerificationKey (wrong key, or the payload was tampered with).
	ErrBadSignature = errors.New("license: signature verification failed")
)

// Payload is the JSON document that is signed to form a license key. Times are
// serialized as RFC 3339 (Go's default time.Time JSON encoding).
type Payload struct {
	Licensee string    `json:"licensee"`
	Features []string  `json:"features"`
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"`
}

// License is a verified, decoded license. The community (unlicensed) license is
// the zero value plus no features; see [Community].
type License struct {
	Licensee string    `json:"licensee"`
	Features []string  `json:"features"`
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"`
}

// Community returns the license granted when no key is present: no licensee, no
// features, so every premium entitlement check fails. It is never nil.
func Community() *License { return &License{} }

// IsCommunity reports whether l grants no enterprise entitlements — i.e. it is
// the unlicensed community edition (nil, or no licensee and no features).
func (l *License) IsCommunity() bool {
	return l == nil || (l.Licensee == "" && len(l.Features) == 0)
}

// ValidAt reports whether l is temporally valid at instant t: t is at or after
// Issued (when set) and strictly before Expires (when set). A zero Expires
// means "no expiry". Note that temporal validity is independent of feature
// entitlement — gate a premium feature on both ValidAt and [License.HasFeature].
func (l *License) ValidAt(t time.Time) bool {
	if l == nil {
		return false
	}
	if !l.Issued.IsZero() && t.Before(l.Issued) {
		return false
	}
	if !l.Expires.IsZero() && !t.Before(l.Expires) {
		return false
	}
	return true
}

// HasFeature reports whether name is in the license's feature set.
func (l *License) HasFeature(name string) bool {
	if l == nil {
		return false
	}
	for _, f := range l.Features {
		if f == name {
			return true
		}
	}
	return false
}

// Sign builds and signs a license key from p using priv. It is the canonical
// encoder shared by the signer tool (cmd/purser-license) and tests, ensuring
// they produce exactly what [Verify] expects. The signature covers the raw
// payload JSON bytes.
func Sign(priv ed25519.PrivateKey, p Payload) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("license: private key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("license: marshal payload: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify decodes key, checks its ed25519 signature against [VerificationKey],
// and returns the decoded license. It does NOT check expiry — call
// [License.ValidAt] for that — so callers can distinguish "forged" from
// "expired". It returns [ErrMalformed] or [ErrBadSignature] on failure.
func Verify(key string) (*License, error) {
	parts := strings.Split(strings.TrimSpace(key), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: payload: %v", ErrMalformed, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: signature: %v", ErrMalformed, err)
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(VerificationKey, payload, sig) {
		return nil, ErrBadSignature
	}
	var p Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("%w: payload json: %v", ErrMalformed, err)
	}
	return &License{
		Licensee: p.Licensee,
		Features: p.Features,
		Issued:   p.Issued,
		Expires:  p.Expires,
	}, nil
}

// FromEnv reads a license key from $PURSER_LICENSE_KEY and verifies it. An
// unset/empty variable is NOT an error: it returns the community license (see
// [Community]) so the community edition boots with enterprise features simply
// off. A present-but-invalid key returns the verification error.
func FromEnv() (*License, error) {
	key := strings.TrimSpace(os.Getenv(EnvVar))
	if key == "" {
		return Community(), nil
	}
	return Verify(key)
}
