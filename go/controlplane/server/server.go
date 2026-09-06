// Package server exposes the control-plane management API under /api/v1.
//
// These management endpoints are deliberately separate from the inference data
// path (docs/04_Control_Plane.html §10, "superficie minima"). The MVP uses the
// standard-library net/http router (Go 1.22+ method+path patterns) to keep the
// dependency footprint minimal — important for air-gapped builds.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/time/rate"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/audit"
	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/planning"
	"github.com/purser/purser/go/controlplane/policy"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/registry/importer"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	plannerplan "github.com/purser/purser/go/planner/plan"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protojson"
)

//go:embed openapi.json
var openAPISpec []byte

// Deployer is the orchestration surface the API needs. It is satisfied by
// *orchestrator.Orchestrator but declared here structurally to avoid a hard
// dependency (and to allow test doubles).
type Deployer interface {
	Apply(ctx context.Context, plan *purserv1.DeploymentPlan) (string, error)
	Teardown(ctx context.Context, deploymentID string) error
}

// MetricsSource provides a snapshot of live metrics for the SSE endpoint. It is
// satisfied by fleet.LiveMetrics.
type MetricsSource interface {
	Snapshot(ctx context.Context) (any, error)
}

// NodeMetricsGetter fetches the most recent live hardware-metrics sample for a
// single node. It is satisfied by *fleet.LiveMetrics (via its Get method) and
// by test doubles. When wired, handleMetricsSSE enumerates every registry node
// and zero-fills entries for nodes that have not yet sent a heartbeat.
type NodeMetricsGetter interface {
	Get(nodeID string) (fleet.NodeMetrics, bool)
}

// FleetManager is the fleet-lifecycle surface the API needs: minting cluster
// join tokens (node enrollment) and transitioning a node's lifecycle state
// (drain, decommission). It is satisfied by *fleet.Manager but declared
// structurally so tests can supply a double and so the server does not depend
// on the whole fleet surface.
type FleetManager interface {
	GenerateJoinToken(ctx context.Context, ttl time.Duration) (*fleet.JoinToken, error)
	// Drain cordons a node (marks it DRAINING) so no NEW work schedules onto it,
	// auditing fleet.node.draining. It does not migrate or rebalance existing
	// work.
	Drain(ctx context.Context, nodeID string) error
	// Decommission transitions a node to DECOMMISSIONED and revokes its
	// certificates, auditing fleet.node.decommissioned. It is a lifecycle
	// transition, not a hard row deletion.
	Decommission(ctx context.Context, nodeID string) error
}

// ReconcilerStatusProvider is the surface the GET /api/v1/reconciler/status
// endpoint needs. It is satisfied by *reconciler.Reconciler but declared as
// an interface so test doubles can stub it without starting a live control loop.
type ReconcilerStatusProvider interface {
	Status() reconciler.ReconcilerStatus
}

// contextKey is a private key type for values stored in request contexts.
// Using a package-local type avoids collisions with keys from other packages.
type contextKey int

const (
	// ctxKeyOIDCSub is the context key for the OIDC subject claim.
	ctxKeyOIDCSub contextKey = iota
	// ctxKeyOIDCEmail is the context key for the OIDC email claim.
	ctxKeyOIDCEmail
	// ctxKeyOIDCRole is the context key for the role resolved from OIDC group/role
	// claim mappings. Set by oidcMiddleware when GroupMappings is configured and a
	// matching group or role claim is found in the token; used by rbacMiddleware to
	// enforce the mapped role without requiring an API key.
	ctxKeyOIDCRole
	// ctxKeyOIDCTenant is the context key for the tenant extracted from the OIDC
	// "tid" (EntraID) or "tenant_id" claim. Used by list handlers to scope results
	// for viewer-role tokens.
	ctxKeyOIDCTenant
)

// OIDCConfig configures the optional OIDC authentication layer for the admin
// UI and management REST API (/api/v1). When non-nil, every request must carry
// a valid Bearer token issued by the configured provider. Machine-to-machine
// requests from the gateway (X-Purser-Internal-Token) are exempted. If nil,
// OIDC is disabled and all requests pass through — the community default.
type OIDCConfig struct {
	// Issuer is the OIDC provider base URL, e.g.
	// https://login.microsoftonline.com/<tenant>/v2.0 for EntraID.
	Issuer string
	// ClientID is the expected audience claim in tokens issued by the provider.
	ClientID string
	// ClientSecret is the OAuth2 client secret for confidential clients.
	// Optional — leave empty for public clients or pure PKCE flows.
	ClientSecret string
	// RedirectURI is the full callback URL for the Authorization Code Flow,
	// e.g. http://localhost:8080/auth/callback. When set, GET /auth/login and
	// GET /auth/callback are activated as browser SSO endpoints.
	RedirectURI string
	// TokenEndpoint is the IdP's token exchange URL (populated from OIDC
	// discovery in main.go). Required for the callback code exchange.
	TokenEndpoint string
	// GroupMappings maps OIDC group or role claim values to Purser roles
	// ("admin", "viewer", or "inference"). Example:
	//   {"purser-admins":"admin","purser-viewers":"viewer"}
	// When the token's "groups" or "roles" claim contains a key in this map, the
	// highest-privilege mapped role is injected into the request context so
	// rbacMiddleware can enforce it without an API key. If not set here, it is
	// read from PURSER_OIDC_GROUP_MAPPINGS at startup.
	GroupMappings map[string]string
}

// TokenClaims is the full set of claims extracted from a verified OIDC token.
// It is a superset of what VerifyToken returns and adds group, role, and tenant
// claims for RBAC mapping and tenant-scoped list filtering.
type TokenClaims struct {
	Sub    string   // "sub" subject claim
	Email  string   // "email" claim
	Groups []string // "groups" claim (array) — EntraID groups, Keycloak groups
	Roles  []string // "roles" claim (array) — EntraID app roles
	Tenant string   // "tid" (EntraID) or "tenant_id" claim
}

// GroupClaimsVerifier is an optional extension to TokenVerifier that also
// extracts group, role, and tenant claims. When a verifier implements this
// interface, oidcMiddleware calls VerifyClaims instead of VerifyToken so that
// OIDC group-based RBAC and tenant scoping are available. Implementations that
// only need sub+email may omit this interface — the middleware falls back to
// VerifyToken gracefully.
type GroupClaimsVerifier interface {
	VerifyClaims(ctx context.Context, rawToken string) (*TokenClaims, error)
}

// TokenVerifier is the single interface oidcMiddleware uses to verify raw ID
// tokens. The production path wraps *oidc.IDTokenVerifier via
// OIDCVerifierAdapter; test stubs can implement this interface without making
// real HTTP calls to an IdP.
type TokenVerifier interface {
	VerifyToken(ctx context.Context, rawToken string) (sub, email string, err error)
}

// OIDCVerifierAdapter adapts *oidc.IDTokenVerifier (from coreos/go-oidc) to
// the TokenVerifier interface. Build one with NewOIDCVerifierAdapter and pass
// it via Config.OIDCVerifier so New() needs no OIDC discovery calls.
type OIDCVerifierAdapter struct {
	v *oidc.IDTokenVerifier
}

// NewOIDCVerifierAdapter wraps v in the TokenVerifier interface.
func NewOIDCVerifierAdapter(v *oidc.IDTokenVerifier) TokenVerifier {
	return &OIDCVerifierAdapter{v: v}
}

