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
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/audit"
	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/planning"
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

// contextKey is a private key type for values stored in request contexts.
// Using a package-local type avoids collisions with keys from other packages.
type contextKey int

const (
	// ctxKeyOIDCSub is the context key for the OIDC subject claim.
	ctxKeyOIDCSub contextKey = iota
	// ctxKeyOIDCEmail is the context key for the OIDC email claim.
	ctxKeyOIDCEmail
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

// Config configures the HTTP server.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Logger is used for request/error logging; a default is used if nil.
	Logger *slog.Logger
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
	// bypass OIDC verification so the gateway can perform route-sync without a
	// human token.
	InternalToken string
	// HFToken is the HuggingFace API token used by POST /api/v1/models/import
	// when the caller does not supply an X-HF-Token header. Read from
	// PURSER_HF_TOKEN at startup. Leave empty for public-model-only access.
	HFToken string
	// HFBaseURL overrides the HuggingFace API base URL used by the import
	// handler. Leave empty to use the default (https://huggingface.co). Useful
	// in tests that point the server at an httptest mock.
	HFBaseURL string
}

// Server holds the API dependencies and the composed HTTP handler.
type Server struct {
	reg           registry.Registry
	log           *slog.Logger
	mux           *http.ServeMux
	server        *http.Server
	deployer      Deployer
	metrics       MetricsSource
	nodeMetrics   NodeMetricsGetter
	metricTO      time.Duration
	planner       *planning.Planner
	fleet         FleetManager
	clusterID     string
	publicAddr    string
	license       *license.License
	oidcVerifier  TokenVerifier // nil = OIDC disabled
	internalToken string        // gateway exemption secret
	hfToken       string
	hfBaseURL     string

	// handler is the mux wrapped with OTEL (outer) and OIDC (inner) middleware.
	// Returned by Handler() and used as http.Server.Handler so all test paths
	// go through the same middleware chain.
	handler http.Handler

	// OTEL infrastructure gauge instruments. All three are no-ops unless a real
	// MeterProvider was installed by telemetry.Init before New() is called.
	gaugeDeploymentsActive metric.Int64Gauge
	gaugeNodesReady        metric.Int64Gauge
	gaugeNodesTotal        metric.Int64Gauge
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
	s := &Server{
		reg:           reg,
		log:           logger,
		mux:           http.NewServeMux(),
		deployer:      cfg.Deployer,
		metrics:       cfg.Metrics,
		nodeMetrics:   cfg.NodeMetrics,
		metricTO:      interval,
		planner:       cfg.Planner,
		fleet:         cfg.Fleet,
		clusterID:     clusterID,
		publicAddr:    publicAddr,
		license:       lic,
		internalToken: cfg.InternalToken,
		hfToken:       cfg.HFToken,
		hfBaseURL:     cfg.HFBaseURL,
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

	s.routes()

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

	// Wrap the mux with OTEL (outer) then OIDC (inner) middleware so every
	// route gets a trace span that covers the full request including auth.
	// When no TracerProvider/OIDCVerifier is configured both are no-ops.
	s.handler = otelMiddleware(s.oidcMiddleware(s.mux))
	s.server = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handler returns the composed http.Handler (useful for tests via httptest).
// It is the mux wrapped with OTEL (outer) and OIDC (inner) middleware.
// Both are transparent no-ops when not configured.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe starts serving and blocks until the server stops.
func (s *Server) ListenAndServe() error { return s.server.ListenAndServe() }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }

// oidcMiddleware returns an http.Handler that enforces OIDC authentication
// before delegating to next. It is a pass-through when s.oidcVerifier is nil
// (OIDC not configured). When active:
//
//  1. Requests carrying the correct X-Purser-Internal-Token header are
//     exempted so the gateway can perform route-sync without a human token.
//  2. All other requests must include a valid Bearer token in the Authorization
//     header; absent, malformed, or invalid tokens yield 401 Unauthorized with
//     a JSON body {"error":"unauthorized","message":"valid OIDC token required"}.
//  3. On a valid token the verified sub and email claims are injected into the
//     request context (keyed by ctxKeyOIDCSub / ctxKeyOIDCEmail) so handlers
//     can log the authenticated actor.
func (s *Server) oidcMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. OIDC disabled — pass through unconditionally.
		if s.oidcVerifier == nil {
			next.ServeHTTP(w, r)
			return
		}
		// 2. Gateway internal-token exemption: the gateway sends route-sync
		// requests with X-Purser-Internal-Token; those must not require a
		// human OIDC token.
		if s.internalToken != "" && r.Header.Get("X-Purser-Internal-Token") == s.internalToken {
			next.ServeHTTP(w, r)
			return
		}
		// 3. Extract Bearer token from the Authorization header.
		rawToken, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(rawToken) == "" {
			s.writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":   "unauthorized",
				"message": "valid OIDC token required",
			})
			return
		}
		// 4. Verify the token via the configured IdP.
		sub, email, err := s.oidcVerifier.VerifyToken(r.Context(), rawToken)
		if err != nil {
			s.log.Debug("OIDC token verification failed", "err", err)
			s.writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":   "unauthorized",
				"message": "valid OIDC token required",
			})
			return
		}
		// 5. Inject verified claims into the request context for downstream
		// handlers to log as the authenticated actor.
		ctx := context.WithValue(r.Context(), ctxKeyOIDCSub, sub)
		ctx = context.WithValue(ctx, ctxKeyOIDCEmail, email)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/nodes", s.handleListNodes)
	s.mux.HandleFunc("GET /api/v1/nodes/{id}", s.handleGetNode)
	s.mux.HandleFunc("POST /api/v1/nodes/{id}/drain", s.handleDrainNode)
	s.mux.HandleFunc("POST /api/v1/nodes/{id}/restart", s.handleRestartNode)
	s.mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.handleDeleteNode)
	s.mux.HandleFunc("GET /api/v1/models", s.handleListModels)
	s.mux.HandleFunc("POST /api/v1/models", s.handleCreateModel)
	s.mux.HandleFunc("POST /api/v1/models/import", s.handleImportModel)
	s.mux.HandleFunc("DELETE /api/v1/models/{id}", s.handleDeleteModel)
	s.mux.HandleFunc("GET /api/v1/models/{id}/health", s.handleModelHealth)
	s.mux.HandleFunc("POST /api/v1/models/{id}/plan", s.handlePreviewPlan)
	s.mux.HandleFunc("POST /api/v1/models/{id}/deploy", s.handleDeployModel)
	s.mux.HandleFunc("POST /api/v1/join-token", s.handleJoinToken)
	s.mux.HandleFunc("GET /api/v1/enrollment-bundle", s.handleEnrollmentBundle)
	s.mux.HandleFunc("GET /api/v1/deployments", s.handleListDeployments)
	s.mux.HandleFunc("DELETE /api/v1/deployments/{id}", s.handleDeleteDeployment)
	s.mux.HandleFunc("GET /api/v1/plans/{id}", s.handleGetPlan)
	s.mux.HandleFunc("GET /api/v1/cluster/health", s.handleClusterHealth)
	s.mux.HandleFunc("POST /api/v1/apikeys", s.handleCreateAPIKey)
	s.mux.HandleFunc("GET /api/v1/apikeys", s.handleListAPIKeys)
	s.mux.HandleFunc("DELETE /api/v1/apikeys/{id}", s.handleDeleteAPIKey)
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
}

