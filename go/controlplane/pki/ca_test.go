package pki_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
)

func openReg(t *testing.T) registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

func TestCA_GenerateAndIssueVerify(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, err := pki.New(ctx, reg, pki.Options{})
	if err != nil {
		t.Fatalf("new CA: %v", err)
	}

	// CA cert must be available and self-signed (IsCA).
	caCert, err := ca.CACertificate(ctx)
	if err != nil {
		t.Fatalf("CACertificate: %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA cert is not marked IsCA")
	}
	if len(ca.CACertPEM()) == 0 {
		t.Error("CACertPEM empty")
	}

	// Issue a client cert for an agent.
	issued, err := ca.Issue(ctx, pki.CertRequest{CommonName: "node-1", Role: pki.RoleAgent, DNSNames: []string{"gpu.local"}})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(issued.CertPEM) == 0 || len(issued.KeyPEM) == 0 {
		t.Fatal("issued cert/key PEM empty")
	}

	// The issued cert must verify against the CA as a client cert.
	leaf, err := ca.VerifyClient(ctx, issued.CertPEM)
	if err != nil {
		t.Fatalf("VerifyClient: %v", err)
	}
	if leaf.Subject.CommonName != "node-1" {
		t.Errorf("CN = %q, want node-1", leaf.Subject.CommonName)
	}
	hasClientAuth := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("leaf missing ClientAuth ext key usage")
	}

	// Metadata must be persisted in the certs table.
	rec, err := reg.GetCert(ctx, issued.Serial)
	if err != nil {
		t.Fatalf("GetCert: %v", err)
	}
	if rec.Subject != "node-1" || rec.Role != pki.RoleAgent || rec.State != pki.StateIssued {
		t.Errorf("unexpected persisted cert: %+v", rec)
	}
}