// VerifyToken verifies rawToken and extracts the sub and email claims.
func (a *OIDCVerifierAdapter) VerifyToken(ctx context.Context, rawToken string) (string, string, error) {
	tok, err := a.v.Verify(ctx, rawToken)
	if err != nil {
		return "", "", err
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := tok.Claims(&claims); err != nil {
		return "", "", err
	}
	return claims.Sub, claims.Email, nil
}

// VerifyClaims verifies rawToken and extracts the full set of claims needed for
// group-based RBAC and tenant scoping: sub, email, groups (array), roles
// (array), and tenant ("tid" for EntraID or "tenant_id").
// This implements GroupClaimsVerifier so oidcMiddleware can extract group/role
// mappings and the tenant in a single token verification round-trip.
func (a *OIDCVerifierAdapter) VerifyClaims(ctx context.Context, rawToken string) (*TokenClaims, error) {
	tok, err := a.v.Verify(ctx, rawToken)
	if err != nil {
		return nil, err
	}
	var c struct {
		Sub      string   `json:"sub"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
		Roles    []string `json:"roles"`
		TID      string   `json:"tid"`
		TenantID string   `json:"tenant_id"`
	}
	if err := tok.Claims(&c); err != nil {
		return nil, err
	}
	tenant := c.TID
	if tenant == "" {
		tenant = c.TenantID
	}
	return &TokenClaims{
		Sub:    c.Sub,
		Email:  c.Email,
		Groups: c.Groups,
		Roles:  c.Roles,
		Tenant: tenant,
	}, nil
}

// Config configures the HTTP server.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Logger is used for request/error logging; a default is used if nil.
	Logger *slog.Logger
	// RaftNode, if set, enables the Raft-cluster status endpoint
	// (GET /api/v1/cluster/status) and causes the handler to report Raft
	// consensus state. When nil the endpoint returns a standalone-mode
	// response (is_leader: true) so load-balancers work in both modes.
	RaftNode RaftNode
	// Deployer, if set, backs the deploy/teardown endpoints.
	Deployer Deployer
	// Metrics, if set, backs the live SSE metrics endpoint; otherwise a
	// registry-derived summary is emitted. Superseded by NodeMetrics when both
	// are set.
	Metrics MetricsSource
	// NodeMetrics, if set, backs the live SSE metrics endpoint with per-node
	// hardware data from agent heartbeats. The server enumerates every registry
	// node and zero-fills metrics for nodes that have not yet reported.
	// Takes priority over Metrics when both are configured.
	NodeMetrics NodeMetricsGetter
	// MetricsInterval is the SSE emit cadence (default 2s).
	MetricsInterval time.Duration
	// Planner, if set, produces DeploymentPlans from the current fleet (backs
	// the "deploy with no supplied plan" path and the /models fit verdicts).
	Planner *planning.Planner
	// Fleet, if set, backs the join-token and node-lifecycle endpoints
	// (enrollment, drain, decommission).
	Fleet FleetManager
	// ClusterID is echoed in join-token responses so an enrolling agent knows
	// which cluster it is joining. Defaults to "default".
	ClusterID string
	// PublicAddr is the control-plane address that enrolling nodes should dial
	// (e.g. "http://10.0.0.1:8080"). It is emitted verbatim in the enrollment
	// bundle so an operator can override it for external nodes. If unset, Addr
	// is used as a best-effort fallback (which may be a bind address like
	// ":8080" that is not reachable from external machines).
	PublicAddr string
	// License is the verified license resolved at startup (see
	// license.FromEnv). It gates the enterprise endpoints. If nil, the server
	// falls back to the community license (enterprise features off).
	License *license.License
	// OIDC configures the optional OIDC authentication layer for the admin UI
	// and management REST API (/api/v1). When non-nil, every request must carry
	// a valid Bearer token issued by the configured provider. Machine-to-machine
	// requests from the gateway (X-Purser-Internal-Token) are exempted.
	// If nil, OIDC is disabled (community default).
	OIDC *OIDCConfig
	// OIDCVerifier, when non-nil, overrides the token verifier that would
	// normally be created via OIDC discovery. Use this in tests to inject a stub
	// that exercises the middleware path without calling a live IdP. When set,
	// OIDC authentication is active even if OIDC is nil.
	OIDCVerifier TokenVerifier
	// InternalToken is the shared secret compared against the
	// X-Purser-Internal-Token request header. Requests carrying this value
	// bypass OIDC verification (and RBAC) so the gateway can perform route-sync
	// without a human token.
	InternalToken string
	// HFToken is the HuggingFace API token used by POST /api/v1/models/import
	// when the caller does not supply an X-HF-Token header. Read from
	// PURSER_HF_TOKEN at startup. Leave empty for public-model-only access.
	HFToken string
	// HFBaseURL overrides the HuggingFace API base URL used by the import
	// handler. Leave empty to use the default (https://huggingface.co). Useful
	// in tests that point the server at an httptest mock.
	HFBaseURL string
	// VertexAI, if set, is used for VertexAI model import requests instead of
	// constructing a client from environment variables at request time.
	// Primarily useful for testing with a pre-configured mock client.
	VertexAI *importer.VertexAIClient

	// TLSCert is the path to a PEM-encoded TLS certificate file. When both
	// TLSCert and TLSKey are non-empty, ListenAndServe serves HTTPS.
	TLSCert string
	// TLSKey is the path to a PEM-encoded TLS private key file. Required
	// when TLSCert is set.
	TLSKey string
	// TLSCertPEM / TLSKeyPEM hold the raw PEM bytes for the server certificate
	// and key.  When non-nil they take precedence over TLSCert/TLSKey file
	// paths.  The auto-TLS path in main.go issues a cert from the internal PKI
	// CA and passes the PEM bytes here instead of writing temporary disk files.
	TLSCertPEM []byte
	TLSKeyPEM  []byte

	// RateLimitRPS is the per-source-IP rate limit in requests per second.
	// 0 (the zero-value) maps to the default of 100 RPS.
	// Set to -1 to disable per-IP rate limiting entirely.
	RateLimitRPS float64
	// RateLimitKeyRPS is the per-API-key rate limit in requests per second.
	// 0 (the zero-value) maps to the default of 50 RPS.
	// Set to -1 to disable per-key rate limiting entirely.
	RateLimitKeyRPS float64

	// Reconciler, if set, backs the GET /api/v1/reconciler/status endpoint.
	// When nil the endpoint returns 501 Not Implemented.
	Reconciler ReconcilerStatusProvider

	// SessionSecret is the 32-byte HMAC-SHA256 key used to sign session cookies
	// issued by the Authorization Code Flow callback. When nil and OIDC is
	// configured, an ephemeral random key is auto-generated at startup (sessions
	// expire when the process restarts). Set PURSER_SESSION_SECRET to a fixed
	// 32-byte hex key for persistence across restarts.
	SessionSecret []byte
}

// rateLimiterEntry tracks per-key sliding-window rate-limit state.
// The struct is stored in the ipLimiters / keyLimiters maps; unused entries
// are evicted by cleanupLimiters after ipLimiterIdleTimeout.
type rateLimiterEntry struct {
	count  int
	window time.Time
}

// Server holds the API dependencies and the composed HTTP handler.
type Server struct {
	reg               registry.Registry
	log               *slog.Logger
	mux               *http.ServeMux
	server            *http.Server
	deployer          Deployer
	metrics           MetricsSource
	nodeMetrics       NodeMetricsGetter
	metricTO          time.Duration
	planner           *planning.Planner
	fleet             FleetManager
	clusterID         string
	publicAddr        string
	license           *license.License
	oidcVerifier      TokenVerifier // nil = OIDC disabled
	oidcConfig        *OIDCConfig   // nil when OIDC not configured
	sessionSecret     []byte        // HMAC-SHA256 key for session cookie signing
	pkceStore         *pkceStateStore
	oidcGroupMappings map[string]string // group/role claim → Purser role; nil = no mapping
	internalToken     string            // gateway exemption secret
	hfToken           string
	hfBaseURL         string
	vertexai          *importer.VertexAIClient
	reconcilerStatus  ReconcilerStatusProvider // nil = endpoint disabled
	raftNode          RaftNode                 // nil = standalone mode

	// TLS: file paths (explicit mode) or pre-configured TLS config (auto mode).
	tlsCert    string
	tlsKey     string
	tlsEnabled bool // true when TLS is active via either mode

	// Rate limiting state.
	rateLimitRPS    float64 // per-IP; 0 = disabled
	rateLimitKeyRPS float64 // per-key; 0 = disabled

	// Per-IP and per-API-key rate limiter maps. Each entry is keyed by the
	// client IP or API key ID; the *Access maps record the last-used timestamp
	// so cleanupLimiters can evict stale entries and prevent unbounded growth.
	ipLimitersMu      sync.Mutex
	ipLimiters        map[string]*rate.Limiter
	ipLimitersAccess  map[string]time.Time
	keyLimitersMu     sync.Mutex
	keyLimiters       map[string]*rate.Limiter
	keyLimitersAccess map[string]time.Time

	// policyMu guards policyEngine; use RLock for reads and Lock for swaps.
	policyMu     sync.RWMutex
	policyEngine *policy.Engine // nil when feature is off or no policies are loaded

	// handler is the mux wrapped with CORS (outermost), OTEL, OIDC, rate-limit,
	// and RBAC (inner) middleware. Returned by Handler() and used as
	// http.Server.Handler so all test paths go through the same middleware chain.
	handler http.Handler

	// hasAnyAPIKeyCache caches whether at least one enabled API key exists in
	// the registry. It is loaded lazily on the first unauthenticated request to
	// an /api/v1/* endpoint and never invalidated (API key creation only ever
	// transitions false→true, never the reverse within a process lifetime).
	hasAnyAPIKeyCache atomic.Bool

	// OTEL infrastructure gauge instruments. All three are no-ops unless a real
	// MeterProvider was installed by telemetry.Init before New() is called.
	gaugeDeploymentsActive metric.Int64Gauge
	gaugeNodesReady        metric.Int64Gauge
	gaugeNodesTotal        metric.Int64Gauge

	// OTEL per-node hardware gauge instruments (Float64 for utilisation %/tok/s,
	// Int64 for the binary inference-port-alive indicator). No-ops unless a real
	// MeterProvider was installed by telemetry.Init before New() is called.
	gaugeNodeCPU            metric.Float64Gauge
	gaugeNodeGPU            metric.Float64Gauge
	gaugeNodeMemBandwidth   metric.Float64Gauge
	gaugeNodeTokPerSec      metric.Float64Gauge
	gaugeNodeInferenceAlive metric.Int64Gauge
}

// New builds a Server backed by reg.
func New(reg registry.Registry, cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := cfg.MetricsInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	clusterID := cfg.ClusterID
	if clusterID == "" {
		clusterID = "default"
	}
	publicAddr := cfg.PublicAddr
	if publicAddr == "" {
		publicAddr = cfg.Addr
	}
	lic := cfg.License
	if lic == nil {
		lic = license.Community()
	}
	// Resolve rate limit RPS values: 0 (zero-value) → defaults.
	rlRPS := cfg.RateLimitRPS
	if rlRPS == 0 {
		rlRPS = 100
	}
	rlKeyRPS := cfg.RateLimitKeyRPS
	if rlKeyRPS == 0 {
		rlKeyRPS = 50
	}

	// Resolve OIDC group mappings: Config.OIDC.GroupMappings takes priority; fall
	// back to the PURSER_OIDC_GROUP_MAPPINGS env var (JSON) when not set.
	var oidcGroupMappings map[string]string
	if cfg.OIDC != nil && len(cfg.OIDC.GroupMappings) > 0 {
		oidcGroupMappings = cfg.OIDC.GroupMappings
	} else if raw := os.Getenv("PURSER_OIDC_GROUP_MAPPINGS"); raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			oidcGroupMappings = m
		} else {
			logger.Warn("PURSER_OIDC_GROUP_MAPPINGS: invalid JSON, group mapping disabled", "err", err)
		}
	}

	s := &Server{
		reg:               reg,
		log:               logger,
		mux:               http.NewServeMux(),
		deployer:          cfg.Deployer,
		metrics:           cfg.Metrics,
		nodeMetrics:       cfg.NodeMetrics,
		metricTO:          interval,
		planner:           cfg.Planner,
		fleet:             cfg.Fleet,
		clusterID:         clusterID,
		publicAddr:        publicAddr,
		license:           lic,
		oidcGroupMappings: oidcGroupMappings,
		internalToken:     cfg.InternalToken,
		hfToken:           cfg.HFToken,
		hfBaseURL:         cfg.HFBaseURL,
		vertexai:          cfg.VertexAI,
		reconcilerStatus:  cfg.Reconciler,
		raftNode:          cfg.RaftNode,
		tlsCert:           cfg.TLSCert,
		tlsKey:            cfg.TLSKey,
		rateLimitRPS:      rlRPS,
		rateLimitKeyRPS:   rlKeyRPS,
		ipLimiters:        make(map[string]*rate.Limiter),
		ipLimitersAccess:  make(map[string]time.Time),
		keyLimiters:       make(map[string]*rate.Limiter),
		keyLimitersAccess: make(map[string]time.Time),
	}

	// OIDC verifier: prefer an injected verifier (for tests or pre-built
	// production callers) over on-the-fly discovery.
	if cfg.OIDCVerifier != nil {
		s.oidcVerifier = cfg.OIDCVerifier
	} else if cfg.OIDC != nil {
		// Eager discovery: if the provider is unreachable at startup, the
		// misconfiguration is caught here — not at the first admin request.
		// In production, main.go pre-creates the provider and passes it via
		// OIDCVerifier, so this branch is a defensive fallback.
		provider, err := oidc.NewProvider(context.Background(), cfg.OIDC.Issuer)
		if err != nil {
			panic("purser: OIDC discovery failed for issuer " + cfg.OIDC.Issuer + ": " + err.Error())
		}
		s.oidcVerifier = NewOIDCVerifierAdapter(
			provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
		)
		logger.Info("OIDC authentication enabled", "issuer", cfg.OIDC.Issuer, "client_id", cfg.OIDC.ClientID)
	}

	// Store the OIDC config for the Authorization Code Flow handlers.
	if cfg.OIDC != nil {
		s.oidcConfig = cfg.OIDC
	}

	// Session secret for session cookie signing. Used by both signSession and
	// verifySession. When not provided and OIDC is configured, auto-generate an
	// ephemeral key (sessions expire on process restart).
	if len(cfg.SessionSecret) > 0 {
		s.sessionSecret = cfg.SessionSecret
	} else if s.oidcVerifier != nil {
		s.sessionSecret = make([]byte, 32)
		if _, err := rand.Read(s.sessionSecret); err != nil {
			panic("purser: generate ephemeral session secret: " + err.Error())
		}
		logger.Warn("PURSER_SESSION_SECRET not set; using ephemeral key (sessions expire on restart)")
	}

	// PKCE state store: always initialised so the auth endpoints are ready.
	s.pkceStore = newPKCEStateStore()

	s.routes()

	// Eagerly load stored policies (if any) into the OPA engine so the first
	// deploy request after startup is evaluated against the correct policy set.
	if reg != nil {
		s.reloadPolicies(context.Background())
	}

	// Initialise OTEL metric instruments. otel.Meter() returns a no-op meter
	// (zero overhead) if no real MeterProvider was installed by telemetry.Init,
	// so this is always safe to call even without a collector.
	m := otel.Meter("purser.control-plane")
	s.gaugeDeploymentsActive, _ = m.Int64Gauge("purser.deployments.active",
		metric.WithDescription("Number of deployments in ACTIVE state"),
		metric.WithUnit("{deployment}"))
	s.gaugeNodesReady, _ = m.Int64Gauge("purser.nodes.ready",
		metric.WithDescription("Number of nodes in READY or RUNNING state"),
		metric.WithUnit("{node}"))
	s.gaugeNodesTotal, _ = m.Int64Gauge("purser.nodes.total",
		metric.WithDescription("Total number of registered nodes"),
		metric.WithUnit("{node}"))

	// Per-node hardware metrics (labelled by node_id). Values are populated
	// from the LiveMetrics heartbeat cache on every collectInfraMetrics tick.
	s.gaugeNodeCPU, _ = m.Float64Gauge("purser.node.cpu_utilization",
		metric.WithDescription("CPU utilisation percentage reported by the node agent (0–100)"),
		metric.WithUnit("%"))
	s.gaugeNodeGPU, _ = m.Float64Gauge("purser.node.gpu_utilization",
		metric.WithDescription("GPU utilisation percentage reported by the node agent (0–100, 0 when no GPU)"),
		metric.WithUnit("%"))
	s.gaugeNodeMemBandwidth, _ = m.Float64Gauge("purser.node.mem_bandwidth_utilization",
		metric.WithDescription("Memory-bandwidth utilisation percentage reported by the node agent (0–100)"),
		metric.WithUnit("%"))
	s.gaugeNodeTokPerSec, _ = m.Float64Gauge("purser.node.tokens_per_second",
		metric.WithDescription("Tokens per second currently being processed by the node (0 if not serving)"),
		metric.WithUnit("{token}/s"))
	s.gaugeNodeInferenceAlive, _ = m.Int64Gauge("purser.node.inference_port_alive",
		metric.WithDescription("1 if the node's inference HTTP port is responding, 0 otherwise"),
		metric.WithUnit("{bool}"))

	// Wrap the mux: CORS (outermost, handles preflight OPTIONS before any other
	// processing) → OTEL (distributed tracing) → OIDC (human-user auth) →
	// rate-limit → RBAC (API key role enforcement) → mux. CORS and OTEL are
	// transparent no-ops when not configured; OIDC is a no-op when unconfigured.
	s.handler = s.corsMiddleware(otelMiddleware(s.oidcMiddleware(s.rateLimitMiddleware(s.rbacMiddleware(s.mux)))))

	// Build the underlying http.Server. For in-memory TLS (auto mode) the PEM
	// bytes are pre-parsed into a tls.Certificate and attached via TLSConfig so
	// ListenAndServeTLS("", "") can use them without writing to disk.
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,  // total time to read request including body
		WriteTimeout:      180 * time.Second, // allows SSE streams to run for minutes
		IdleTimeout:       120 * time.Second, // keep-alive connections
		MaxHeaderBytes:    1 << 20,           // 1 MB header limit
	}
	if len(cfg.TLSCertPEM) > 0 && len(cfg.TLSKeyPEM) > 0 {
		cert, err := tls.X509KeyPair(cfg.TLSCertPEM, cfg.TLSKeyPEM)
		if err != nil {
			// Misconfiguration is fatal at construction time, not at serve time.
			panic("purser: TLS auto-cert is invalid: " + err.Error())
		}
		httpSrv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		s.tlsEnabled = true
	} else if cfg.TLSCert != "" && cfg.TLSKey != "" {
		s.tlsEnabled = true
	}
	s.server = httpSrv
	return s
}

// Handler returns the composed http.Handler (useful for tests via httptest).
// It is the mux wrapped with OTEL (outer), OIDC (middle), and RBAC (inner)
// middleware. All three are transparent no-ops when not configured.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe starts background maintenance goroutines and then serves
// HTTP until the server stops. When TLS is configured (via TLSCert/TLSKey
// file paths or pre-loaded PEM in TLSCertPEM/TLSKeyPEM) it calls the
// underlying ListenAndServeTLS so the management API is served over HTTPS.
func (s *Server) ListenAndServe() error {
	go s.cleanupLimiters()
	go s.startKeyExpiryWatcher(context.Background())
	// Hourly background cleanup of expired OIDC sessions and PKCE state rows.
	// This prevents unbounded growth of the oidc_sessions and pkce_state tables
	// on long-running instances. The goroutine runs for the lifetime of the
	// process; no cancellation is wired up because process exit is sufficient.
	if s.reg != nil {
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				if n, err := s.reg.DeleteExpiredOIDCSessions(context.Background()); err == nil && n > 0 {
					s.log.Debug("cleaned expired OIDC sessions", "count", n)
				}
				if _, err := s.reg.DeleteExpiredPKCEStates(context.Background()); err != nil {
					s.log.Debug("DeleteExpiredPKCEStates error", "err", err)
				}
			}
		}()
	}
	if s.tlsEnabled {
		s.log.Info("management API serving HTTPS", "addr", s.server.Addr)
		// For file-path mode pass the paths; for in-memory mode the TLSConfig
		// already has the certificate so "" is correct for both arguments.
		return s.server.ListenAndServeTLS(s.tlsCert, s.tlsKey)
	}
	s.log.Info("management API serving HTTP (no TLS)", "addr", s.server.Addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }

// validateInternalToken returns true when provided matches s.internalToken
// using a constant-time comparison (prevents timing-based token enumeration).
// Returns false whenever s.internalToken is empty so that unconfigured
// deployments do not accidentally grant access.
func (s *Server) validateInternalToken(provided string) bool {
	if s.internalToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.internalToken)) == 1
}

// startKeyExpiryWatcher emits audit events for API keys that will expire within
// the next 14 days. It ticks every 6 hours and runs until ctx is cancelled.
// Each affected key produces one "apikey.expiry_warning" audit entry carrying
// the key's name, RFC3339 expiry timestamp, and approximate days remaining.
func (s *Server) startKeyExpiryWatcher(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			horizon := time.Now().Add(14 * 24 * time.Hour)
			keys, err := s.reg.ListAPIKeysExpiringBefore(ctx, horizon)
			if err != nil {
				s.log.Warn("key expiry watcher: list failed", "err", err)
				continue
			}
			for _, k := range keys {
				if k.ExpiresAt == nil {
					continue
				}
				days := int(time.Until(*k.ExpiresAt).Hours() / 24)
				_ = s.reg.AppendAudit(ctx, &registry.AuditEntry{
					Actor:  "system",
					Action: "apikey.expiry_warning",
					Target: k.ID,
					Details: json.RawMessage(fmt.Sprintf(
						`{"name":%q,"expires_at":%q,"days_remaining":%d}`,
						k.Name, k.ExpiresAt.Format(time.RFC3339), days,
					)),
				})
			}
		}
	}
}

// cleanupLimiters sweeps the IP and API-key rate-limiter maps every 5 minutes,
// removing entries that have not been accessed for more than 10 minutes. This
// prevents the maps from growing without bound under a long-lived server.
func (s *Server) cleanupLimiters() {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic in cleanupLimiters goroutine", "recovered", r)
		}
	}()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		s.ipLimitersMu.Lock()
		for k, t := range s.ipLimitersAccess {
			if t.Before(cutoff) {
				delete(s.ipLimiters, k)
				delete(s.ipLimitersAccess, k)
			}
		}
		s.ipLimitersMu.Unlock()
		s.keyLimitersMu.Lock()
		for k, t := range s.keyLimitersAccess {
			if t.Before(cutoff) {
				delete(s.keyLimiters, k)
				delete(s.keyLimitersAccess, k)
			}
		}
		s.keyLimitersMu.Unlock()
	}
}

// oidcMiddleware returns an http.Handler that enforces OIDC authentication
// before delegating to next. It is a pass-through when s.oidcVerifier is nil
// (OIDC not configured). When active:
//
//  1. /auth/login and /auth/callback are always exempted — they ARE the login
//     flow and are unauthenticated by definition.
//  2. Requests carrying the correct X-Purser-Internal-Token header are
//     exempted so the gateway can perform route-sync without a human token.
//  3. A valid "Authorization: Bearer <ID-token>" is accepted (existing path).
//  4. A valid "Cookie: purser_session=<signed-token>" is accepted as an
//     alternative to Bearer (browser SSO path).
//  5. Browser requests (Accept: text/html) with neither credential are
//     redirected to /auth/login when the Authorization Code Flow is configured.
//  6. API requests with neither credential receive 401 JSON.
func (s *Server) oidcMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. OIDC disabled — pass through unconditionally.
		if s.oidcVerifier == nil {
			next.ServeHTTP(w, r)
			return
		}
		// 2. Auth endpoints are exempt: they ARE the login/logout flow.
		// /auth/logout and /auth/backchannel-logout are reachable even with an
		// already-revoked session so the browser can always clear its cookie.
		switch r.URL.Path {
		case "/auth/login", "/auth/callback", "/auth/logout", "/auth/backchannel-logout":
			next.ServeHTTP(w, r)
			return
		}
		// 3. Gateway internal-token exemption: the gateway sends route-sync
		// requests with X-Purser-Internal-Token; those must not require a
		// human OIDC token. Use constant-time comparison to prevent timing attacks.
		if s.validateInternalToken(r.Header.Get("X-Purser-Internal-Token")) {
			next.ServeHTTP(w, r)
			return
		}
		// 4. Try Bearer token (ID token from the IdP, existing flow).
		if rawToken, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.TrimSpace(rawToken) != "" {
			// When the verifier also implements GroupClaimsVerifier use VerifyClaims
			// (single round-trip) for the full claim set; fall back to VerifyToken for
			// backward compatibility with stubs that only implement the basic interface.
			var sub, email, oidcRole, oidcTenant string
			if gcv, ok := s.oidcVerifier.(GroupClaimsVerifier); ok {
				claims, err := gcv.VerifyClaims(r.Context(), rawToken)
				if err != nil {
					s.log.Debug("OIDC token verification failed", "err", err)
					s.writeJSON(w, http.StatusUnauthorized, map[string]any{
						"error":   "unauthorized",
						"message": "valid OIDC token required",
					})
					return
				}
				sub = claims.Sub
				email = claims.Email
				oidcTenant = claims.Tenant
				// Map groups + roles claims to the highest-privilege Purser role.
				if len(s.oidcGroupMappings) > 0 {
					oidcRole = s.resolveGroupRole(append(claims.Groups, claims.Roles...))
				}
			} else {
				var err error
				sub, email, err = s.oidcVerifier.VerifyToken(r.Context(), rawToken)
				if err != nil {
					s.log.Debug("OIDC token verification failed", "err", err)
					s.writeJSON(w, http.StatusUnauthorized, map[string]any{
						"error":   "unauthorized",
						"message": "valid OIDC token required",
					})
					return
				}
			}
			// Inject verified claims into the request context for downstream handlers.
			ctx := context.WithValue(r.Context(), ctxKeyOIDCSub, sub)
			ctx = context.WithValue(ctx, ctxKeyOIDCEmail, email)
			if oidcRole != "" {
				ctx = context.WithValue(ctx, ctxKeyOIDCRole, oidcRole)
			}
			if oidcTenant != "" {
				ctx = context.WithValue(ctx, ctxKeyOIDCTenant, oidcTenant)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// 5. Try session cookie (browser SSO path).
		if len(s.sessionSecret) > 0 {
			if cookie, err := r.Cookie(sessionCookieName); err == nil {
				if sub, email, err := s.verifySession(cookie.Value); err == nil {
					// 5a. Check revocation in the distributed session store.
					// This catches sessions revoked via backchannel logout or an
					// admin force-logout on any other cluster node.
					revoked := false
					if s.reg != nil {
						tokenHash := sha256HexOf(cookie.Value)
						if _, dbErr := s.reg.GetOIDCSession(r.Context(), tokenHash); dbErr != nil {
							// Session not found or revoked in DB — treat as invalid.
							s.log.Debug("OIDC session revoked or not in DB", "err", dbErr)
							revoked = true
						}
					}
					if !revoked {
						ctx := context.WithValue(r.Context(), ctxKeyOIDCSub, sub)
						ctx = context.WithValue(ctx, ctxKeyOIDCEmail, email)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				} else {
					s.log.Debug("OIDC session cookie invalid", "err", err)
				}
			}
		}
		// 6. No valid credential. Redirect browser requests to /auth/login when
		// the Authorization Code Flow is configured; return 401 JSON otherwise.
		if strings.Contains(r.Header.Get("Accept"), "text/html") &&
			s.oidcConfig != nil && s.oidcConfig.RedirectURI != "" {
			http.Redirect(w, r, "/auth/login", http.StatusFound)
			return
		}
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":   "unauthorized",
			"message": "valid OIDC token required",
		})
	})
}

// rbacPublicPaths are the paths that bypass RBAC regardless of the key
// presented. These are always accessible (e.g. health check, API schema).
var rbacPublicPaths = map[string]bool{
	"/api/v1/cluster/health": true,
	"/api/v1/cluster/status": true,
	"/api/v1/openapi.json":   true,
}

// rbacMiddleware enforces role-based access control on every request based on
// the API key's Role field. It runs after key lookup so the gate is cheap for
// anonymous requests (pass-through) and only becomes O(keys) when a Bearer
// token is present.
//
// Rules (in order):
//  1. Public endpoints (GET /api/v1/cluster/health, /api/v1/openapi.json) → pass through.
//  2. Authorization: Bearer matches s.internalToken → pass through (gateway route-sync).
//  3. No Bearer token → pass through (handler enforces auth if required).
//  4. Bearer token found but no matching key → pass through.
//  5. Role "admin" → pass through.
//  6. Role "viewer" → GET allowed; non-GET → 403.
//  7. Role "inference" → any /api/v1/* path → 403 (inference keys are for the
//     gateway only, not the CP management surface).
func (s *Server) rbacMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Public GET endpoints always pass through.
		if r.Method == http.MethodGet && rbacPublicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// 2. Extract Bearer token.
		token := bearerToken(r)

		// 2a. Internal token passes through unconditionally.
		// Constant-time comparison prevents timing-based enumeration.
		if s.validateInternalToken(token) {
			next.ServeHTTP(w, r)
			return
		}

		// 2b. OIDC-resolved role: if oidcMiddleware already resolved a role from
		// group/role claim mappings, enforce it directly — no API key lookup needed.
		// This path is taken when the token carries a matching group claim and
		// PURSER_OIDC_GROUP_MAPPINGS (or OIDCConfig.GroupMappings) is configured.
		if oidcRole, ok := r.Context().Value(ctxKeyOIDCRole).(string); ok && oidcRole != "" {
			switch oidcRole {
			case "admin":
				next.ServeHTTP(w, r)
			case "viewer":
				if r.Method != http.MethodGet {
					s.writeJSON(w, http.StatusForbidden, map[string]any{
						"error":   "forbidden",
						"message": "OIDC-assigned viewer role allows read-only access",
					})
					return
				}
				next.ServeHTTP(w, r)
			case "inference":
				if strings.HasPrefix(r.URL.Path, "/api/v1/") {
					s.writeJSON(w, http.StatusForbidden, map[string]any{
						"error":   "forbidden",
						"message": "OIDC-assigned inference role cannot manage the cluster",
					})
					return
				}
				next.ServeHTTP(w, r)
			default:
				// Unknown OIDC-mapped role: conservative viewer behavior.
				if r.Method != http.MethodGet {
					s.writeJSON(w, http.StatusForbidden, map[string]any{
						"error":   "forbidden",
						"message": "OIDC-assigned viewer role allows read-only access",
					})
					return
				}
				next.ServeHTTP(w, r)
			}
			return
		}

		// 3. No token → fail-closed for /api/v1/* when auth is configured.
		// Non-management paths (/auth/login, /auth/callback, etc.) always pass
		// through. For management paths: reject with 401 when OIDC is configured
		// or at least one API key exists (bootstrapped deployment). Only allow
		// unauthenticated access when nothing is configured (pure dev mode).
		if token == "" {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				next.ServeHTTP(w, r)
				return
			}
			// Requests carrying a valid X-Purser-Internal-Token always pass through
			// even without a Bearer token — oidcMiddleware already exempts them
			// from OIDC verification; rbacMiddleware must do the same so internal
			// calls (gateway → /api/v1/usage, /api/v1/inference-events, etc.) are
			// not blocked when API keys are configured.
			if s.validateInternalToken(r.Header.Get("X-Purser-Internal-Token")) {
				next.ServeHTTP(w, r)
				return
			}
			if s.oidcVerifier != nil {
				s.writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error":   "unauthorized",
					"message": "authentication required: provide a valid API key or OIDC token",
				})
				return
			}
			// Cached check: once we know API keys exist, skip the DB on every request.
			if s.hasAnyAPIKeyCache.Load() {
				s.writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error":   "unauthorized",
					"message": "authentication required",
				})
				return
			}
			// One-time DB check: if a key exists, cache true and reject.
			if s.reg != nil {
				if has, err := s.reg.HasAnyAPIKey(r.Context()); err == nil && has {
					s.hasAnyAPIKeyCache.Store(true)
					s.writeJSON(w, http.StatusUnauthorized, map[string]any{
						"error":   "unauthorized",
						"message": "authentication required",
					})
					return
				}
			}
			// No API keys, no OIDC → dev/bootstrap mode, pass-through.
			next.ServeHTTP(w, r)
			return
		}

		// 4. Look up the key by hash via an indexed single-row query (O(1)).
		if s.reg == nil {
			next.ServeHTTP(w, r)
			return
		}
		sum := sha256.Sum256([]byte(token))
		hashHex := hex.EncodeToString(sum[:])
		matched, err := s.reg.GetAPIKeyByHash(r.Context(), hashHex)
		if err != nil {
			// ErrNotFound or any registry error — pass through, handler enforces.
			next.ServeHTTP(w, r)
			return
		}

		// 5–7. Enforce role.
		switch matched.Role {
		case "admin":
			next.ServeHTTP(w, r)
		case "viewer":
			if r.Method != http.MethodGet {
				s.writeJSON(w, http.StatusForbidden, map[string]any{
					"error":   "forbidden",
					"message": "this API key has viewer role (read-only)",
				})
				return
			}
			next.ServeHTTP(w, r)
		case "inference":
			if strings.HasPrefix(r.URL.Path, "/api/v1/") {
				s.writeJSON(w, http.StatusForbidden, map[string]any{
					"error":   "forbidden",
					"message": "this API key has inference role and cannot manage the cluster",
				})
				return
			}
			next.ServeHTTP(w, r)
		default:
			// Unknown role: conservative viewer behavior.
			if r.Method != http.MethodGet {
				s.writeJSON(w, http.StatusForbidden, map[string]any{
					"error":   "forbidden",
					"message": "this API key has viewer role (read-only)",
				})
				return
			}
			next.ServeHTTP(w, r)
		}
	})
}

// corsMiddleware sets CORS response headers based on the PURSER_ALLOWED_ORIGINS
// environment variable (comma-separated list of allowed origin values, e.g.
// "https://app.example.com,https://admin.example.com"). The wildcard "*" is
// also accepted. When the variable is empty or unset, all cross-origin requests
// are silently allowed through without any ACAO header (same-origin only policy
// — browsers will block them). Preflight OPTIONS requests are answered
// immediately with 204 No Content when the origin is in the allowed list.
// The allowed list is read once at middleware creation time for performance.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allowedRaw := os.Getenv("PURSER_ALLOWED_ORIGINS")
	allowed := make(map[string]bool)
	if allowedRaw != "" {
		for _, o := range strings.Split(allowedRaw, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowed[o] = true
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowed[origin] || allowed["*"]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			if origin != "" && (allowed[origin] || allowed["*"]) {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, X-Purser-Internal-Token")
				w.Header().Set("Access-Control-Max-Age", "3600")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// resolveGroupRole returns the highest-privilege Purser role found by looking
// up each entry in groups through s.oidcGroupMappings. The privilege order is
// admin > inference > viewer. Returns "" if no mapping is found.
func (s *Server) resolveGroupRole(groups []string) string {
	rolePriority := map[string]int{"admin": 3, "inference": 2, "viewer": 1}
	best := ""
	bestPri := 0
	for _, g := range groups {
		if role, ok := s.oidcGroupMappings[g]; ok {
			if pri := rolePriority[role]; pri > bestPri {
				best = role
				bestPri = pri
			}
		}
	}
	return best
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
// Returns an empty string if the header is absent or malformed.
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// actorFromRequest extracts a displayable actor identity from the request.
// Priority: OIDC sub claim > OIDC email claim > API key fingerprint (first 8
// hex chars of SHA-256) > "system".
//
// This is the single source of truth for audit log Actor fields on HTTP-handled
// requests. Background goroutines and startup code that have no request context
// should use the literal string "system" directly.
func actorFromRequest(r *http.Request) string {
	if sub, ok := r.Context().Value(ctxKeyOIDCSub).(string); ok && sub != "" {
		return "oidc:" + sub
	}
	if email, ok := r.Context().Value(ctxKeyOIDCEmail).(string); ok && email != "" {
		return "oidc:" + email
	}
	if token := bearerToken(r); token != "" {
		sum := sha256.Sum256([]byte(token))
		return "apikey:" + hex.EncodeToString(sum[:])[:8]
	}
	return "system"
}

// rateLimitExempt reports whether the request is exempt from rate limiting.
// GET /api/v1/cluster/health and GET /api/v1/openapi.json are always allowed
// through so that monitoring systems and tooling do not get throttled.
func rateLimitExempt(r *http.Request) bool {
	return r.Method == http.MethodGet && rbacPublicPaths[r.URL.Path]
}

// rateLimitMiddleware enforces two independent token-bucket limits:
//
//  1. Per source-IP: controlled by s.rateLimitRPS. Prevents accidental CI/CD
//     hammering from a single machine. Negative value disables it.
//  2. Per API-key bearer token: controlled by s.rateLimitKeyRPS. Applies
//     whenever the request carries a Bearer token that is not the internal
//     gateway token. Negative value disables it.
//
// On limit exceeded the middleware writes 429 Too Many Requests with a
// "Retry-After: 1" header and does NOT call the next handler.
//
// GET /api/v1/cluster/health and GET /api/v1/openapi.json are always exempt
// (monitoring / health-check endpoints must not be throttled).
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rateLimitExempt(r) {
			next.ServeHTTP(w, r)
			return
		}

		// --- Per-IP limit ---
		if s.rateLimitRPS > 0 {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				// Fallback: use RemoteAddr verbatim (handles bare IPs in tests).
				ip = r.RemoteAddr
			}
			limiter := s.getOrCreateLimiter(&s.ipLimitersMu, s.ipLimiters, s.ipLimitersAccess, ip, s.rateLimitRPS)
			if !limiter.Allow() {
				w.Header().Set("Retry-After", "1")
				s.writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":   "rate_limit_exceeded",
					"message": "too many requests; slow down and retry",
				})
				return
			}
		}

		// --- Per-API-key limit ---
		if s.rateLimitKeyRPS > 0 {
			tok := bearerToken(r)
			// Only apply when there is a bearer token AND it is not the internal
			// gateway token (those are machine-to-machine, high-frequency by design).
			if tok != "" && (s.internalToken == "" || tok != s.internalToken) {
				sum := sha256.Sum256([]byte(tok))
				keyHash := hex.EncodeToString(sum[:])
				limiter := s.getOrCreateLimiter(&s.keyLimitersMu, s.keyLimiters, s.keyLimitersAccess, keyHash, s.rateLimitKeyRPS)
				if !limiter.Allow() {
					w.Header().Set("Retry-After", "1")
					s.writeJSON(w, http.StatusTooManyRequests, map[string]any{
						"error":   "rate_limit_exceeded",
						"message": "API key rate limit exceeded; slow down and retry",
					})
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

// getOrCreateLimiter returns the existing *rate.Limiter for key in m (guarded
// by mu), or lazily creates and stores one using rps as both the steady-state
// rate and the initial burst (burst = max(1, int(rps))). The last-access
// timestamp is updated in access so cleanupLimiters can evict stale entries.
func (s *Server) getOrCreateLimiter(
	mu *sync.Mutex,
	m map[string]*rate.Limiter,
	access map[string]time.Time,
	key string, rps float64,
) *rate.Limiter {
	burst := int(rps)
	if burst < 1 {
		burst = 1
	}
	mu.Lock()
	defer mu.Unlock()
	l, ok := m[key]
	if !ok {
		l = rate.NewLimiter(rate.Limit(rps), burst)
		m[key] = l
	}
	access[key] = time.Now()
	return l
}

func (s *Server) routes() {
	// Authorization Code Flow + PKCE + session management endpoints (browser SSO).
	// These are exempt from oidcMiddleware — they ARE the login/logout flow.
	s.mux.HandleFunc("GET /auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	s.mux.HandleFunc("GET /auth/logout", s.handleAuthLogout)
	// Backchannel logout: called by the IdP when a user session ends at the
	// IdP side. No user credential is presented — the IdP signs the token.
	s.mux.HandleFunc("POST /auth/backchannel-logout", s.handleBackchannelLogout)

	s.mux.HandleFunc("GET /api/v1/nodes", s.handleListNodes)
	s.mux.HandleFunc("GET /api/v1/nodes/{id}", s.handleGetNode)
	s.mux.HandleFunc("POST /api/v1/nodes/{id}/drain", s.handleDrainNode)
	s.mux.HandleFunc("POST /api/v1/nodes/{id}/restart", s.handleRestartNode)
	s.mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.handleDeleteNode)
	s.mux.HandleFunc("GET /api/v1/models", s.handleListModels)
	s.mux.HandleFunc("POST /api/v1/models", s.handleCreateModel)
	s.mux.HandleFunc("POST /api/v1/models/import", s.handleImportModel)
	s.mux.HandleFunc("GET /api/v1/models/{id}", s.handleGetModel)
	s.mux.HandleFunc("DELETE /api/v1/models/{id}", s.handleDeleteModel)
	s.mux.HandleFunc("GET /api/v1/models/{id}/health", s.handleModelHealth)
	s.mux.HandleFunc("POST /api/v1/models/{id}/plan", s.handlePreviewPlan)
	s.mux.Handle("POST /api/v1/models/{id}/deploy",
		s.policyMiddleware("deploy")(http.HandlerFunc(s.handleDeployModel)))
	s.mux.HandleFunc("POST /api/v1/join-token", s.handleJoinToken)
	s.mux.HandleFunc("GET /api/v1/enrollment-bundle", s.handleEnrollmentBundle)
	s.mux.HandleFunc("GET /api/v1/deployments", s.handleListDeployments)
	s.mux.HandleFunc("DELETE /api/v1/deployments/{id}", s.handleDeleteDeployment)
	s.mux.HandleFunc("GET /api/v1/plans/{id}", s.handleGetPlan)
	s.mux.HandleFunc("GET /api/v1/cluster/health", s.handleClusterHealth)
	s.mux.HandleFunc("GET /api/v1/cluster/status", s.handleClusterStatus)
	s.mux.HandleFunc("POST /api/v1/apikeys", s.handleCreateAPIKey)
	s.mux.HandleFunc("GET /api/v1/apikeys", s.handleListAPIKeys)
	s.mux.HandleFunc("DELETE /api/v1/apikeys/{id}", s.handleDeleteAPIKey)
	s.mux.HandleFunc("POST /api/v1/apikeys/{id}/rotate", s.handleRotateAPIKey)
	s.mux.HandleFunc("GET /api/v1/apikeys/{id}/access-log", s.handleListAPIKeyAccess)
	s.mux.HandleFunc("GET /api/v1/metrics", s.handleMetricsSSE)
	s.mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPISpec)

	// Usage accounting endpoints.
	s.mux.HandleFunc("POST /api/v1/usage", s.handleRecordUsage)
	s.mux.HandleFunc("GET /api/v1/apikeys/{id}/usage", s.handleGetKeyUsage)
	s.mux.HandleFunc("GET /api/v1/usage/summary", s.handleUsageSummary)

	// Enterprise (open-core) endpoints. Public code, gated at runtime by a
	// valid, offline-verified license key (see enterprise/license).
	s.mux.HandleFunc("GET /api/v1/enterprise/status", s.handleEnterpriseStatus)
	s.mux.HandleFunc("GET /api/v1/enterprise/audit-log", s.handleEnterpriseAuditLog)

	// Observability: reconciler config + tracker state.
	s.mux.HandleFunc("GET /api/v1/reconciler/status", s.handleReconcilerStatus)

	// Fleet capacity headroom — viewer-accessible.
	s.mux.HandleFunc("GET /api/v1/fleet/capacity", s.handleFleetCapacity)

	// Config-as-code: apply/diff/export purser.yaml desired state.
	s.mux.HandleFunc("POST /api/v1/config/apply", s.handleConfigApply)
	s.mux.HandleFunc("POST /api/v1/config/diff", s.handleConfigDiff)
	s.mux.HandleFunc("GET /api/v1/config/export", s.handleConfigExport)
	// Inference audit log (AI Act Art. 12).
	// GET list and verify are enterprise-gated ("inference_audit" feature) and viewer-accessible.
	// POST is internal-only (gateway→CP) and never enterprise-gated.
	s.mux.HandleFunc("GET /api/v1/inference-audit", s.handleListInferenceAudit)
	s.mux.HandleFunc("GET /api/v1/inference-audit/verify", s.handleVerifyInferenceChain)
	s.mux.HandleFunc("POST /api/v1/inference-events", s.handleRecordInferenceEvent)

	// Policy-as-code (OPA/Rego) — enterprise-gated ("policy_engine" feature).
	s.mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	s.mux.HandleFunc("PUT /api/v1/policies/{name}", s.handleUpsertPolicy)
	s.mux.HandleFunc("DELETE /api/v1/policies/{name}", s.handleDeletePolicy)
	s.mux.HandleFunc("POST /api/v1/policies/eval", s.handleEvalPolicy)

	// Deployment approval gates (AI Act Art.14 human oversight).
	// Enterprise-gated ("deployment_approvals" feature). GET is viewer-accessible;
	// POST approve/reject is admin-only (enforced inside the handler).
	s.mux.HandleFunc("GET /api/v1/approvals", s.handleListApprovals)
	s.mux.HandleFunc("GET /api/v1/approvals/{deploymentId}", s.handleGetApproval)
	s.mux.HandleFunc("POST /api/v1/approvals/{deploymentId}/approve", s.handleApproveDeployment)
	s.mux.HandleFunc("POST /api/v1/approvals/{deploymentId}/reject", s.handleRejectDeployment)

	// Billing / chargeback — GET /billing/report is enterprise-gated ("billing"
	// feature); GET /billing/summary is open for all viewer/admin roles.
	s.mux.HandleFunc("GET /api/v1/billing/report", s.handleBillingReport)
	s.mux.HandleFunc("GET /api/v1/billing/summary", s.handleBillingSummary)

	// GDPR Art.17 right-to-erasure — admin only, enterprise-gated ("gdpr" feature).
	s.mux.HandleFunc("POST /api/v1/gdpr/erasure", s.handleGDPRErasure)
	s.mux.HandleFunc("GET /api/v1/gdpr/erasure-log", s.handleGDPRErasureLog)
}

// featureAudit is the entitlement required by the tamper-evident audit log
// (see LICENSING.md, "Compliance").
const featureAudit = "audit"

// featurePolicyEngine is the enterprise entitlement for OPA/Rego policy
// evaluation. When absent, policyMiddleware is a no-op.
const featurePolicyEngine = "policy_engine"

// reloadPolicies fetches all enabled policies from the registry, compiles them
// into a fresh OPA engine, and atomically swaps it in. Callers: New() and
// every PUT/DELETE policy handler.
func (s *Server) reloadPolicies(ctx context.Context) {
	if s.reg == nil {
		return
	}
	rows, err := s.reg.ListPolicies(ctx)
	if err != nil {
		s.log.Warn("policy: could not list policies from registry", "err", err)
		return
	}
	var sources []policy.PolicySource
	for _, p := range rows {
		if p.Enabled {
			sources = append(sources, policy.PolicySource{Name: p.Name, Rego: p.Rego})
		}
	}
	eng, err := policy.LoadPolicies(ctx, sources)
	if err != nil {
		s.log.Warn("policy: failed to compile policies", "err", err)
		return
	}
	s.policyMu.Lock()
	s.policyEngine = eng
	s.policyMu.Unlock()
	s.log.Info("policy engine reloaded", "count", len(sources))
}

// policyMiddleware gates requests with action through the active OPA engine.
// It is a no-op when:
//   - the enterprise "policy_engine" feature is not licensed, or
//   - no policies are loaded (open-by-default semantics).
func (s *Server) policyMiddleware(action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.licenseAllows(featurePolicyEngine) {
				next.ServeHTTP(w, r)
				return
			}
			s.policyMu.RLock()
			eng := s.policyEngine
			s.policyMu.RUnlock()
			if eng == nil {
				next.ServeHTTP(w, r)
				return
			}
			req := policy.EvalRequest{
				Action:  action,
				ModelID: r.PathValue("id"),
			}
			if tok := bearerToken(r); tok != "" {
				sum := sha256.Sum256([]byte(tok))
				req.KeyHash = hex.EncodeToString(sum[:])
			}
			result, err := eng.Allow(r.Context(), req)
			if err != nil {
				s.writeError(w, http.StatusInternalServerError, "policy_eval_failed", err.Error())
				return
			}
			if !result.Allowed {
				s.writeJSON(w, http.StatusForbidden, map[string]any{
					"error":   "policy_denied",
					"message": result.Reason,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// handleOpenAPISpec serves the embedded OpenAPI 3.0 specification as JSON.
// The spec is embedded at compile time from openapi.json (generated from
// openapi.yaml) and served verbatim — no runtime conversion needed.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPISpec)
}

// licenseAllows reports whether the active license currently entitles feature:
// it must be temporally valid now AND include the feature. This is the single
// choke point every enterprise handler calls before doing premium work.
func (s *Server) licenseAllows(feature string) bool {
	return s.license.ValidAt(time.Now()) && s.license.HasFeature(feature)
}

// writeLicenseRequired emits the 402 Payment Required response used when an
// enterprise feature is called without a valid entitlement.
func (s *Server) writeLicenseRequired(w http.ResponseWriter, feature string) {
	s.writeJSON(w, http.StatusPaymentRequired, map[string]any{
		"error": map[string]any{
			"message": "enterprise license required",
			"feature": feature,
			"type":    "license_required",
		},
	})
}

// handleEnterpriseStatus reports the active edition. With a valid license it
// returns the licensee and enabled features; otherwise it reports the community
// edition. It never fails and never phones home — the verdict comes entirely
// from the offline-verified key loaded at startup.
func (s *Server) handleEnterpriseStatus(w http.ResponseWriter, r *http.Request) {
	lic := s.license
	if lic.IsCommunity() || !lic.ValidAt(time.Now()) {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"edition":  "community",
			"licensee": "community",
			"features": []string{},
		})
		return
	}
	feats := lic.Features
	if feats == nil {
		feats = []string{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"edition":  "enterprise",
		"licensee": lic.Licensee,
		"features": feats,
		"expires":  lic.Expires,
	})
}

// defaultAuditLimit caps how many recent audit rows the endpoint returns (and
// verifies) when the caller does not specify ?limit=.
const defaultAuditLimit = 100

// handleEnterpriseAuditLog is the premium tamper-evident audit-log endpoint,
// gated on the "audit" feature. Without a valid entitlement it returns 402
// Payment Required. With one it reads the most recent entries (default
// defaultAuditLimit, override with ?limit=N), reconstructs them into the hash
// chain in ascending seq order, verifies the chain end to end, and reports both
// the entries and a chain summary:
//
//	{
//	  "feature":  "audit",
//	  "licensee": "...",
//	  "entries":  [ {audit.Entry}, ... ],   // ascending seq
//	  "chain":    { "verified": bool, "length": n,
//	                "break": { "index": i, "seq": s, "kind": "seq|link|hash",
//	                           "msg": "..." }? }   // present only when !verified
//	}
//
// A failed verification is reported as verified:false with the break location —
// it is never a 500. Rows written before the hash chain existed (Seq==0) are
// skipped so a legacy prefix cannot spuriously fail the chain. Verification is
// sound over the returned window when that window covers the chain from genesis
// (the default for logs within the limit).
func (s *Server) handleEnterpriseAuditLog(w http.ResponseWriter, r *http.Request) {
	if !s.licenseAllows(featureAudit) {
		s.writeLicenseRequired(w, featureAudit)
		return
	}

	limit := defaultAuditLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}

	rows, err := s.reg.ListAudit(r.Context(), limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_audit_failed", err.Error())
		return
	}

	// ListAudit returns newest-first; the chain must be verified oldest-first
	// (ascending seq). Reverse into ascending order, skipping any legacy rows
	// that predate the chain (Seq < FirstSeq).
	entries := make([]audit.Entry, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Seq < audit.FirstSeq {
			continue
		}
		entries = append(entries, rows[i].ChainEntry())
	}

	chain := map[string]any{"verified": true, "length": len(entries)}
	if verr := audit.Verify(entries); verr != nil {
		chain["verified"] = false
		var ve *audit.VerifyError
		if errors.As(verr, &ve) {
			chain["break"] = map[string]any{
				"index": ve.Index,
				"seq":   ve.Seq,
				"kind":  ve.Kind,
				"msg":   ve.Msg,
			}
		} else {
			chain["break"] = map[string]any{"msg": verr.Error()}
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"feature":  featureAudit,
		"licensee": s.license.Licensee,
		"entries":  entries,
		"chain":    chain,
	})
}

// handleReconcilerStatus returns the reconciler's current configuration and
// per-event-type tracker snapshot. Viewer-accessible (GET). Returns 501 when
// no reconciler is wired up (e.g. in test servers that omit it from Config).
//
// Response shape:
//
//	{
//	  "config": { "interval_s": 10, "node_timeout_s": 45,
//	              "hysteresis_s": 30, "action_cooldown_s": 60 },
//	  "tracker": {
//	    "node_down":          { "tracked": 0, "oldest_age_s": 0 },
//	    "orphan_deployment":  { "tracked": 1, "oldest_age_s": 120 }
//	  }
//	}
func (s *Server) handleReconcilerStatus(w http.ResponseWriter, r *http.Request) {
	if s.reconcilerStatus == nil {
		s.writeError(w, http.StatusNotImplemented, "no_reconciler", "reconciler not configured")
		return
	}
	s.writeJSON(w, http.StatusOK, s.reconcilerStatus.Status())
}

// handleListNodes returns all nodes known to the registry.
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.reg.ListNodes(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_nodes_failed", err.Error())
		return
	}
	if nodes == nil {
		nodes = []*registry.Node{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// handleGetNode returns a single node by ID.
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.reg.GetNode(r.Context(), r.PathValue("id"))
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_node_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, n)
}

// handleDrainNode cordons a node. It marks the node DRAINING via the fleet
// manager (auditing fleet.node.draining) so the planner stops scheduling NEW
// deployments onto it.
//
// HONESTY: this cordons the node only. It does NOT live-migrate, rebalance, or
// fail over the deployments already running on the node — that execution is a
// separate, not-yet-complete capability and is deliberately not claimed here.
// An unknown node yields 404; success is 200 with the node's new state.
func (s *Server) handleDrainNode(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		s.writeError(w, http.StatusNotImplemented, "no_fleet", "fleet manager not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.fleet.Drain(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "node not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "drain_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"node_id": id,
		"state":   fleet.NodeStateDraining,
		"message": "node cordoned (unschedulable); existing deployments are not migrated or rebalanced",
	})
}

// handleDeleteNode decommissions a node via the fleet manager: it transitions
// the node to DECOMMISSIONED and revokes its certificates (auditing
// fleet.node.decommissioned). It is a guarded operation, never a cascade,
// mirroring handleDeleteModel:
//
//  1. an unknown id yields 404;
//  2. if any non-terminal deployment still occupies the node (its host or one
//     of its engines runs there) the delete is refused with 409 "node_in_use"
//     listing the blocking deployment id(s) — tear those down or migrate them
//     first;
//  3. otherwise the node is decommissioned and 204 No Content is returned.
//
// HONESTY: "decommission" is a lifecycle transition (state → DECOMMISSIONED +
// certificate revocation), not a hard row deletion; the node remains visible in
// GET /api/v1/nodes in the DECOMMISSIONED state.
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		s.writeError(w, http.StatusNotImplemented, "no_fleet", "fleet manager not configured")
		return
	}
	id := r.PathValue("id")

	// 404 up front so a missing node never reports as "in use".
	if _, err := s.reg.GetNode(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "node not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "get_node_failed", err.Error())
		return
	}

	// Safety: refuse while any live deployment still occupies the node.
	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}
	var blocking []string
	for _, d := range deps {
		if !deploymentTerminal(d.State) && deploymentOccupiesNode(d, id) {
			blocking = append(blocking, d.ID)
		}
	}
	if len(blocking) > 0 {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":       "node_in_use",
			"message":     "node still hosts one or more active deployments; tear them down or migrate them first",
			"deployments": blocking,
		})
		return
	}

	if err := s.fleet.Decommission(r.Context(), id); err != nil {
		// A concurrent decommission may have removed it between the checks above
		// and here; surface that as the same 404, anything else as 500.
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "node not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "decommission_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestartNode tears down all active deployments on the node and lets the
// reconciler re-provision them on remaining available nodes. The node itself
// is not rebooted.
//
// Responses:
//   - 404 if the node does not exist;
//   - 409 if the node has no active deployments to restart (nothing to do);
//   - 202 Accepted on success — restart is async; actual re-provisioning
//     happens in the background as the reconciler notices the STOPPED
//     deployments and re-schedules them on remaining READY nodes.
func (s *Server) handleRestartNode(w http.ResponseWriter, r *http.Request) {
	if s.deployer == nil {
		s.writeError(w, http.StatusNotImplemented, "no_deployer", "orchestrator not configured")
		return
	}
	id := r.PathValue("id")

	// 404 up front so a missing node is never misreported as "nothing to restart".
	if _, err := s.reg.GetNode(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "node not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "get_node_failed", err.Error())
		return
	}

	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}
	var targets []string
	for _, d := range deps {
		if !deploymentTerminal(d.State) && deploymentOccupiesNode(d, id) {
			targets = append(targets, d.ID)
		}
	}

	// 409 if there are no active deployments to restart.
	if len(targets) == 0 {
		s.writeError(w, http.StatusConflict, "nothing_to_restart",
			"node has no active deployments to restart")
		return
	}

	// Tear down each deployment; the reconciler will notice the STOPPED state
	// and re-provision on remaining READY nodes.
	for _, depID := range targets {
		if err := s.deployer.Teardown(r.Context(), depID); err != nil {
			s.log.Warn("restart: teardown failed",
				"node", id, "deployment", depID, "err", err)
		}
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: actorFromRequest(r), Action: "api.node.restart", Target: id,
	})
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"node_id":     id,
		"deployments": targets,
		"message":     "deployments torn down; re-provisioning proceeds in the background",
	})
}

// modelWithFit augments a catalog Model with its deployability verdict against
// the current fleet. The embedded *registry.Model promotes its JSON fields, so
// the wire shape is the model plus a "fit" object.
type modelWithFit struct {
	*registry.Model
	Fit planning.Fit `json:"fit"`
}

// handleListModels returns the model catalog. When a Planner is configured each
// entry is annotated with a fit verdict (deployable / node count + estimated
// tok/s range, or the deficit) so the UI can render the "Runs / Doesn't fit"
// badge without a second round-trip.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.reg.ListModels(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_models_failed", err.Error())
		return
	}
	if models == nil {
		models = []*registry.Model{}
	}
	if s.planner == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"models": models})
		return
	}

	fitByID := map[string]planning.Fit{}
	if fits, err := s.planner.FitAll(r.Context()); err != nil {
		// A planner hiccup must not hide the catalog: log and serve without fit.
		s.log.Warn("compute model fit verdicts failed", "err", err)
	} else {
		for _, f := range fits {
			fitByID[f.ModelID] = f
		}
	}
	out := make([]modelWithFit, 0, len(models))
	for _, m := range models {
		out = append(out, modelWithFit{Model: m, Fit: fitByID[m.ID]})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// handleCreateModel registers a model in the catalog. The request body is a
// protojson-encoded purser.v1.ModelSpec — the same encoding the catalog
// persists and the planner reads back — so the spec round-trips losslessly. The
// promoted columns (family/architecture/params/engine) are derived from the
// spec for cheap listing/querying.
func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB body limit
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
			"request body exceeds 1 MB limit")
		return
	}
	if len(raw) == 0 {
		s.writeError(w, http.StatusBadRequest, "bad_request", "empty body: expected a protojson ModelSpec")
		return
	}
	spec := &purserv1.ModelSpec{}
	if err := protojson.Unmarshal(raw, spec); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_spec", "invalid ModelSpec: "+err.Error())
		return
	}
	id := spec.GetModelId()
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "bad_spec", "model_id is required")
		return
	}

	// Reject duplicates up front for a clean 409 (the store's PK is the final
	// guard against a racing create — handled below).
	if _, err := s.reg.GetModel(r.Context(), id); err == nil {
		s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+id)
		return
	} else if !errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusInternalServerError, "get_model_failed", err.Error())
		return
	}

	// Re-marshal via protojson so the stored blob is canonical and matches how
	// the planner decodes it.
	blob, err := protojson.Marshal(spec)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_spec_failed", err.Error())
		return
	}
	// Type defaults to "llm". The ModelSpec proto does not carry a model-type
	// field yet, so the field is set here as a constant default. Future callers
	// that need to register embedding models can supply a pre-built
	// registry.Model directly via the store, or extend the proto.
	m := &registry.Model{
		ID:           id,
		Family:       spec.GetFamily(),
		Architecture: spec.GetArchitecture(),
		ParamsTotalB: spec.GetParamsTotalB(),
		Engine:       spec.GetEngine(),
		Type:         "llm",
		Spec:         blob,
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		// A UNIQUE-constraint failure (e.g. a racing create) is a conflict, not
		// an internal error.
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+id)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: actorFromRequest(r), Action: "model.created", Target: id})
	s.writeJSON(w, http.StatusCreated, map[string]any{"model_id": id})
}

// importRequest is the body of POST /api/v1/models/import.
//
// HuggingFace (source="huggingface"): set repo, revision, filename_pattern.
// Object storage (source="s3"/"gcs"/"azure"): set uri, name (optional), family.
// SageMaker (source="sagemaker"): set model_group (optional override), version.
// VertexAI (source="vertexai"): set model, vertex_version (empty means latest).
type importRequest struct {
	Source string `json:"source"`
	// HuggingFace fields
	Repo            string `json:"repo"`
	Revision        string `json:"revision"`
	FilenamePattern string `json:"filename_pattern"`
	// Object-storage fields (s3://, gs://, az://)
	URI    string  `json:"uri"`
	Name   string  `json:"name"`
	Family string  `json:"family"`
	SizeGB float64 `json:"size_gb"`
	// SageMaker fields
	// ModelGroup overrides PURSER_SAGEMAKER_MODEL_GROUP for "sagemaker" imports.
	ModelGroup string `json:"model_group,omitempty"`
	// Version selects a specific approved package version; 0 means latest.
	Version int `json:"version,omitempty"`
	// VertexAI fields
	// Model is the GCP Vertex AI model resource name
	// ("projects/p/locations/l/models/m") or a bare model ID
	// (requires PURSER_VERTEX_PROJECT to be set).
	Model string `json:"model,omitempty"`
	// VertexVersion selects a specific Vertex AI model version. Empty means latest.
	VertexVersion string `json:"vertex_version,omitempty"`
	// Azure ML fields
	// Workspace is an optional per-request override for the Azure ML workspace
	// name. When empty, the server falls back to PURSER_AZURE_ML_WORKSPACE.
	Workspace string `json:"workspace,omitempty"`
	// AzureVersion selects a specific Azure ML model version. Empty means latest.
	AzureVersion string `json:"azure_version,omitempty"`
}

// hfSourceBlob is the JSON shape stored in Model.Source for HuggingFace
// imports. It is kept small on purpose — it is the import provenance, not the
// full model spec.
type hfSourceBlob struct {
	Type           string `json:"type"`
	Repo           string `json:"repo"`
	Revision       string `json:"revision"`
	Filename       string `json:"filename"`
	SizeBytesTotal int64  `json:"size_bytes_total"`
}

// handleImportModel registers a model imported from an external source.
// It dispatches by the "source" field in the request body:
//
//   - "huggingface" — auto-populates spec from HuggingFace Hub metadata.
//   - "s3", "gcs", "azure" — resolves the object-storage URI to a download URL.
//   - "sagemaker" — lists approved packages from an AWS SageMaker model group.
//   - "vertexai" — imports from GCP Vertex AI Model Registry.
//   - "azureml" — imports from an Azure ML workspace.
//
// POST /api/v1/models/import
func (s *Server) handleImportModel(w http.ResponseWriter, r *http.Request) {
	var body importRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	switch body.Source {
	case "huggingface":
		s.handleImportHuggingFace(w, r, body)
	case "s3", "gcs", "azure":
		s.handleImportObjectStorage(w, r, body)
	case "sagemaker":
		s.handleImportSageMaker(w, r, body)
	case "vertexai":
		s.handleImportVertexAI(w, r, body)
	case "azureml":
		s.handleImportAzureML(w, r, body)
	default:
		s.writeError(w, http.StatusBadRequest, "unknown_source",
			"unknown import source: "+body.Source+"; supported: huggingface, s3, gcs, azure, sagemaker, vertexai, azureml")
	}
}

// handleImportHuggingFace implements the "huggingface" branch of handleImportModel.
//
// The HuggingFace API token is resolved in precedence order:
//  1. X-HF-Token request header (caller-supplied, e.g. CI workflows);
//  2. PURSER_HF_TOKEN server env var (operator-configured default).
//
// Error mapping:
//   - HF API 404 → 404 not_found
//   - HF API 401/403 → 401 hf_auth_required
//   - No matching GGUF files → 400 no_matching_files
//   - Network/decode failure → 502 Bad Gateway
//   - Model already exists → 409 model_exists
func (s *Server) handleImportHuggingFace(w http.ResponseWriter, r *http.Request, body importRequest) {
	if body.Repo == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "repo is required")
		return
	}
	if body.Revision == "" {
		body.Revision = "main"
	}

	// Resolve HF token: header takes precedence over server env var.
	token := r.Header.Get("X-HF-Token")
	if token == "" {
		token = s.hfToken
	}

	hfc := importer.NewHFClient(token)
	if s.hfBaseURL != "" {
		hfc.BaseURL = s.hfBaseURL
	}

	meta, err := hfc.FetchMetadata(r.Context(), body.Repo, body.Revision, body.FilenamePattern)
	if err != nil {
		if importer.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, "not_found",
				"HuggingFace repo not found: "+body.Repo)
			return
		}
		if importer.IsAuthRequired(err) {
			s.writeError(w, http.StatusUnauthorized, "hf_auth_required",
				"private/gated model requires PURSER_HF_TOKEN: "+body.Repo)
			return
		}
		s.writeError(w, http.StatusBadGateway, "hf_fetch_failed",
			"HuggingFace API error: "+err.Error())
		return
	}

	if len(meta.GGUFFiles) == 0 {
		pattern := body.FilenamePattern
		if pattern == "" {
			pattern = "*.gguf"
		}
		s.writeError(w, http.StatusBadRequest, "no_matching_files",
			"no "+pattern+" files found in repo "+body.Repo)
		return
	}

	// Best match: first GGUF file in the list. Compute total size over all
	// matching files (useful when multiple quantisations are downloaded).
	bestFile := meta.GGUFFiles[0].Name
	var sizeBytesTotal int64
	for _, f := range meta.GGUFFiles {
		sizeBytesTotal += f.Size
	}

	// Model ID = last path component of the repo name (e.g. "Llama-3.1-8B-Instruct").
	modelID := path.Base(meta.ModelID)

	// Check for duplicates (clean 409 before the store's PK constraint).
	if _, err := s.reg.GetModel(r.Context(), modelID); err == nil {
		s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+modelID)
		return
	} else if !errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusInternalServerError, "get_model_failed", err.Error())
		return
	}

	sourceBlob := hfSourceBlob{
		Type:           "huggingface",
		Repo:           body.Repo,
		Revision:       body.Revision,
		Filename:       bestFile,
		SizeBytesTotal: sizeBytesTotal,
	}
	sourceJSON, err := json.Marshal(sourceBlob)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_source_failed", err.Error())
		return
	}

	m := &registry.Model{
		ID:     modelID,
		Family: meta.Family,
		Source: sourceJSON,
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+modelID)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "model.imported",
		Target: modelID,
	})
	s.writeJSON(w, http.StatusCreated, m)
}

// handleImportObjectStorage implements the "s3"/"gcs"/"azure" branch of
// handleImportModel. It resolves the object-storage URI to an HTTPS download
// URL (pre-signed when credentials are present, public otherwise) and stores
// the result in Model.Source so the agent can fetch the weights at deploy time.
func (s *Server) handleImportObjectStorage(w http.ResponseWriter, r *http.Request, body importRequest) {
	if body.URI == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "uri is required")
		return
	}

	objSrc, err := importer.ParseObjectURI(body.URI)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_uri", err.Error())
		return
	}

	// Derive the model ID from the request name; fall back to the key's
	// last path segment (best-effort, not authoritative metadata).
	id := body.Name
	if id == "" {
		if objSrc.Key != "" {
			parts := strings.Split(objSrc.Key, "/")
			id = parts[len(parts)-1]
		}
		if id == "" {
			id = objSrc.Bucket + "-" + objSrc.Type
		}
	}

	// Reject duplicates before attempting the insert.
	if _, err := s.reg.GetModel(r.Context(), id); err == nil {
		s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+id)
		return
	} else if !errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusInternalServerError, "get_model_failed", err.Error())
		return
	}

	sourceBlob, err := json.Marshal(objSrc)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_source_failed", err.Error())
		return
	}

	m := &registry.Model{
		ID:     id,
		Family: body.Family,
		Source: sourceBlob,
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+id)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: actorFromRequest(r), Action: "model.imported", Target: id,
	})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"model_id":     id,
		"source_type":  objSrc.Type,
		"download_url": objSrc.DownloadURL,
	})
}

// tenantedDetail is a minimal JSON decode of the deployment Detail blob that
// extracts only the tenant field. It overlaps structurally with
// orchestrator.DeploymentDetail (which also carries FailoverPlanID, Engines,
// etc.) but is defined here to avoid importing the orchestrator package just for
// this field. Both structs are valid JSON shapes for the same blob.
type tenantedDetail struct {
	Tenant string `json:"tenant"`
}

// deploymentTenant extracts the tenant field from a deployment's Detail blob.
// Returns "" when the field is absent, blank, or the blob cannot be decoded.
func deploymentTenant(d *registry.Deployment) string {
	if len(d.Detail) == 0 {
		return ""
	}
	var td tenantedDetail
	if err := json.Unmarshal(d.Detail, &td); err != nil {
		return ""
	}
	return td.Tenant
}

// deploymentTerminal reports whether a deployment in the given state has
// released its placement. Only STOPPED and FAILED are terminal; every other
// state — PLANNED, PROVISIONING, ACTIVE, REBALANCING, STOPPING — is live and
// still holds both its model binding and its node placement.
func deploymentTerminal(state string) bool {
	switch state {
	case purserv1.DeploymentState_DEPLOYMENT_STATE_STOPPED.String(),
		purserv1.DeploymentState_DEPLOYMENT_STATE_FAILED.String():
		return true
	default:
		return false
	}
}

// deploymentPinsModel reports whether a deployment in the given state still
// binds the model it references (i.e. it is not terminal) — deployments are
// torn down explicitly, never implicitly by a model delete.
func deploymentPinsModel(state string) bool {
	return !deploymentTerminal(state)
}

// deploymentNodeRefs is a minimal decode of the orchestrator's
// Deployment.Detail blob — just the node references — so the API can tell
// whether a deployment still occupies a node without importing the orchestrator
// (the server intentionally depends on the orchestrator only through the
// Deployer interface). Fields mirror orchestrator.DeploymentDetail's JSON tags.
type deploymentNodeRefs struct {
	HostNodeID string `json:"host_node_id"`
	Engines    []struct {
		NodeID string `json:"node_id"`
	} `json:"engines"`
}

// deploymentOccupiesNode reports whether the deployment's persisted detail
// places its host or any of its engines on nodeID. A deployment with no decoded
// placement (empty/invalid detail) is treated as not occupying the node.
func deploymentOccupiesNode(d *registry.Deployment, nodeID string) bool {
	if len(d.Detail) == 0 {
		return false
	}
	var refs deploymentNodeRefs
	if err := json.Unmarshal(d.Detail, &refs); err != nil {
		return false
	}
	if refs.HostNodeID == nodeID {
		return true
	}
	for _, e := range refs.Engines {
		if e.NodeID == nodeID {
			return true
		}
	}
	return false
}

// handleGetModel returns a single model by ID.
func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	m, err := s.reg.GetModel(r.Context(), r.PathValue("id"))
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_model_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, m)
}

// handleDeleteModel removes a model from the catalog. It is a guarded delete,
// never a cascade:
//
//  1. an unknown id yields 404 (mirroring handleGetNode);
//  2. if any non-terminal deployment still references the model, the delete is
//     refused with 409 "model_in_use" listing the blocking deployment id(s) —
//     deployments are torn down explicitly, never implicitly by a model delete;
//  3. otherwise the catalog row is removed and 204 No Content is returned.
func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 404 up front so a missing model never reports as "in use".
	if _, err := s.reg.GetModel(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "get_model_failed", err.Error())
		return
	}

	// Safety: refuse while any live deployment still pins the model.
	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}
	var blocking []string
	for _, d := range deps {
		if d.ModelID == id && deploymentPinsModel(d.State) {
			blocking = append(blocking, d.ID)
		}
	}
	if len(blocking) > 0 {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":       "model_in_use",
			"message":     "model is referenced by one or more active deployments; tear them down first",
			"deployments": blocking,
		})
		return
	}

	if err := s.reg.DeleteModel(r.Context(), id); err != nil {
		// A concurrent delete may have removed it between the checks above and
		// here; surface that as the same 404, anything else as 500.
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "delete_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: actorFromRequest(r), Action: "model.deleted", Target: id})
	w.WriteHeader(http.StatusNoContent)
}

// ModelHealth is the response body of GET /api/v1/models/{id}/health.
type ModelHealth struct {
	ModelID         string `json:"model_id"`
	Status          string `json:"status"` // "healthy" | "degraded" | "unavailable"
	DeploymentID    string `json:"deployment_id"`
	DeploymentState string `json:"deployment_state"`
	NodeCount       int    `json:"node_count"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

// handleModelHealth reports the operational health of a deployed model.
// Does not perform a live inference probe.
//
// Rules:
//   - 404 if the model does not exist in the catalog.
//   - "healthy"   when the most-recent deployment is ACTIVE.
//   - "degraded"  when the deployment is PROVISIONING or STOPPING (transient).
//   - "unavailable" when FAILED, STOPPED, or no deployment exists.
func (s *Server) handleModelHealth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 404 if the model is not in the catalog.
	if _, err := s.reg.GetModel(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "get_model_failed", err.Error())
		return
	}

	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}

	// Pick the most-recent deployment for this model (by CreatedAt).
	var latest *registry.Deployment
	for _, d := range deps {
		d := d
		if d.ModelID != id {
			continue
		}
		if latest == nil || d.CreatedAt.After(latest.CreatedAt) {
			latest = d
		}
	}

	health := ModelHealth{ModelID: id}
	if latest == nil {
		health.Status = "unavailable"
		health.ErrorMessage = "no deployment found for this model"
		s.writeJSON(w, http.StatusOK, health)
		return
	}

	health.DeploymentID = latest.ID
	// Strip the "DEPLOYMENT_STATE_" proto prefix for a clean wire representation.
	health.DeploymentState = strings.TrimPrefix(latest.State, "DEPLOYMENT_STATE_")
	health.NodeCount = countDeploymentNodes(latest)

	switch latest.State {
	case purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String():
		health.Status = "healthy"
	case purserv1.DeploymentState_DEPLOYMENT_STATE_PROVISIONING.String(),
		purserv1.DeploymentState_DEPLOYMENT_STATE_STOPPING.String():
		health.Status = "degraded"
	default: // FAILED, STOPPED, PLANNED, or any unrecognised state
		health.Status = "unavailable"
		health.ErrorMessage = "deployment is in state " + health.DeploymentState
	}

	s.writeJSON(w, http.StatusOK, health)
}

