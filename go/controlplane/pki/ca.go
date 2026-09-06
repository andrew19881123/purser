package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// Role values recorded on issued certificates (stored in the OU field and the
// certs table). The Gateway and Agents authenticate with distinct roles.
const (
	RoleCA      = "ca"
	RoleAgent   = "agent"
	RoleGateway = "gateway"
)

// Cert lifecycle states persisted in the registry certs table.
const (
	StateIssued  = "issued"
	StateRevoked = "revoked"
	StateRotated = "rotated"
)

// Default validity windows. These are deliberately conservative for the MVP and
// are expected to be tuned in production (short leaf TTLs + rotation).
const (
	DefaultCATTL   = 10 * 365 * 24 * time.Hour // 10 years
	DefaultLeafTTL = 90 * 24 * time.Hour       // 90 days

	// RotationGracePeriod is how long the previous CA remains in the trust
	// bundle after rotation. This allows leaf certificates issued under the
	// old CA to remain valid while agents re-enroll under the new CA.
	RotationGracePeriod = 72 * time.Hour

	// crlDistributionPoint is the URL embedded in every issued leaf certificate.
	// Clients that implement OCSP/CRL checking can fetch the revocation list here.
	crlDistributionPoint = "http://control-plane.purser.internal/pki/crl.pem"
)

// Options configures the certificate authority.
type Options struct {
	// CommonName for the CA certificate subject.
	CommonName string
	// Dir, if non-empty, is where the CA key/cert are persisted as PEM
	// (ca.crt, ca.key) so the CA survives restarts. If empty, the CA is
	// ephemeral (regenerated on every start) — acceptable for tests only.
	Dir string
	// CATTL / LeafTTL override the default validity windows.
	CATTL   time.Duration
	LeafTTL time.Duration
	// Clock is injectable for deterministic tests; defaults to time.Now.
	Clock func() time.Time
}

// Authority is the concrete internal CA. It is safe for concurrent use.
//
// It generates (or loads) a self-signed root CA on construction, issues ECDSA
// leaf certificates for Agents/Gateway, records issued-certificate metadata in
// the registry certs table, and supports revocation and rotation.
//
// Production deployments should use GenerateIntermediate to create an online
// intermediate CA for day-to-day leaf issuance, keeping the root CA offline.
type Authority struct {
	reg     registry.Registry
	clock   func() time.Time
	leafTTL time.Duration

	mu sync.RWMutex
	// activeCert/activeKey/activePEM is the currently-active signing CA.
	// In a hierarchical PKI this is the intermediate CA; in dev/test it is
	// the self-signed root CA.
	activeCert *x509.Certificate
	activeKey  *ecdsa.PrivateKey
	activePEM  []byte
	// oldCert is the previous CA certificate kept in the trust bundle during
	// the rotation grace period.  It is nil before the first rotation.
	oldCert   *x509.Certificate
	oldExpiry time.Time // when oldCert is removed from the trust bundle
	// rootCert is the root CA certificate that signed activeCert.  It is nil
	// when activeCert is itself the root (self-signed, dev/test mode).
	rootCert *x509.Certificate

	dir    string
	caTTL  time.Duration
	caName string
}

// compile-time assertion that Authority satisfies the CA interface.
var _ CA = (*Authority)(nil)

