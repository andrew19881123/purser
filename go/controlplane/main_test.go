package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
)

// quietLogger returns a logger that discards output so test runs stay clean.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedCA opens (creating if necessary) a registry at dbPath and materialises the
// on-disk CA under pkiDir, returning the current CA certificate serial.
func seedCA(t *testing.T, dbPath, pkiDir string) string {
	t.Helper()
	ctx := context.Background()
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ca, err := pki.New(ctx, reg, pki.Options{Dir: pkiDir})
	if err != nil {
		t.Fatalf("new CA: %v", err)
	}
	return ca.CACert().SerialNumber.String()
}

// caSerialOnDisk parses <pkiDir>/ca.crt and returns its serial number string.
func caSerialOnDisk(t *testing.T, pkiDir string) string {
	t.Helper()
	pemBytes, err := os.ReadFile(filepath.Join(pkiDir, "ca.crt"))
	if err != nil {
		t.Fatalf("read ca.crt: %v", err)
	}
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		t.Fatal("ca.crt did not decode")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse ca.crt: %v", err)
	}
	return cert.SerialNumber.String()
}

func TestRunPKICmd_RequiresSubcommand(t *testing.T) {
	if err := runPKICmd(quietLogger(), nil); err == nil {
		t.Error("runPKICmd with no subcommand must return an error")
	}
}

func TestRunPKICmd_UnknownSubcommand(t *testing.T) {
	if err := runPKICmd(quietLogger(), []string{"bogus"}); err == nil {
		t.Error("runPKICmd with an unknown subcommand must return an error")
	}
}

func TestRunPKIRotate_RequiresConfirm(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "reg.db")
	pkiDir := filepath.Join(dir, "pki")
	seedCA(t, dbPath, pkiDir)

	err := runPKICmd(quietLogger(), []string{"rotate", "--db", dbPath, "--pki-dir", pkiDir})
	if err == nil {
		t.Error("pki rotate without --confirm must return an error")
	}
	// The CA on disk must be unchanged when the confirmation gate rejects.
}

func TestRunPKIRotate_RotatesOnDisk(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "reg.db")
	pkiDir := filepath.Join(dir, "pki")
	before := seedCA(t, dbPath, pkiDir)
	if got := caSerialOnDisk(t, pkiDir); got != before {
		t.Fatalf("pre-rotate on-disk serial %s != seeded %s", got, before)
	}

	if err := runPKICmd(quietLogger(), []string{"rotate", "--db", dbPath, "--pki-dir", pkiDir, "--confirm"}); err != nil {
		t.Fatalf("pki rotate --confirm: %v", err)
	}

	after := caSerialOnDisk(t, pkiDir)
	if after == before {
		t.Errorf("CA serial on disk unchanged after rotation: %s", after)
	}

	// The previous CA must be marked rotated in the registry.
	ctx := context.Background()
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	defer reg.Close()
	if rec, err := reg.GetCert(ctx, before); err != nil {
		t.Fatalf("GetCert(old CA): %v", err)
	} else if rec.State != pki.StateRotated {
		t.Errorf("old CA state = %q, want %q", rec.State, pki.StateRotated)
	}
}

func TestRunPKIRevokeAll_RequiresConfirm(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "reg.db")
	pkiDir := filepath.Join(dir, "pki")
	seedCA(t, dbPath, pkiDir)

	if err := runPKICmd(quietLogger(), []string{"revoke-all", "--db", dbPath, "--pki-dir", pkiDir}); err == nil {
		t.Error("pki revoke-all without --confirm must return an error")
	}
}

func TestRunPKIRevokeAll_RevokesIssuedLeaves(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "reg.db")
	pkiDir := filepath.Join(dir, "pki")
	ctx := context.Background()

	// Seed a CA and issue two leaf certificates.
	reg, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := reg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ca, err := pki.New(ctx, reg, pki.Options{Dir: pkiDir})
	if err != nil {
		t.Fatalf("new CA: %v", err)
	}
	var serials []string
	for _, cn := range []string{"node-1", "node-2"} {
		c, err := ca.Issue(ctx, pki.CertRequest{CommonName: cn, Role: pki.RoleAgent})
		if err != nil {
			t.Fatalf("Issue %s: %v", cn, err)
		}
		serials = append(serials, c.Serial)
	}
	reg.Close()

	if err := runPKICmd(quietLogger(), []string{"revoke-all", "--db", dbPath, "--pki-dir", pkiDir, "--confirm"}); err != nil {
		t.Fatalf("pki revoke-all --confirm: %v", err)
	}

	// Reopen and confirm both leaves are revoked.
	reg2, err := registry.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	defer reg2.Close()
	for _, s := range serials {
		rec, err := reg2.GetCert(ctx, s)
		if err != nil {
			t.Fatalf("GetCert(%s): %v", s, err)
		}
		if rec.State != pki.StateRevoked {
			t.Errorf("cert %s state = %q, want %q", s, rec.State, pki.StateRevoked)
		}
	}
}