// countDeploymentNodes decodes the deployment's placement detail and counts
// the total number of nodes it references (1 host + N engines). Returns 0 when
// no placement detail is stored.
func countDeploymentNodes(d *registry.Deployment) int {
	if len(d.Detail) == 0 {
		return 0
	}
	var refs deploymentNodeRefs
	if err := json.Unmarshal(d.Detail, &refs); err != nil {
		return 0
	}
	n := 0
	if refs.HostNodeID != "" {
		n++
	}
	n += len(refs.Engines)
	return n
}

// deployRequest is the body of POST /models/{id}/deploy. Provide either an
// inline plan (protojson DeploymentPlan) or a plan_id referencing a stored plan.
type deployRequest struct {
	PlanID string          `json:"plan_id,omitempty"`
	Plan   json.RawMessage `json:"plan,omitempty"`
}

// handleDeployModel resolves a DeploymentPlan and hands it to the orchestrator.
//
// The plan is obtained in priority order:
//  1. an inline `plan` (protojson) in the body — caller-supplied;
//  2. a stored plan referenced by `plan_id`;
//  3. otherwise the Planner produces one from the current fleet (READY nodes +
//     link matrix + catalog ModelSpec) and it is persisted to the `plans` table
//     before being applied — this is the normal, plan-less deploy path.
func (s *Server) handleDeployModel(w http.ResponseWriter, r *http.Request) {
	if s.deployer == nil {
		s.writeError(w, http.StatusNotImplemented, "no_deployer", "orchestrator not configured")
		return
	}
	modelID := r.PathValue("id")
	var body deployRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
	}

	plan := &purserv1.DeploymentPlan{}
	switch {
	case len(body.Plan) > 0:
		if err := protojson.Unmarshal(body.Plan, plan); err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_plan", "invalid plan: "+err.Error())
			return
		}
	case body.PlanID != "":
		row, err := s.reg.GetPlan(r.Context(), body.PlanID)
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "plan not found")
			return
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "get_plan_failed", err.Error())
			return
		}
		if err := protojson.Unmarshal(row.Plan, plan); err != nil {
			s.writeError(w, http.StatusInternalServerError, "bad_plan", "stored plan is invalid: "+err.Error())
			return
		}
	case s.planner != nil:
		produced, ok := s.planFromFleet(w, r, modelID)
		if !ok {
			return // planFromFleet already wrote the response
		}
		plan = produced
	default:
		s.writeError(w, http.StatusBadRequest, "bad_request", "provide plan or plan_id (no planner configured)")
		return
	}
	if plan.ModelId == "" {
		plan.ModelId = modelID
	}

	// Deployment approval gate (AI Act Art.14). When the enterprise feature
	// "deployment_approvals" is active, queue an approval record and return
	// a pending_approval status — the actual rollout runs only after an admin
	// calls POST /api/v1/approvals/{id}/approve.
	if s.licenseAllows(featureDeploymentApprovals) {
		pendingDepID := randHex(8)
		requester := apiKeyHashFromRequest(r)
		approval := &registry.DeploymentApproval{
			DeploymentID: pendingDepID,
			ModelID:      plan.ModelId,
			Requester:    requester,
		}
		if err := s.reg.RequestDeploymentApproval(r.Context(), approval); err != nil {
			s.writeError(w, http.StatusInternalServerError, "approval_queue_failed", err.Error())
			return
		}
		_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
			Actor: requester, Action: "deployment.approval.requested", Target: pendingDepID,
		})
		s.writeJSON(w, http.StatusAccepted, map[string]any{
			"status":        "pending_approval",
			"deployment_id": pendingDepID,
			"model_id":      plan.ModelId,
			"message":       "deployment queued for admin approval (AI Act Art.14); call POST /api/v1/approvals/" + pendingDepID + "/approve to proceed",
		})
		return
	}

	depID, err := s.deployer.Apply(r.Context(), plan)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":         "deploy_failed",
			"message":       err.Error(),
			"deployment_id": depID,
		})
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"deployment_id": depID,
		"model_id":      plan.ModelId,
		"plan_id":       plan.GetPlanId(),
	})
}

