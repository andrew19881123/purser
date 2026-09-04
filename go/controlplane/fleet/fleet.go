// Package fleet is the fleet-management surface of the control plane: the
// enterprise governance that turns a tool into a product.
//
// From here the operator enrolls new nodes (generating join tokens and
// approving join requests), drains a node for maintenance without service
// downtime, and decommissions a node (revoking its certificates). It also hosts
// the RegistrationService gRPC server (see registration.go). See
// docs/04_Control_Plane.html §7.
package fleet

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/registry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Node lifecycle state strings (mirror the NodeState proto enum).
const (
	NodeStateEnrolled       = "NODE_STATE_ENROLLED"
	NodeStateReady          = "NODE_STATE_READY"
	NodeStateDraining       = "NODE_STATE_DRAINING"
	NodeStateDecommissioned = "NODE_STATE_DECOMMISSIONED"
)

// Common errors.
var (
	ErrInvalidToken = errors.New("fleet: invalid or expired join token")
	ErrTokenUsed    = errors.New("fleet: join token already used")
)

// JoinToken is a short-lived, single-use credential handed to a machine so it
// can request enrollment into the cluster.
type JoinToken struct {
	Token     string
	ExpiresAt time.Time
	// Uses remaining (1 means single-use, not yet consumed).
	Uses int
}

// Manager governs the fleet lifecycle. It coordinates the registry (node state)
// and the internal CA (certificate issue/revoke).
type Manager struct {
	reg    registry.Registry
	ca     pki.CA
	secret []byte
	clock  func() time.Time

	mu   sync.Mutex
	used map[string]bool // consumed token nonces (in-memory, best-effort single-use)
}

// New builds a fleet Manager with a random signing secret for join tokens.
func New(reg registry.Registry, ca pki.CA) *Manager {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return NewWithSecret(reg, ca, secret)
}

// NewWithSecret builds a Manager with an explicit join-token signing secret
// (useful to keep tokens valid across restarts, or for tests).
func NewWithSecret(reg registry.Registry, ca pki.CA, secret []byte) *Manager {
	return &Manager{
		reg:    reg,
		ca:     ca,
		secret: secret,
		clock:  time.Now,
		used:   map[string]bool{},
	}
}

// SetClock overrides the time source (tests).
func (m *Manager) SetClock(now func() time.Time) {
	if now != nil {
		m.clock = now
	}
}

func (m *Manager) now() time.Time { return m.clock().UTC() }

// tokenPayload is the signed content of a join token.
type tokenPayload struct {
	Exp   int64  `json:"exp"`   // unix seconds
	Nonce string `json:"nonce"` // random, enforces single-use
}

// GenerateJoinToken mints a signed, expiring, single-use join token. The token
// is self-contained (HMAC-signed) so it can be verified without storage; the
// nonce is tracked in-memory to enforce single-use within a process lifetime.
func (m *Manager) GenerateJoinToken(ctx context.Context, ttl time.Duration) (*JoinToken, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("fleet: token nonce: %w", err)
	}
	exp := m.now().Add(ttl)
	payload := tokenPayload{Exp: exp.Unix(), Nonce: hex.EncodeToString(nonce)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	sig := m.sign(body)
	token := body + "." + sig

	m.audit(ctx, "fleet.join_token.generated", "", map[string]any{"expires_at": exp.Format(time.RFC3339)})
	return &JoinToken{Token: token, ExpiresAt: exp, Uses: 1}, nil
}

func (m *Manager) sign(body string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validateToken verifies the signature and expiry and enforces single-use. On
// success the token's nonce is marked consumed.
func (m *Manager) validateToken(token string) error {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ErrInvalidToken
	}
	want := m.sign(body)
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return ErrInvalidToken
	}
	var p tokenPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return ErrInvalidToken
	}
	if m.now().Unix() > p.Exp {
		return ErrInvalidToken
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used[p.Nonce] {
		return ErrTokenUsed
	}
	m.used[p.Nonce] = true
	return nil
}

// JoinResult is the outcome of a successful Join.
type JoinResult struct {
	NodeID     string
	ClientCert []byte
	CACert     []byte
}

