// Package pki is the internal Certificate Authority of the control plane.
//
// The control plane acts as the CA for the cluster's PKI: it issues and rotates
// the mTLS certificates used by Agents, and revokes them on decommission. See
// docs/04_Control_Plane.html §10.
//
// This package is a phase-1 skeleton: it defines the CA interface; a concrete
// (self-signed, on-disk) CA implementation lands in phase 2.
package pki

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"time"
)

// ErrNotImplemented marks phase-2 functionality not yet built.
var ErrNotImplemented = errors.New("pki: not implemented (phase 2)")

// CertRequest describes a certificate to issue.
type CertRequest struct {
	// CommonName identifies the subject (typically the node ID).
	CommonName string
	// Role the certificate authorizes (e.g. "agent", "gateway").
	Role string
	// DNSNames / IPAddresses the certificate should be valid for.
	DNSNames    []string
	IPAddresses []net.IP
	// TTL is the requested validity duration.
	TTL time.Duration
}

// IssuedCert is a freshly minted certificate plus its PEM material.
type IssuedCert struct {
	Serial    string
	CertPEM   []byte
	KeyPEM    []byte
	NotBefore time.Time
	NotAfter  time.Time
}

// CA is the internal certificate authority abstraction.
//
// TODO(phase2): provide a concrete implementation that persists CA state and
// issued-certificate metadata via the registry (certs table) and supports
// rotation and CRL/OCSP-style revocation lookup.
type CA interface {
	// CACertificate returns the CA's own certificate (for trust distribution).
	CACertificate(ctx context.Context) (*x509.Certificate, error)
	// Issue mints a new leaf certificate for req.
	Issue(ctx context.Context, req CertRequest) (*IssuedCert, error)
	// Revoke marks the certificate with the given serial as revoked.
	Revoke(ctx context.Context, serial string) error
}