// planFromFleet runs the Planner for modelID and persists the produced plan. On
// any failure it writes the appropriate error response and returns ok=false:
//   - 404 if the model is unknown;
//   - 422 with reason/deficit/suggestions if the model does not fit the fleet;
//   - 500 on internal/persistence errors.
func (s *Server) planFromFleet(w http.ResponseWriter, r *http.Request, modelID string) (*purserv1.DeploymentPlan, bool) {
	produced, err := s.planner.Plan(r.Context(), modelID, plannerplan.Constraints{})
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "model not found")
		return nil, false
	}
	var fe *planning.FitError
	if errors.As(err, &fe) {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":       "model_does_not_fit",
			"message":     fe.Error(),
			"reason":      fe.Reason,
			"deficit_gb":  fe.DeficitGB,
			"suggestions": fe.Suggestions,
		})
		return nil, false
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "plan_failed", err.Error())
		return nil, false
	}

	// Assign a unique persistence ID (the planner's is deterministic, so two
	// deploys of the same model would collide on the plans PK) and store it so
	// GET /plans/{id} can serve the explanation.
	produced.ModelId = modelID
	produced.PlanId = produced.GetPlanId() + "-" + randHex(4)
	if err := s.persistPlan(r.Context(), produced); err != nil {
		s.writeError(w, http.StatusInternalServerError, "persist_plan_failed", err.Error())
		return nil, false
	}
	return produced, true
}

