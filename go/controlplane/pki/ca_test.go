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