func TestCA_Revoke(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, _ := pki.New(ctx, reg, pki.Options{})

	issued, err := ca.Issue(ctx, pki.CertRequest{CommonName: "node-x", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := ca.VerifyClient(ctx, issued.CertPEM); err != nil {
		t.Fatalf("pre-revoke verify: %v", err)
	}

	if err := ca.Revoke(ctx, issued.Serial); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if revoked, _ := ca.IsRevoked(ctx, issued.Serial); !revoked {
		t.Error("IsRevoked = false after revoke")
	}
	// VerifyClient must now reject the revoked cert.
	if _, err := ca.VerifyClient(ctx, issued.CertPEM); err == nil {
		t.Error("VerifyClient accepted a revoked certificate")
	}
}

func TestCA_PersistAcrossRestart(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	dir := t.TempDir()

	ca1, err := pki.New(ctx, reg, pki.Options{Dir: dir})
	if err != nil {
		t.Fatalf("new CA1: %v", err)
	}
	c1, _ := ca1.CACertificate(ctx)
	issued, err := ca1.Issue(ctx, pki.CertRequest{CommonName: "node-1", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Re-open with the same dir: the CA must be identical.
	ca2, err := pki.New(ctx, reg, pki.Options{Dir: dir})
	if err != nil {
		t.Fatalf("new CA2: %v", err)
	}
	c2, _ := ca2.CACertificate(ctx)
	if c1.SerialNumber.Cmp(c2.SerialNumber) != 0 {
		t.Errorf("CA serial changed across restart: %s vs %s", c1.SerialNumber, c2.SerialNumber)
	}
	// A cert issued by the first CA still verifies under the reloaded CA.
	if _, err := ca2.VerifyClient(ctx, issued.CertPEM); err != nil {
		t.Errorf("cert issued by CA1 must verify under reloaded CA: %v", err)
	}
}

func TestCA_Rotate(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, _ := pki.New(ctx, reg, pki.Options{})
	before, _ := ca.CACertificate(ctx)

	after, err := ca.Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if before.SerialNumber.Cmp(after.SerialNumber) == 0 {
		t.Error("CA serial unchanged after rotation")
	}
	// The old CA cert should be marked rotated in the registry.
	if rec, err := reg.GetCert(ctx, before.SerialNumber.String()); err == nil {
		if rec.State != pki.StateRotated {
			t.Errorf("old CA state = %q, want rotated", rec.State)
		}
	}
	// New CA can still issue+verify.
	issued, err := ca.Issue(ctx, pki.CertRequest{CommonName: "post-rotate", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("issue after rotate: %v", err)
	}
	if _, err := ca.VerifyClient(ctx, issued.CertPEM); err != nil {
		t.Errorf("verify after rotate: %v", err)
	}
}

// TestCA_RootMaxPathLen verifies the root CA has MaxPathLen=1 (GAP-03).
func TestCA_RootMaxPathLen(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, err := pki.New(ctx, reg, pki.Options{})
	if err != nil {
		t.Fatalf("new CA: %v", err)
	}
	cert, _ := ca.CACertificate(ctx)
	if cert.MaxPathLen != 1 {
		t.Errorf("root CA MaxPathLen = %d, want 1", cert.MaxPathLen)
	}
	if cert.MaxPathLenZero {
		t.Error("root CA MaxPathLenZero must be false (MaxPathLen=1 set)")
	}
}

// TestCA_GenerateIntermediate verifies the intermediate CA hierarchy (GAP-03).
func TestCA_GenerateIntermediate(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	root, err := pki.New(ctx, reg, pki.Options{})
	if err != nil {
		t.Fatalf("new root CA: %v", err)
	}

	inter, err := root.GenerateIntermediate(ctx)
	if err != nil {
		t.Fatalf("GenerateIntermediate: %v", err)
	}

	// Intermediate must be a CA with MaxPathLen=0.
	interCert, _ := inter.CACertificate(ctx)
	if !interCert.IsCA {
		t.Error("intermediate cert must have IsCA=true")
	}
	if interCert.MaxPathLen != 0 || !interCert.MaxPathLenZero {
		t.Errorf("intermediate MaxPathLen=%d MaxPathLenZero=%v, want 0/true",
			interCert.MaxPathLen, interCert.MaxPathLenZero)
	}

	// Intermediate must be signed by the root.
	rootCert, _ := root.CACertificate(ctx)
	if err := interCert.CheckSignatureFrom(rootCert); err != nil {
		t.Errorf("intermediate not signed by root: %v", err)
	}

	// Leaf issued by intermediate must verify against intermediate's CertPool
	// (which includes the root for chain verification).
	issued, err := inter.Issue(ctx, pki.CertRequest{CommonName: "via-intermediate", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue via intermediate: %v", err)
	}
	if _, err := inter.VerifyClient(ctx, issued.CertPEM); err != nil {
		t.Errorf("VerifyClient via intermediate: %v", err)
	}
}

// TestCA_DualTrustBundle_GracePeriod verifies that a cert issued before
// rotation remains valid during the 72-hour grace period (GAP-05).
func TestCA_DualTrustBundle_GracePeriod(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, _ := pki.New(ctx, reg, pki.Options{})

	// Issue a cert BEFORE rotation.
	preRotate, err := ca.Issue(ctx, pki.CertRequest{CommonName: "pre-rotate", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue before rotate: %v", err)
	}

	if _, err := ca.Rotate(ctx); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Pre-rotation cert must still verify during the grace period.
	if _, err := ca.VerifyClient(ctx, preRotate.CertPEM); err != nil {
		t.Errorf("pre-rotation cert must verify during grace period: %v", err)
	}

	// Post-rotation cert must also verify.
	postRotate, err := ca.Issue(ctx, pki.CertRequest{CommonName: "post-rotate", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue after rotate: %v", err)
	}
	if _, err := ca.VerifyClient(ctx, postRotate.CertPEM); err != nil {
		t.Errorf("post-rotation cert must verify: %v", err)
	}
}

// TestCA_DualTrustBundle_AfterGrace verifies that a pre-rotation cert is
// rejected once the grace period has expired (GAP-05).
func TestCA_DualTrustBundle_AfterGrace(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, _ := pki.New(ctx, reg, pki.Options{})

	preRotate, err := ca.Issue(ctx, pki.CertRequest{CommonName: "pre-rotate", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := ca.Rotate(ctx); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Force the grace period to expire immediately.
	ca.ForceGraceExpiry()

	// Pre-rotation cert must now be rejected.
	if _, err := ca.VerifyClient(ctx, preRotate.CertPEM); err == nil {
		t.Error("pre-rotation cert must be rejected after grace period expires")
	}

	// Post-rotation cert must still verify.
	postRotate, err := ca.Issue(ctx, pki.CertRequest{CommonName: "post-rotate", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue after rotate: %v", err)
	}
	if _, err := ca.VerifyClient(ctx, postRotate.CertPEM); err != nil {
		t.Errorf("post-rotation cert must still verify after grace expiry: %v", err)
	}
}

// TestCA_CRLDistributionPoint verifies that issued leaf certs include the
// CRL distribution point URL.
func TestCA_CRLDistributionPoint(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, _ := pki.New(ctx, reg, pki.Options{})

	issued, err := ca.Issue(ctx, pki.CertRequest{CommonName: "crl-test", Role: pki.RoleAgent})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	blk, _ := pem.Decode(issued.CertPEM)
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	const wantCRL = "http://control-plane.purser.internal/pki/crl.pem"
	found := false
	for _, dp := range leaf.CRLDistributionPoints {
		if dp == wantCRL {
			found = true
		}
	}
	if !found {
		t.Errorf("CRL distribution point not found in leaf cert; got %v", leaf.CRLDistributionPoints)
	}
}

// TestCA_PassphraseProtectedKey verifies CA key round-trip with PURSER_PKI_KEY_PASSPHRASE.
func TestCA_PassphraseProtectedKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("PURSER_PKI_KEY_PASSPHRASE", "super-secret")

	reg1 := openReg(t)
	ca1, err := pki.New(ctx, reg1, pki.Options{Dir: dir})
	if err != nil {
		t.Fatalf("new CA with passphrase: %v", err)
	}
	serial1, _ := ca1.CACertificate(ctx)

	// Reload from disk — must succeed with the same passphrase.
	reg2 := openReg(t)
	ca2, err := pki.New(ctx, reg2, pki.Options{Dir: dir})
	if err != nil {
		t.Fatalf("reload CA with passphrase: %v", err)
	}
	serial2, _ := ca2.CACertificate(ctx)
	if serial1.SerialNumber.Cmp(serial2.SerialNumber) != 0 {
		t.Error("CA serial changed across passphrase-protected restart")
	}
}

// TestCA_PassphraseProtectedKey_WrongPassphrase verifies that loading a
// passphrase-protected key with the wrong passphrase fails.
func TestCA_PassphraseProtectedKey_WrongPassphrase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Setenv("PURSER_PKI_KEY_PASSPHRASE", "correct-pass")
	reg1 := openReg(t)
	if _, err := pki.New(ctx, reg1, pki.Options{Dir: dir}); err != nil {
		t.Fatalf("new CA: %v", err)
	}

	// Try to reload with wrong passphrase.
	t.Setenv("PURSER_PKI_KEY_PASSPHRASE", "wrong-pass")
	reg2 := openReg(t)
	_, err := pki.New(ctx, reg2, pki.Options{Dir: dir})
	if err == nil {
		t.Error("expected error when loading with wrong passphrase, got nil")
	}
}

// sanity: CACertPEM decodes to the same cert returned by CACertificate.
func TestCA_CACertPEMMatches(t *testing.T) {
	ctx := context.Background()
	reg := openReg(t)
	ca, _ := pki.New(ctx, reg, pki.Options{})
	cert, _ := ca.CACertificate(ctx)
	blk, _ := pem.Decode(ca.CACertPEM())
	if blk == nil {
		t.Fatal("CACertPEM did not decode")
	}
	parsed, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse CACertPEM: %v", err)
	}
	if parsed.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Error("CACertPEM serial mismatch")
	}
}