// persistPlan stores a produced plan (protojson-encoded, matching how plans are
// read back) in the plans table.
func (s *Server) persistPlan(ctx context.Context, plan *purserv1.DeploymentPlan) error {
	blob, err := protojson.Marshal(plan)
	if err != nil {
		return err
	}
	return s.reg.CreatePlan(ctx, &registry.Plan{
		ID:           plan.GetPlanId(),
		ModelID:      plan.GetModelId(),
		Quantization: plan.GetQuantization(),
		Cost:         plan.GetCost(),
		Plan:         blob,
	})
}

// previewResponse is the body of POST /models/{id}/plan. It embeds a
// registry.Plan so a feasible preview marshals to the exact wire shape GET
// /plans/{id} serves — the UI renders a dry-run identically to a stored plan —
// prefixed by a "feasible" flag. The embedded plan is ephemeral: it is never
// written to the plans table, so its id resolves to no stored row.
type previewResponse struct {
	Feasible bool `json:"feasible"`
	*registry.Plan
}

// handlePreviewPlan is the read-only dry run behind POST /models/{id}/plan: it
// computes the Planner's layer-split plan for a model against the CURRENT fleet
// and returns it WITHOUT persisting anything and WITHOUT deploying.
//
// It reuses the deploy path's planning half — the same READY-node fleet and the
// SAME planner call (see planFromFleet) — but stops there: it never writes to
// the plans table, never invokes the orchestrator, and (being a read, not a
// mutation) emits no audit event.
//
// The feasibility verdict shapes the body, not the status code. Preview is a
// Community capability, so a model that does not fit is a 200 with
// {"feasible": false, "reason": "<planner error>"} — never the deploy path's
// 402/422. A feasible plan is a 200 with "feasible": true and the plan inline,
// marshalled exactly as persistPlan/handleGetPlan do so the UI can render it
// identically; the inline plan is ephemeral and its id resolves to no stored
// plan. Other outcomes mirror the deploy path: 404 for an unknown model
// (like handleGetNode), 501 when no Planner is configured, 500 on internal
// failures.
func (s *Server) handlePreviewPlan(w http.ResponseWriter, r *http.Request) {
	if s.planner == nil {
		s.writeError(w, http.StatusNotImplemented, "no_planner", "planner not configured")
		return
	}
	modelID := r.PathValue("id")

	produced, err := s.planner.Plan(r.Context(), modelID, plannerplan.Constraints{})
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	var fe *planning.FitError
	if errors.As(err, &fe) {
		// Infeasibility is a normal preview outcome, not an error: report it as
		// 200 with the planner's reason so callers can render "doesn't fit"
		// without treating it as a failed request.
		s.writeJSON(w, http.StatusOK, map[string]any{
			"feasible": false,
			"reason":   fe.Error(),
		})
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "plan_failed", err.Error())
		return
	}

	// Marshal the plan exactly as the persist/get-plan path does, but do NOT
	// persist it: no persistence id is minted, no plans row is written, no
	// orchestrator is invoked, and no audit event is emitted.
	produced.ModelId = modelID
	blob, err := protojson.Marshal(produced)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_plan_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, previewResponse{
		Feasible: true,
		Plan: &registry.Plan{
			ID:           produced.GetPlanId(),
			ModelID:      produced.GetModelId(),
			Quantization: produced.GetQuantization(),
			Cost:         produced.GetCost(),
			Plan:         blob,
		},
	})
}

