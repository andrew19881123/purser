// Package pki is the internal Certificate Authority of the control plane.
//
// The control plane acts as the CA for the cluster's PKI: it issues and rotates
// the mTLS certificates used by Agents, and revokes them on decommission. See
// docs/04_Control_Plane.html §10.
//
// The CA interface is defined here; the concrete self-signed, on-disk
// implementation lives in pki/ca.go and is fully wired into the control plane.
package pki

import (
	"context"
	"crypto/x509"
	"net"
	"time"
)

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
type CA interface {
	// CACertificate returns the CA's own certificate (for trust distribution).
	CACertificate(ctx context.Context) (*x509.Certificate, error)
	// Issue mints a new leaf certificate for req.
	Issue(ctx context.Context, req CertRequest) (*IssuedCert, error)
	// Revoke marks the certificate with the given serial as revoked.
	Revoke(ctx context.Context, serial string) error
}