// featureAudit is the entitlement required by the tamper-evident audit log
// (see LICENSING.md, "Compliance").
const featureAudit = "audit"

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
		Actor: "api", Action: "api.node.restart", Target: id,
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
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "read body: "+err.Error())
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
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: "api", Action: "model.created", Target: id})
	s.writeJSON(w, http.StatusCreated, map[string]any{"model_id": id})
}

// importRequest is the body of POST /api/v1/models/import.
type importRequest struct {
	Source          string `json:"source"`
	Repo            string `json:"repo"`
	Revision        string `json:"revision"`
	FilenamePattern string `json:"filename_pattern"`
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

// handleImportModel auto-populates a Model spec from HuggingFace Hub metadata
// and registers it in the catalog.
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
//   - Model already exists → 409 model_exists (mirrors handleCreateModel)
func (s *Server) handleImportModel(w http.ResponseWriter, r *http.Request) {
	var body importRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if body.Source != "huggingface" {
		s.writeError(w, http.StatusBadRequest, "unsupported_source",
			"unsupported import source: "+body.Source+"; supported: huggingface")
		return
	}
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
		Actor:  "api",
		Action: "model.imported",
		Target: modelID,
	})
	s.writeJSON(w, http.StatusCreated, m)
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
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: "api", Action: "model.deleted", Target: id})
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

// handleListDeployments returns all deployments.
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.reg.ListDeployments(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list_deployments_failed", err.Error())
		return
	}
	if deps == nil {
		deps = []*registry.Deployment{}
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
		Quota:   body.Quota,
		Enabled: true,
	}
	if err := s.reg.CreateAPIKey(r.Context(), key); err != nil {
		s.writeError(w, http.StatusInternalServerError, "create_apikey_failed", err.Error())
		return
	}
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: "api", Action: "apikey.created", Target: id})
	// Return the plaintext key ONCE.
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id":     id,
		"name":   body.Name,
		"tenant": body.Tenant,
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
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: "api", Action: "apikey.deleted", Target: id})
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
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: "api", Action: "join_token.minted", Target: s.clusterID})
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
	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{Actor: "api", Action: "enrollment_bundle.created", Target: s.clusterID})

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
func (s *Server) handleRecordUsage(w http.ResponseWriter, r *http.Request) {
	if s.internalToken != "" {
		if tok := r.Header.Get("X-Purser-Internal-Token"); tok != s.internalToken {
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
// configured). The goroutine exits when ctx is cancelled.
func (s *Server) StartInfraMetrics(ctx context.Context) {
	go func() {
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
}