// handleListDeployments returns all deployments. Tenant-scoped OIDC viewer
// tokens restrict the response to deployments whose Detail.tenant matches the
// token's tenant claim (foundational multi-tenant isolation layer).
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}
	if deps == nil {
		deps = []*registry.Deployment{}
	}

	// Tenant scoping: a viewer with an OIDC tenant claim only sees deployments
	// whose Detail JSON contains a matching "tenant" field. Admin tokens and
	// API-key-based viewers see all deployments (no tenant claim → no filter).
	if oidcRole, _ := r.Context().Value(ctxKeyOIDCRole).(string); oidcRole == "viewer" {
		if oidcTenant, _ := r.Context().Value(ctxKeyOIDCTenant).(string); oidcTenant != "" {
			var filtered []*registry.Deployment
			for _, d := range deps {
				if deploymentTenant(d) == oidcTenant {
					filtered = append(filtered, d)
				}
			}
			if filtered == nil {
				filtered = []*registry.Deployment{}
			}
			deps = filtered
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"deployments": deps})
}

// handleDeleteDeployment tears down a deployment.
func (s *Server) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	if s.deployer == nil {
		s.writeError(w, http.StatusNotImplemented, "no_deployer", "orchestrator not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.deployer.Teardown(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "deployment not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "teardown_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"deployment_id": id, "state": "stopping"})
}

// handleGetPlan returns a stored plan by ID.
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.reg.GetPlan(r.Context(), r.PathValue("id"))
	if errors.Is(err, registry.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_plan_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

// ClusterHealth is the response body of the cluster health endpoint.
type ClusterHealth struct {
	Status     string    `json:"status"`
	TotalNodes int       `json:"total_nodes"`
	ReadyNodes int       `json:"ready_nodes"`
	CheckedAt  time.Time `json:"checked_at"`
}

// handleClusterHealth reports a coarse cluster health summary derived from the
// registry: DB reachability plus node counts.
func (s *Server) handleClusterHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.reg.Ping(ctx); err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, ClusterHealth{
			Status:    "unavailable",
			CheckedAt: time.Now().UTC(),
		})
		return
	}
	nodes, err := s.reg.ListNodes(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "health_failed", err.Error())
		return
	}
	ready := 0
	for _, n := range nodes {
		if n.State == "NODE_STATE_READY" || n.State == "NODE_STATE_RUNNING" {
			ready++
		}
	}
	status := "ok"
	if len(nodes) == 0 {
		status = "empty"
	} else if ready == 0 {
		status = "degraded"
	}
	s.writeJSON(w, http.StatusOK, ClusterHealth{
		Status:     status,
		TotalNodes: len(nodes),
		ReadyNodes: ready,
		CheckedAt:  time.Now().UTC(),
	})
}