// New constructs an Authority, generating or loading the CA material.
func New(ctx context.Context, reg registry.Registry, opts Options) (*Authority, error) {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	if opts.CommonName == "" {
		opts.CommonName = "Purser Control Plane CA"
	}
	if opts.CATTL == 0 {
		opts.CATTL = DefaultCATTL
	}
	if opts.LeafTTL == 0 {
		opts.LeafTTL = DefaultLeafTTL
	}
	a := &Authority{
		reg:     reg,
		clock:   clock,
		leafTTL: opts.LeafTTL,
		dir:     opts.Dir,
		caTTL:   opts.CATTL,
		caName:  opts.CommonName,
	}
	if err := a.loadOrCreate(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Authority) now() time.Time { return a.clock().UTC() }

// loadOrCreate loads the CA from disk (if Dir is set and files exist) or
// generates a fresh self-signed CA.
func (a *Authority) loadOrCreate(ctx context.Context) error {
	if a.dir != "" {
		crtPath := filepath.Join(a.dir, "ca.crt")
		keyPath := filepath.Join(a.dir, "ca.key")
		crtPEM, errC := os.ReadFile(crtPath)
		keyPEM, errK := os.ReadFile(keyPath)
		if errC == nil && errK == nil {
			return a.adopt(ctx, crtPEM, keyPEM, false)
		}
	}
	return a.generate(ctx)
}

// adopt parses PEM material into the in-memory CA state. When persist is true
// the CA cert/key are also written to disk (if a Dir is configured).
//
// keyPEM may be encrypted (PURK magic prefix); the env var
// PURSER_PKI_KEY_PASSPHRASE is consulted for the passphrase.
func (a *Authority) adopt(ctx context.Context, crtPEM, keyPEM []byte, persist bool) error {
	blk, _ := pem.Decode(crtPEM)
	if blk == nil {
		return errors.New("pki: invalid CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return fmt.Errorf("pki: parse CA cert: %w", err)
	}
	// Decrypt the key if it carries the PURK encryption envelope.
	passphrase := os.Getenv("PURSER_PKI_KEY_PASSPHRASE")
	decrypted, err := decryptKeyPEM(keyPEM, passphrase)
	if err != nil {
		return fmt.Errorf("pki: decrypt CA key: %w", err)
	}
	kblk, _ := pem.Decode(decrypted)
	if kblk == nil {
		return errors.New("pki: invalid CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(kblk.Bytes)
	if err != nil {
		return fmt.Errorf("pki: parse CA key: %w", err)
	}
	a.mu.Lock()
	a.activeCert = cert
	a.activeKey = key
	a.activePEM = crtPEM
	a.mu.Unlock()
	if persist && a.dir != "" {
		if err := a.writeDisk(crtPEM, keyPEM); err != nil {
			return err
		}
	}
	// Record CA cert in the registry (idempotent upsert).
	a.recordCA(ctx, cert, crtPEM)
	return nil
}

// writeDisk persists the CA certificate and key to a.dir. The key is encrypted
// with AES-256-GCM if PURSER_PKI_KEY_PASSPHRASE is set.
func (a *Authority) writeDisk(crtPEM, keyPEM []byte) error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("pki: mkdir %q: %w", a.dir, err)
	}
	if err := os.WriteFile(filepath.Join(a.dir, "ca.crt"), crtPEM, 0o644); err != nil {
		return fmt.Errorf("pki: write ca.crt: %w", err)
	}
	passphrase := os.Getenv("PURSER_PKI_KEY_PASSPHRASE")
	encKey, err := encryptKeyPEM(keyPEM, passphrase)
	if err != nil {
		return fmt.Errorf("pki: encrypt ca.key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(a.dir, "ca.key"), encKey, 0o600); err != nil {
		return fmt.Errorf("pki: write ca.key: %w", err)
	}
	return nil
}

// generate creates a fresh self-signed root CA and persists it.
//
// The root CA template uses MaxPathLen=1, allowing it to sign exactly one level
// of intermediate CA. This replaces the previous MaxPathLenZero=true template
// which prevented the hierarchy Root → Intermediate → Leaf (GAP-03).
func (a *Authority) generate(ctx context.Context) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate CA key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return err
	}
	now := a.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         a.caName,
			Organization:       []string{"Purser"},
			OrganizationalUnit: []string{RoleCA},
		},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(a.caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1, // permits one level of intermediate CA (GAP-03)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("pki: create CA cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("pki: parse created CA cert: %w", err)
	}
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("pki: marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	a.mu.Lock()
	a.activeCert = cert
	a.activeKey = key
	a.activePEM = crtPEM
	a.mu.Unlock()

	if a.dir != "" {
		if err := a.writeDisk(crtPEM, keyPEM); err != nil {
			return err
		}
	}
	a.recordCA(ctx, cert, crtPEM)
	return nil
}

// GenerateIntermediate creates an intermediate CA signed by this root CA.
//
// The intermediate CA can sign leaf certificates (MaxPathLen=0) but cannot
// issue further subordinate CAs. In production deployments the root CA should
// be taken offline after signing the intermediate; day-to-day leaf issuance
// should go through the returned intermediate Authority.
func (a *Authority) GenerateIntermediate(ctx context.Context) (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate intermediate key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	now := a.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         "Purser Intermediate CA",
			Organization:       []string{"Purser"},
			OrganizationalUnit: []string{RoleCA},
		},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(2 * 365 * 24 * time.Hour),
		IsCA:                  true,
		MaxPathLen:            0,    // intermediate cannot issue sub-CAs
		MaxPathLenZero:        true, // explicit zero (distinguishable from unset)
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	a.mu.RLock()
	rootCert := a.activeCert
	rootKey := a.activeKey
	a.mu.RUnlock()

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, &key.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("pki: sign intermediate: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse intermediate cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	intermediate := &Authority{
		reg:        a.reg,
		clock:      a.clock,
		leafTTL:    a.leafTTL,
		dir:        a.dir,
		caTTL:      a.caTTL,
		caName:     "Purser Intermediate CA",
		activeCert: cert,
		activeKey:  key,
		activePEM:  certPEM,
		rootCert:   rootCert, // root included in trust bundle via CertPool
	}
	intermediate.recordCA(ctx, cert, certPEM)
	return intermediate, nil
}