// Join validates the token, registers (or updates) the node as ENROLLED, issues
// its mTLS client certificate via the CA, and returns the node ID plus the
// client and CA certificates.
//
// advAgentAddr and advInfAddr are the node's self-advertised AgentService and
// inference addresses ("host:port"), as sent in the JoinRequest. They are
// persisted on the node so the orchestrator's resolver can dial the agent and
// route inference traffic without relying on the hostname + fixed-port
// convention (which cannot distinguish multiple agents on one host). Either may
// be empty, in which case the resolver falls back to that convention.
func (m *Manager) Join(ctx context.Context, token string, hw *purserv1.HardwareProfile, advAgentAddr, advInfAddr string) (*JoinResult, error) {
	if err := m.validateToken(token); err != nil {
		return nil, err
	}
	nodeID := ""
	if hw != nil {
		nodeID = hw.GetNodeId()
	}
	if nodeID == "" {
		nodeID = genNodeID()
	}

	node := &registry.Node{
		ID:                      nodeID,
		State:                   NodeStateEnrolled,
		AdvertisedAgentAddr:     advAgentAddr,
		AdvertisedInferenceAddr: advInfAddr,
	}
	if hw != nil {
		node.Hostname = hw.GetHostname()
		node.OS = hw.GetOs().String()
		node.Arch = hw.GetArch().String()
		node.RAMGB = hw.GetRamTotalGb()
		var vram float64
		for _, g := range hw.GetGpus() {
			n := g.GetCount()
			if n == 0 {
				n = 1
			}
			vram += g.GetVramGb() * float64(n)
		}
		node.VRAMGB = vram
		if b, err := protojson.Marshal(hw); err == nil {
			node.HardwareProfile = b
		}
	}

	// Upsert the node.
	if _, err := m.reg.GetNode(ctx, nodeID); errors.Is(err, registry.ErrNotFound) {
		if err := m.reg.CreateNode(ctx, node); err != nil {
			return nil, fmt.Errorf("fleet: register node: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("fleet: lookup node: %w", err)
	} else {
		if err := m.reg.UpdateNode(ctx, node); err != nil {
			return nil, fmt.Errorf("fleet: update node: %w", err)
		}
	}

	// Issue the client certificate.
	var dns []string
	if node.Hostname != "" {
		dns = []string{node.Hostname}
	}
	issued, err := m.ca.Issue(ctx, pki.CertRequest{
		CommonName: nodeID,
		Role:       pki.RoleAgent,
		DNSNames:   dns,
	})
	if err != nil {
		return nil, fmt.Errorf("fleet: issue cert: %w", err)
	}
	caCert, err := m.ca.CACertificate(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: ca cert: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})

	m.audit(ctx, "fleet.node.enrolled", nodeID, map[string]any{"serial": issued.Serial})
	return &JoinResult{NodeID: nodeID, ClientCert: issued.CertPEM, CACert: caPEM}, nil
}

// Enroll is a convenience wrapper kept for API compatibility: it enrolls a node
// from a pre-built registry.Node profile.
func (m *Manager) Enroll(ctx context.Context, token string, profile *registry.Node) error {
	if profile == nil {
		return errors.New("fleet: nil profile")
	}
	hw := &purserv1.HardwareProfile{NodeId: profile.ID, Hostname: profile.Hostname, RamTotalGb: profile.RAMGB}
	_, err := m.Join(ctx, token, hw, profile.AdvertisedAgentAddr, profile.AdvertisedInferenceAddr)
	return err
}

// Approve promotes an ENROLLED node to READY (operator approval step).
func (m *Manager) Approve(ctx context.Context, nodeID string) error {
	n, err := m.reg.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("fleet: approve: %w", err)
	}
	n.State = NodeStateReady
	if err := m.reg.UpdateNode(ctx, n); err != nil {
		return err
	}
	m.audit(ctx, "fleet.node.approved", nodeID, nil)
	return nil
}

// Drain moves a node to DRAINING so it stops accepting new work while existing
// work migrates away.
func (m *Manager) Drain(ctx context.Context, nodeID string) error {
	n, err := m.reg.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("fleet: drain: %w", err)
	}
	n.State = NodeStateDraining
	if err := m.reg.UpdateNode(ctx, n); err != nil {
		return err
	}
	m.audit(ctx, "fleet.node.draining", nodeID, nil)
	return nil
}

// Decommission permanently removes a node from rotation and revokes its
// certificates.
func (m *Manager) Decommission(ctx context.Context, nodeID string) error {
	n, err := m.reg.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("fleet: decommission: %w", err)
	}
	n.State = NodeStateDecommissioned
	if err := m.reg.UpdateNode(ctx, n); err != nil {
		return err
	}
	// Revoke any certificates issued to this node (CommonName == nodeID).
	if certs, err := m.reg.ListCerts(ctx); err == nil {
		for _, c := range certs {
			if c.Subject == nodeID && c.State == pki.StateIssued {
				_ = m.ca.Revoke(ctx, c.Serial)
			}
		}
	}
	m.audit(ctx, "fleet.node.decommissioned", nodeID, nil)
	return nil
}

func (m *Manager) audit(ctx context.Context, action, target string, details map[string]any) {
	var raw json.RawMessage
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			raw = b
		}
	}
	_ = m.reg.AppendAudit(ctx, &registry.AuditEntry{Actor: "fleet", Action: action, Target: target, Details: raw})
}

func genNodeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "node-" + hex.EncodeToString(b[:])
}