// createAPIKeyRequest is the body of POST /apikeys.
type createAPIKeyRequest struct {
	Name   string `json:"name"`
	Tenant string `json:"tenant"`
	Quota  int64  `json:"quota"`
	// Role is the RBAC role for the key: "admin" (default), "viewer", or
	// "inference". An empty role is treated as "admin" for backward compat.
	Role string `json:"role,omitempty"`
}

// handleCreateAPIKey mints a new gateway API key. The plaintext key is returned
// exactly once; only its hash is persisted.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var body createAPIKeyRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
	}

	// Resolve and validate the role. Default "admin" for backward compat.
	role := body.Role
	if role == "" {
		role = "admin"
	}
	switch role {
	case "admin", "viewer", "inference":
		// valid
	default:
		s.writeError(w, http.StatusBadRequest, "invalid_role", `role must be one of: admin, viewer, inference`)
		return
	}

	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		s.writeError(w, http.StatusInternalServerError, "keygen_failed", err.Error())
		return
	}
	plaintext := "psk_" + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(plaintext))
	id := "key-" + randHex(8)
	key := &registry.APIKey{
		ID:      id,
		Name:    body.Name,
		KeyHash: hex.EncodeToString(sum[:]),
		Tenant:  body.Tenant,
		Role:    role,
		Quota:   body.Quota,
		Enabled: true,
	}
	if err := s.reg.CreateAPIKey(r.Context(), key); err != nil {
		s.writeError(w, http.StatusInternalServerError, "create_apikey_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: actorFromRequest(r), Action: "apikey.created", Target: id})
	// Return the plaintext key ONCE.
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id":     id,
		"name":   body.Name,
		"tenant": body.Tenant,
		"role":   role,
		"key":    plaintext,
	})
}

// handleListAPIKeys returns all API keys. The plaintext key and its SHA-256
// hash are never returned — only metadata (id, name, tenant, quota, enabled,
// created_at, updated_at). The KeyHash field on registry.APIKey carries
// json:"-" so it is excluded from marshalling automatically.
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.reg.ListAPIKeys(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_apikeys_failed", err.Error())
		return
	}
	if keys == nil {
		keys = []*registry.APIKey{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"apikeys": keys})
}

// handleDeleteAPIKey revokes (permanently removes) an API key by ID.
// Returns 404 if the key does not exist, 204 No Content on success.
// Emits an apikey.deleted audit event on success.
func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reg.DeleteAPIKey(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "api key not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "delete_apikey_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: actorFromRequest(r), Action: "apikey.deleted", Target: id})
	w.WriteHeader(http.StatusNoContent)
}

// joinTokenRequest is the optional body of POST /join-token.
type joinTokenRequest struct {
	// TTLSeconds is the token lifetime; <= 0 falls back to the fleet default.
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

// handleJoinToken mints a single-use, expiring cluster join token. The operator
// (or the E2E harness) hands the returned token to a machine via
// PURSER_JOIN_TOKEN; the agent then enrolls over the RegistrationService gRPC
// Join RPC. The plaintext token is returned once and never persisted.
func (s *Server) handleJoinToken(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		s.writeError(w, http.StatusNotImplemented, "no_fleet", "fleet manager not configured")
		return
	}
	var body joinTokenRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
			return
		}
	}
	var ttl time.Duration // 0 => fleet default (1h)
	if body.TTLSeconds > 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	tok, err := s.fleet.GenerateJoinToken(r.Context(), ttl)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "join_token_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: actorFromRequest(r), Action: "join_token.minted", Target: s.clusterID})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"token":      tok.Token,
		"expires_at": tok.ExpiresAt.UTC().Format(time.RFC3339),
		"cluster_id": s.clusterID,
	})
}

// handleEnrollmentBundle generates a pre-compiled .env file containing the
// three variables an agent needs to auto-enroll. The bundle is suitable for
// direct placement at /etc/purser/agent.env on target nodes — operators do not
// need to copy individual values.
//
// Query parameters:
//
//	ttl_seconds — token lifetime; <= 0 falls back to the fleet default (1h).
//
// Response: text/plain with a comment header (generation time, expiry) and the
// three env vars:
//
//	PURSER_CONTROL_PLANE_ADDR — s.publicAddr (overridable via PURSER_PUBLIC_ADDR)
//	PURSER_CLUSTER_ID         — s.clusterID
//	PURSER_JOIN_TOKEN         — freshly minted single-use token
func (s *Server) handleEnrollmentBundle(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		s.writeError(w, http.StatusNotImplemented, "no_fleet", "fleet manager not configured")
		return
	}
	var ttl time.Duration // 0 => fleet default (1h)
	if qs := r.URL.Query().Get("ttl_seconds"); qs != "" {
		if secs, err := strconv.ParseInt(qs, 10, 64); err == nil && secs > 0 {
			ttl = time.Duration(secs) * time.Second
		}
	}
	tok, err := s.fleet.GenerateJoinToken(r.Context(), ttl)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "join_token_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: actorFromRequest(r), Action: "enrollment_bundle.created", Target: s.clusterID})

	now := time.Now().UTC()
	expires := tok.ExpiresAt.UTC()
	bundle := fmt.Sprintf(
		"# Purser Agent Enrollment Bundle\n"+
			"# Generated: %s\n"+
			"# Expires:   %s\n"+
			"# Copy this file to /etc/purser/agent.env on each node you want to enroll.\n"+
			"\n"+
			"PURSER_CONTROL_PLANE_ADDR=%s\n"+
			"PURSER_CLUSTER_ID=%s\n"+
			"PURSER_JOIN_TOKEN=%s\n",
		now.Format(time.RFC3339),
		expires.Format(time.RFC3339),
		s.publicAddr,
		s.clusterID,
		tok.Token,
	)

	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprint(w, bundle)
}

// handleMetricsSSE streams live cluster metrics as Server-Sent Events. It emits
// an initial snapshot immediately, then one every MetricsInterval, until the
// client disconnects.
func (s *Server) handleMetricsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "no_flush", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx := r.Context()
	emit := func() bool {
		snap, err := s.metricsSnapshot(ctx)
		if err != nil {
			return false
		}
		b, err := json.Marshal(snap)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return false
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !emit() {
		return
	}
	ticker := time.NewTicker(s.metricTO)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !emit() {
				return
			}
		}
	}
}

// metricsSnapshot builds the SSE payload for one tick.
//
// Priority:
//  1. NodeMetricsGetter (real hardware data) — enumerates every registry node
//     and merges cached heartbeat metrics; nodes that have not yet reported
//     receive zero-filled metrics (honest, no hidden gaps).
//  2. MetricsSource (legacy interface) — delegates to Snapshot().
//  3. Fallback — plain registry state summary (no hardware data).
func (s *Server) metricsSnapshot(ctx context.Context) (any, error) {
	if s.nodeMetrics != nil {
		return s.metricsSnapshotFromCache(ctx)
	}
	if s.metrics != nil {
		return s.metrics.Snapshot(ctx)
	}
	// Fallback: plain node-state summary — no hardware metrics.
	nodes, err := s.reg.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	byState := map[string]int{}
	for _, n := range nodes {
		byState[n.State]++
	}
	return map[string]any{
		"total_nodes": len(nodes),
		"by_state":    byState,
		"at":          time.Now().UTC(),
	}, nil
}

// metricsWire is the per-node hardware-metrics payload inside each SSE frame.
type metricsWire struct {
	PrefillTokS         float64 `json:"prefill_tok_s"`
	DecodeTokS          float64 `json:"decode_tok_s"`
	RAMUsedGB           float64 `json:"ram_used_gb"`
	VRAMUsedGB          float64 `json:"vram_used_gb"`
	QueueDepth          uint32  `json:"queue_depth"`
	AcceptedTokensRatio float64 `json:"accepted_tokens_ratio"`
}

// nodeWire is one node entry inside each SSE frame.
type nodeWire struct {
	NodeID  string      `json:"node_id"`
	State   string      `json:"state"`
	Metrics metricsWire `json:"metrics"`
}

// metricsSnapshotFromCache builds the real-data SSE payload by joining the
// full registry node list against cached heartbeat metrics. Nodes that have
// not yet reported receive zero-filled metrics so the UI always sees every
// known node.
func (s *Server) metricsSnapshotFromCache(ctx context.Context) (any, error) {
	nodes, err := s.reg.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]nodeWire, 0, len(nodes))
	var aggDecode float64
	for _, n := range nodes {
		nw := nodeWire{NodeID: n.ID, State: n.State}
		if m, ok := s.nodeMetrics.Get(n.ID); ok {
			nw.Metrics = metricsWire{
				PrefillTokS:         m.PrefillTps,
				DecodeTokS:          m.DecodeTps,
				RAMUsedGB:           m.RAMUsedGB,
				VRAMUsedGB:          m.VRAMUsedGB,
				QueueDepth:          m.QueueDepth,
				AcceptedTokensRatio: m.AcceptedTokensRatio,
			}
			aggDecode += m.DecodeTps
		}
		out = append(out, nw)
	}
	return map[string]any{
		"at":                     time.Now().UTC().Format(time.RFC3339),
		"aggregate_decode_tok_s": aggDecode,
		"nodes":                  out,
	}, nil
}

// --- Usage accounting handlers ---------------------------------------------