// recordCA upserts the CA cert row in the registry (best-effort; a persistence
// failure must not prevent the CA from operating in-memory).
func (a *Authority) recordCA(ctx context.Context, cert *x509.Certificate, crtPEM []byte) {
	serial := cert.SerialNumber.String()
	rec := &registry.Cert{
		Serial:    serial,
		Subject:   cert.Subject.CommonName,
		Role:      RoleCA,
		PEM:       string(crtPEM),
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		State:     StateIssued,
	}
	if _, err := a.reg.GetCert(ctx, serial); err == nil {
		_ = a.reg.UpdateCert(ctx, rec)
		return
	}
	_ = a.reg.CreateCert(ctx, rec)
}

// CACertificate returns the active signing CA certificate.
func (a *Authority) CACertificate(ctx context.Context) (*x509.Certificate, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeCert == nil {
		return nil, errors.New("pki: CA not initialized")
	}
	return a.activeCert, nil
}

// CACert returns the active signing CA certificate without an error return.
// It returns nil if the CA has not been initialized.
func (a *Authority) CACert() *x509.Certificate {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeCert
}

// CACertPEM returns the PEM-encoded active CA certificate for trust
// bootstrapping (e.g. returned to Agents at Join for mTLS).
func (a *Authority) CACertPEM() []byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]byte, len(a.activePEM))
	copy(out, a.activePEM)
	return out
}

// CertPool returns an x509.CertPool for TLS verification that trusts:
//   - The active CA certificate.
//   - The root CA certificate (when this is an intermediate CA).
//   - The previous CA certificate during the 72-hour rotation grace period,
//     allowing leaf certs issued under the old CA to remain valid while agents
//     re-enroll under the new CA (zero-downtime rotation — GAP-05).
func (a *Authority) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeCert != nil {
		pool.AddCert(a.activeCert)
	}
	// Include root CA when operating as an intermediate.
	if a.rootCert != nil {
		pool.AddCert(a.rootCert)
	}
	// Include the old CA during the rotation grace period.
	if a.oldCert != nil && a.clock().UTC().Before(a.oldExpiry) {
		pool.AddCert(a.oldCert)
	}
	return pool
}