// usageRequest is the body of POST /api/v1/usage.
type usageRequest struct {
	APIKeyID     string `json:"api_key_id"`
	ModelID      string `json:"model_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// handleRecordUsage is the internal gateway callback for usage accounting.
// When InternalToken is set, the caller must present the same value in
// X-Purser-Internal-Token; if not set, the endpoint is open (dev/single-node).
// The comparison uses constant-time equality to prevent timing side-channels.
func (s *Server) handleRecordUsage(w http.ResponseWriter, r *http.Request) {
	if s.internalToken != "" {
		tok := r.Header.Get("X-Purser-Internal-Token")
		if !s.validateInternalToken(tok) {
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing internal token")
			return
		}
	}
	var body usageRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if body.APIKeyID == "" || body.ModelID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "api_key_id and model_id are required")
		return
	}
	if err := s.reg.RecordUsage(r.Context(), body.APIKeyID, body.ModelID, body.InputTokens, body.OutputTokens); err != nil {
		s.writeError(w, http.StatusInternalServerError, "record_usage_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleGetKeyUsage returns the aggregate token usage for a single API key.
// Returns 404 if the key does not exist.
func (s *Server) handleGetKeyUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.reg.GetAPIKey(r.Context(), id); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "not_found", "api key not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "get_apikey_failed", err.Error())
		return
	}
	summary, err := s.reg.GetKeyUsage(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_key_usage_failed", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, summary)
}

// handleUsageSummary returns per-tenant token usage, optionally filtered by a
// ?since=<RFC3339> query parameter. Tenants with no usage in the window are
// omitted. An absent since means "all time".
func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if q := r.URL.Query().Get("since"); q != "" {
		t, err := time.Parse(time.RFC3339, q)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "bad_request", "invalid since: must be RFC3339, got "+q)
			return
		}
		since = t
	}
	tenants, err := s.reg.GetUsageSummary(r.Context(), since)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "get_usage_summary_failed", err.Error())
		return
	}
	if tenants == nil {
		tenants = []registry.TenantUsage{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tenants": tenants})
}

// handleImportSageMaker implements the "sagemaker" import path:
//  1. Build a SageMakerClient from env + request overrides.
//  2. List approved packages in the model group.
//  3. Select the latest (version==0) or the requested version.
//  4. Create a registry.Model with source metadata in Spec.
//  5. Return 201 with the created model and its source JSON.
func (s *Server) handleImportSageMaker(w http.ResponseWriter, r *http.Request, body importRequest) {
	client := importer.NewSageMakerClient()
	if body.ModelGroup != "" {
		client.ModelGroup = body.ModelGroup
	}
	if client.ModelGroup == "" {
		s.writeError(w, http.StatusBadRequest, "missing_model_group",
			"model_group is required (set in request body or PURSER_SAGEMAKER_MODEL_GROUP env)")
		return
	}

	packages, err := client.ListApprovedModelPackages(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "sagemaker_error", err.Error())
		return
	}
	if len(packages) == 0 {
		s.writeError(w, http.StatusNotFound, "no_approved_packages",
			"no approved model packages found in group "+client.ModelGroup)
		return
	}

	// packages is sorted newest-first; version==0 means "latest".
	var pkg importer.ModelPackage
	if body.Version == 0 {
		pkg = packages[0]
	} else {
		found := false
		for _, p := range packages {
			if p.ModelPackageVersion == body.Version {
				pkg = p
				found = true
				break
			}
		}
		if !found {
			s.writeError(w, http.StatusNotFound, "version_not_found",
				"version "+strconv.Itoa(body.Version)+" not found in approved packages")
			return
		}
	}

	// Model ID: {GroupName}-v{N}
	modelID := pkg.ModelPackageGroupName + "-v" + strconv.Itoa(pkg.ModelPackageVersion)

	// Source metadata stored as Spec (opaque JSON blob).
	type sourceDoc struct {
		Type    string `json:"type"`
		ARN     string `json:"arn"`
		S3URI   string `json:"s3_uri"`
		Version int    `json:"version"`
	}
	src := sourceDoc{
		Type:    "sagemaker",
		ARN:     pkg.ModelPackageArn,
		S3URI:   pkg.ModelDataURL,
		Version: pkg.ModelPackageVersion,
	}
	specJSON, err := json.Marshal(src)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_source_failed", err.Error())
		return
	}

	m := &registry.Model{
		ID:     modelID,
		Family: importer.GuessFamilyFromName(pkg.ModelPackageGroupName, pkg.ModelPackageDescription),
		Spec:   specJSON,
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+modelID)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "model.imported",
		Target: modelID,
	})

	s.writeJSON(w, http.StatusCreated, map[string]any{
		"model_id": modelID,
		"source":   json.RawMessage(specJSON),
	})
}

// handleImportVertexAI imports a model from the GCP Vertex AI Model Registry.
//
// Flow:
//  1. Resolve the VertexAIClient (injected via Config.VertexAI for tests, or
//     constructed from env vars: PURSER_VERTEX_PROJECT, PURSER_VERTEX_LOCATION,
//     GOOGLE_APPLICATION_CREDENTIALS).
//  2. Validate that a GCP project is known (either from the client config or
//     embedded in the model's full resource name).
//  3. Call ListModelVersions → pick the requested version or the latest.
//  4. Extract the GCS artifact URI from the chosen version (or via GetArtifactURI
//     if the list endpoint did not include it).
//  5. Persist a registry.Model with source metadata in the Spec JSON blob.
//  6. Return 201 with {model_id, source}.
func (s *Server) handleImportVertexAI(w http.ResponseWriter, r *http.Request, body importRequest) {
	// Resolve the client: prefer the injected one (tests / static config) over
	// constructing one from env at request time.
	client := s.vertexai
	if client == nil {
		client = importer.NewVertexAIClient()
	}

	// Validate project: required when the model name is not a full resource path.
	isFullPath := strings.HasPrefix(body.Model, "projects/")
	if client.Project == "" && !isFullPath {
		s.writeError(w, http.StatusBadRequest, "missing_project",
			"PURSER_VERTEX_PROJECT must be set when model is not a full resource name")
		return
	}

	// List versions to find the target.
	versions, err := client.ListModelVersions(r.Context(), body.Model)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_versions_failed", err.Error())
		return
	}
	if len(versions) == 0 {
		s.writeError(w, http.StatusNotFound, "no_versions", "model has no registered versions in Vertex AI")
		return
	}

	// Pick the specified version, or the latest (versions[0] is newest first).
	var chosen importer.ModelVersion
	if body.VertexVersion != "" {
		for _, v := range versions {
			if v.VersionID == body.VertexVersion {
				chosen = v
				break
			}
		}
		if chosen.VersionID == "" {
			s.writeError(w, http.StatusNotFound, "version_not_found",
				"version not found in Vertex AI: "+body.VertexVersion)
			return
		}
	} else {
		chosen = versions[0]
	}

	// Resolve the GCS artifact URI. ListModelVersions populates ArtifactURI
	// when the API returns it inline; fall back to GetArtifactURI otherwise.
	gcsURI := chosen.ArtifactURI
	if gcsURI == "" {
		gcsURI, err = client.GetArtifactURI(r.Context(), body.Model, chosen.VersionID)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "get_artifact_uri_failed", err.Error())
			return
		}
	}

	// Build source metadata.
	source := map[string]string{
		"type":    "vertexai",
		"model":   body.Model,
		"version": chosen.VersionID,
		"gcs_uri": gcsURI,
	}
	sourceJSON, _ := json.Marshal(source)

	// Derive a stable model ID from the last path segment and the version.
	modelID := body.Model
	if idx := strings.LastIndex(modelID, "/"); idx >= 0 {
		modelID = modelID[idx+1:]
	}
	modelID = modelID + "@" + chosen.VersionID

	// Spec carries the source metadata. For VertexAI imports no ModelSpec proto
	// is available, so the Spec blob is the source object itself wrapped to
	// keep the JSON well-formed and extensible.
	specJSON, _ := json.Marshal(map[string]any{"source": source})

	m := &registry.Model{
		ID:   modelID,
		Spec: json.RawMessage(specJSON),
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists", "model already imported: "+modelID)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:   actorFromRequest(r),
		Action:  "model.imported",
		Target:  modelID,
		Details: json.RawMessage(sourceJSON),
	})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"model_id": modelID,
		"source":   source,
	})
}

// handleImportAzureML imports a model from an Azure ML workspace.
//
// It authenticates via OAuth2 client credentials, lists the requested model's
// versions, selects the latest Production version (or latest overall), and
// creates a catalog entry with the source metadata stored in Spec.
func (s *Server) handleImportAzureML(w http.ResponseWriter, r *http.Request, body importRequest) {
	if body.Model == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "model name is required")
		return
	}

	client, err := importer.NewAzureMLClient(body.Workspace)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	versions, err := client.ListModelVersions(r.Context(), body.Model)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "azureml_error",
			"list model versions: "+err.Error())
		return
	}

	var selected importer.AzureMLModelVersion
	if body.AzureVersion != "" {
		found := false
		for _, v := range versions {
			if v.Version == body.AzureVersion {
				selected = v
				found = true
				break
			}
		}
		if !found {
			s.writeError(w, http.StatusNotFound, "version_not_found",
				"version not found in workspace: "+body.AzureVersion)
			return
		}
	} else {
		v, ok := importer.LatestVersion(versions)
		if !ok {
			s.writeError(w, http.StatusNotFound, "no_versions",
				"no versions found for model: "+body.Model)
			return
		}
		selected = v
	}

	src := map[string]any{
		"type":         "azureml",
		"workspace":    client.Workspace,
		"model":        body.Model,
		"version":      selected.Version,
		"artifact_uri": selected.ArtifactURI,
	}
	srcJSON, err := json.Marshal(src)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_source_failed", err.Error())
		return
	}

	modelID := body.Model
	m := &registry.Model{
		ID:   modelID,
		Spec: srcJSON,
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists",
				"model already exists: "+modelID)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor: actorFromRequest(r), Action: "model.imported", Target: modelID,
	})
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"model_id": modelID,
		"source":   src,
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Error("encode response", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, code int, kind, msg string) {
	s.writeJSON(w, code, map[string]any{"error": kind, "message": msg})
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// OTEL HTTP middleware
// ---------------------------------------------------------------------------

// statusWriter wraps http.ResponseWriter to capture the status code written
// by the downstream handler, so the middleware can record it as a span attribute.
// It forwards http.Flusher if the underlying writer supports it (required by
// the SSE metrics endpoint).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// Write captures a 200 when the handler calls Write without WriteHeader first.
func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher by delegating to the underlying ResponseWriter
// when it supports flushing. The SSE endpoint casts the writer to http.Flusher;
// without this delegation that cast would fail on a statusWriter.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// otelMiddleware wraps next with an OTEL trace span for every HTTP request.
// It records http.method, http.path, and http.status_code as span attributes.
// When no real TracerProvider is configured (the default) the global no-op
// tracer is used, so the middleware is completely transparent with zero overhead.
func otelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer := otel.Tracer("purser.control-plane")
		ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.path", r.URL.Path),
			),
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(ctx))

		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		span.SetAttributes(attribute.Int("http.status_code", status))
	})
}

// ---------------------------------------------------------------------------
// Infrastructure metrics collector
// ---------------------------------------------------------------------------

// StartInfraMetrics runs a background goroutine that samples infrastructure
// gauges (deployments.active, nodes.ready, nodes.total) every 30 seconds and
// pushes them to the configured MeterProvider (no-op when OTEL is not
// configured). The goroutine exits when ctx is cancelled. A deferred recover
// catches any unexpected panics and logs them so a single bad sample does not
// bring down the whole server.
func (s *Server) StartInfraMetrics(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("panic in background goroutine", "recovered", r)
			}
		}()
		s.collectInfraMetrics(ctx) // initial sample immediately
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.collectInfraMetrics(ctx)
			}
		}
	}()
}

// FleetCapacity is the response body of GET /api/v1/fleet/capacity.
type FleetCapacity struct {
	VRAMTotalGB             float64  `json:"vram_total_gb"`
	VRAMUsedGB              float64  `json:"vram_used_gb"`
	VRAMHeadroomGB          float64  `json:"vram_headroom_gb"`
	RAMTotalGB              float64  `json:"ram_total_gb"`
	RAMHeadroomGB           float64  `json:"ram_headroom_gb"`
	MemBandwidthTotalGBs    float64  `json:"mem_bandwidth_total_gbs"`
	MemBandwidthHeadroomGBs float64  `json:"mem_bandwidth_headroom_gbs"`
	ReadyNodes              int      `json:"ready_nodes"`
	Bottleneck              string   `json:"bottleneck"`
	CanFitModels            []string `json:"can_fit_models"`
}

// handleFleetCapacity aggregates resource totals and headroom across all READY
// nodes and reports which catalog models can be deployed right now.
//
//   - vram/ram/bandwidth totals are summed from the hardware profiles of all READY nodes.
//   - "used" is computed by finding which nodes host active deployments and
//     treating their full VRAM contribution as consumed.
//   - bottleneck is the resource with the lowest headroom-to-total ratio.
//   - can_fit_models calls the Planner's FitAll (if configured).
//
// RBAC: viewer-accessible (GET only; no mutation).
func (s *Server) handleFleetCapacity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodes, err := s.reg.ListNodes(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_nodes_failed", err.Error())
		return
	}
	deps, err := s.reg.ListDeployments(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}

	// Build a map of node resources for quick lookup.
	type nodeResources struct {
		vramGB float64
		ramGB  float64
		bwGBs  float64
	}
	nodeByID := make(map[string]*nodeResources, len(nodes))
	var cap FleetCapacity

	for _, n := range nodes {
		if n.State != "NODE_STATE_READY" && n.State != "NODE_STATE_RUNNING" {
			continue
		}
		cap.ReadyNodes++

		res := &nodeResources{
			vramGB: n.VRAMGB,
			ramGB:  n.RAMGB,
		}
		// Decode hardware profile for bandwidth and more accurate RAM figures.
		if len(n.HardwareProfile) > 0 && string(n.HardwareProfile) != "{}" {
			hw := &purserv1.HardwareProfile{}
			if err := protojson.Unmarshal(n.HardwareProfile, hw); err == nil {
				res.bwGBs = hw.GetMemBandwidthGbs()
				if hw.GetRamTotalGb() > 0 {
					res.ramGB = hw.GetRamTotalGb()
				}
			}
		}
		nodeByID[n.ID] = res
		cap.VRAMTotalGB += res.vramGB
		cap.RAMTotalGB += res.ramGB
		cap.MemBandwidthTotalGBs += res.bwGBs
	}

	// Compute "used" from nodes occupied by ACTIVE deployments.
	activeState := purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String()
	usedNodes := make(map[string]bool)
	for _, d := range deps {
		if d.State != activeState {
			continue
		}
		if len(d.Detail) == 0 {
			continue
		}
		var refs deploymentNodeRefs
		if err := json.Unmarshal(d.Detail, &refs); err != nil {
			continue
		}
		if refs.HostNodeID != "" {
			usedNodes[refs.HostNodeID] = true
		}
		for _, e := range refs.Engines {
			if e.NodeID != "" {
				usedNodes[e.NodeID] = true
			}
		}
	}

	var vramUsed, ramUsed, bwUsed float64
	for nodeID := range usedNodes {
		if res, ok := nodeByID[nodeID]; ok {
			vramUsed += res.vramGB
			ramUsed += res.ramGB
			bwUsed += res.bwGBs
		}
	}
	cap.VRAMUsedGB = vramUsed
	cap.VRAMHeadroomGB = cap.VRAMTotalGB - vramUsed
	cap.RAMHeadroomGB = cap.RAMTotalGB - ramUsed
	cap.MemBandwidthHeadroomGBs = cap.MemBandwidthTotalGBs - bwUsed

	// Determine bottleneck: the resource with the lowest headroom/total ratio.
	cap.Bottleneck = fleetBottleneck(
		cap.VRAMTotalGB, cap.VRAMHeadroomGB,
		cap.RAMTotalGB, cap.RAMHeadroomGB,
		cap.MemBandwidthTotalGBs, cap.MemBandwidthHeadroomGBs,
	)

	// Which models can be placed on the current fleet?
	cap.CanFitModels = []string{}
	if s.planner != nil {
		if fits, err := s.planner.FitAll(ctx); err == nil {
			for _, f := range fits {
				if f.Deployable {
					cap.CanFitModels = append(cap.CanFitModels, f.ModelID)
				}
			}
		} else {
			s.log.Warn("fleet capacity: FitAll failed", "err", err)
		}
	}

	s.writeJSON(w, http.StatusOK, cap)
}

// fleetBottleneck returns the label of the most constrained resource based on
// the headroom-to-total ratio. When all totals are zero it returns "none".
func fleetBottleneck(vramTotal, vramHeadroom, ramTotal, ramHeadroom, bwTotal, bwHeadroom float64) string {
	ratio := func(headroom, total float64) float64 {
		if total <= 0 {
			return 1 // unavailable resource: treat as unconstrained
		}
		return headroom / total
	}
	vramR := ratio(vramHeadroom, vramTotal)
	ramR := ratio(ramHeadroom, ramTotal)
	bwR := ratio(bwHeadroom, bwTotal)

	if vramTotal <= 0 && ramTotal <= 0 && bwTotal <= 0 {
		return "none"
	}
	switch {
	case vramR <= ramR && vramR <= bwR:
		return "vram"
	case ramR <= bwR:
		return "ram"
	default:
		return "mem_bandwidth"
	}
}

// collectInfraMetrics queries the registry for deployment and node counts and
// records them as gauge readings. Errors are logged and silently skipped so a
// transient registry hiccup never brings down the metrics loop.
func (s *Server) collectInfraMetrics(ctx context.Context) {
	deps, err := s.reg.ListDeployments(ctx)
	if err != nil {
		s.log.Warn("infra metrics: list deployments failed", "err", err)
	} else {
		var active int64
		for _, d := range deps {
			if d.State == purserv1.DeploymentState_DEPLOYMENT_STATE_ACTIVE.String() {
				active++
			}
		}
		s.gaugeDeploymentsActive.Record(ctx, active)
	}

	nodes, err := s.reg.ListNodes(ctx)
	if err != nil {
		s.log.Warn("infra metrics: list nodes failed", "err", err)
		return
	}
	var ready int64
	for _, n := range nodes {
		if n.State == "NODE_STATE_READY" || n.State == "NODE_STATE_RUNNING" {
			ready++
		}
	}
	s.gaugeNodesReady.Record(ctx, ready)
	s.gaugeNodesTotal.Record(ctx, int64(len(nodes)))

	// Per-node hardware metrics: only emitted when a NodeMetricsGetter is
	// wired (i.e. the fleet registration server is live). Nodes that have
	// not yet sent a heartbeat are skipped — zero-filling every node would
	// produce misleading data for clusters with many cold nodes.
	if s.nodeMetrics != nil {
		for _, n := range nodes {
			m, ok := s.nodeMetrics.Get(n.ID)
			if !ok {
				continue
			}
			attrs := metric.WithAttributes(attribute.String("node_id", n.ID))
			s.gaugeNodeCPU.Record(ctx, m.CpuUtilizationPct, attrs)
			s.gaugeNodeGPU.Record(ctx, m.GpuUtilizationPct, attrs)
			s.gaugeNodeMemBandwidth.Record(ctx, m.MemBandwidthUtilPct, attrs)
			s.gaugeNodeTokPerSec.Record(ctx, m.TokensPerSecond, attrs)
			alive := int64(0)
			if m.InferencePortAlive {
				alive = 1
			}
			s.gaugeNodeInferenceAlive.Record(ctx, alive, attrs)
		}
	}
}