// Issue mints a new ECDSA leaf certificate for req, records its metadata in the
// registry, and returns the cert + private key PEM.
func (a *Authority) Issue(ctx context.Context, req CertRequest) (*IssuedCert, error) {
	if req.CommonName == "" {
		return nil, errors.New("pki: issue requires a CommonName")
	}
	a.mu.RLock()
	caCert, caKey := a.activeCert, a.activeKey
	a.mu.RUnlock()
	if caCert == nil || caKey == nil {
		return nil, errors.New("pki: CA not initialized")
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate leaf key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = a.leafTTL
	}
	role := req.Role
	if role == "" {
		role = RoleAgent
	}
	now := a.now()
	notBefore := now.Add(-1 * time.Minute)
	notAfter := now.Add(ttl)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         req.CommonName,
			Organization:       []string{"Purser"},
			OrganizationalUnit: []string{role},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              req.DNSNames,
		IPAddresses:           req.IPAddresses,
		CRLDistributionPoints: []string{crlDistributionPoint},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: sign leaf cert: %w", err)
	}
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal leaf key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	rec := &registry.Cert{
		Serial:    serial.String(),
		Subject:   req.CommonName,
		Role:      role,
		PEM:       string(crtPEM),
		NotBefore: notBefore,
		NotAfter:  notAfter,
		State:     StateIssued,
	}
	if err := a.reg.CreateCert(ctx, rec); err != nil {
		return nil, fmt.Errorf("pki: persist issued cert: %w", err)
	}
	return &IssuedCert{
		Serial:    serial.String(),
		CertPEM:   crtPEM,
		KeyPEM:    keyPEM,
		NotBefore: notBefore,
		NotAfter:  notAfter,
	}, nil
}

// Revoke marks the certificate with the given serial as revoked in the registry.
func (a *Authority) Revoke(ctx context.Context, serial string) error {
	c, err := a.reg.GetCert(ctx, serial)
	if err != nil {
		return fmt.Errorf("pki: revoke %q: %w", serial, err)
	}
	c.State = StateRevoked
	if err := a.reg.UpdateCert(ctx, c); err != nil {
		return fmt.Errorf("pki: revoke %q: %w", serial, err)
	}
	return nil
}

// IsRevoked reports whether the given serial has been revoked.
func (a *Authority) IsRevoked(ctx context.Context, serial string) (bool, error) {
	c, err := a.reg.GetCert(ctx, serial)
	if err != nil {
		return false, err
	}
	return c.State == StateRevoked, nil
}

// Rotate generates a fresh CA keypair, marks the previous CA as rotated in the
// registry, and makes the new CA active.
//
// The previous CA certificate is retained in the trust bundle (via CertPool)
// for RotationGracePeriod (72 hours) so that leaf certificates issued under it
// remain valid while agents re-enroll. This enables zero-downtime CA rotation
// (GAP-05).
func (a *Authority) Rotate(ctx context.Context) (*x509.Certificate, error) {
	// Snapshot the current active cert and move it to the grace-period slot.
	a.mu.Lock()
	prevCert := a.activeCert
	if prevCert != nil {
		a.oldCert = prevCert
		a.oldExpiry = a.clock().UTC().Add(RotationGracePeriod)
	}
	a.mu.Unlock()

	// Mark previous cert as rotated in the registry (best-effort).
	if prevCert != nil {
		if c, err := a.reg.GetCert(ctx, prevCert.SerialNumber.String()); err == nil {
			c.State = StateRotated
			_ = a.reg.UpdateCert(ctx, c)
		}
	}
	if err := a.generate(ctx); err != nil {
		return nil, err
	}
	return a.CACertificate(ctx)
}

// ForceGraceExpiry immediately expires the dual-trust grace window for the
// previous CA certificate.  Intended for use in tests only.
func (a *Authority) ForceGraceExpiry() {
	a.mu.Lock()
	a.oldExpiry = time.Now().Add(-1 * time.Second)
	a.mu.Unlock()
}

// VerifyClient verifies a leaf certificate PEM against the current CA, checking
// it is valid for client authentication and (if a registry lookup succeeds) not
// revoked. It returns the parsed leaf on success.
func (a *Authority) VerifyClient(ctx context.Context, certPEM []byte) (*x509.Certificate, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return nil, errors.New("pki: invalid certificate PEM")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse leaf: %w", err)
	}
	opts := x509.VerifyOptions{
		Roots:       a.CertPool(),
		CurrentTime: a.now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return nil, fmt.Errorf("pki: verify leaf: %w", err)
	}
	if revoked, err := a.IsRevoked(ctx, leaf.SerialNumber.String()); err == nil && revoked {
		return nil, fmt.Errorf("pki: certificate %s is revoked", leaf.SerialNumber.String())
	}
	return leaf, nil
}

// randSerial returns a random 128-bit positive serial number.
func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("pki: serial: %w", err)
	}
	// Ensure non-zero.
	return n.Add(n, big.NewInt(1)), nil
}
