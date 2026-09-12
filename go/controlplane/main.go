// Command controlplane is the Purser control plane entrypoint.
//
// It opens the SQLite-backed Registry, initializes the internal PKI, and hosts
// the control-plane subsystems: the management REST API (/api/v1), the
// RegistrationService gRPC server (Join/Heartbeat from Agents), the
// Orchestration Controller and the Reconciler control loop. The Planner and
// Gateway are separate processes; the orchestrator notifies the Gateway over
// HTTP when deployments change, and the RouteReconciler re-pushes the desired
// route set periodically so a restarted Gateway recovers on its own.
//
// Subcommands:
//
//	control-plane backup       --db <src>  --output <dst>
//	control-plane restore      --input <src> --db <dst> --confirm
//	control-plane pki rotate     --pki-dir <dir> [--db <path>] --confirm
//	control-plane pki revoke-all --pki-dir <dir> [--db <path>] --confirm
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/backup"
	configpkg "github.com/purser/purser/go/controlplane/config"
	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/ldapauth"
	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/planning"
	raftcp "github.com/purser/purser/go/controlplane/raft"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	"github.com/purser/purser/go/controlplane/telemetry"
	"github.com/purser/purser/go/controlplane/transport"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/grpc"
)

// config holds runtime configuration, resolved from flags with env fallbacks.
type config struct {
	dbPath        string
	addr          string
	grpcAddr      string
	pkiDir        string
	gatewayAddr   string
	gatewayToken  string
	clusterID     string
	agentPort     int
	internalToken string
	// hfToken is the HuggingFace API token used by POST /api/v1/models/import
	// when the caller does not supply an X-HF-Token header. Read from
	// PURSER_HF_TOKEN; leave unset for public-model-only access.
	hfToken string

	// TLS configuration for the management REST API.
	// tlsCert / tlsKey are file paths to PEM cert/key (PURSER_TLS_CERT /
	// PURSER_TLS_KEY). tlsAuto, when true, issues a self-signed cert via the
	// internal PKI CA for "localhost" and the machine hostname (PURSER_TLS_AUTO).
	tlsCert string
	tlsKey  string
	tlsAuto bool

	// Rate limiting for the management REST API.
	rateLimitRPS    float64 // PURSER_RATE_LIMIT_RPS    (default 100)
	rateLimitKeyRPS float64 // PURSER_RATE_LIMIT_KEY_RPS (default 50)

	// configPath, when non-empty, names a purser.yaml to apply at startup
	// and to watch for changes (env PURSER_CONFIG). Enables GitOps-style
	// reconciliation: the file is applied once on boot and then polled every
	// configInterval seconds; any change triggers a re-apply automatically.
	configPath string

	// configInterval is how often the watcher polls purser.yaml for changes.
	// Read from PURSER_CONFIG_INTERVAL (seconds); default 30 s.
	configInterval time.Duration

	// routeReconcileInterval is how often the control plane re-pushes the
	// desired route set (all ACTIVE deployments) to the Gateway. The Gateway
	// keeps routes in memory only, so this loop is what restores them after a
	// Gateway restart. Read from PURSER_ROUTE_RECONCILE_INTERVAL (seconds);
	// default 30 s.
	routeReconcileInterval time.Duration

	// Raft HA configuration. All four fields are optional — if raftNodeID is
	// empty the control plane runs in standalone (single-node) mode and the
	// Raft subsystem is not started.
	raftNodeID    string // PURSER_RAFT_NODE_ID
	raftBindAddr  string // PURSER_RAFT_BIND_ADDR
	raftDataDir   string // PURSER_RAFT_DATA_DIR
	raftBootstrap bool   // PURSER_RAFT_BOOTSTRAP
}

func loadConfig() config {
	c := config{
		dbPath:          envOr("PURSER_DB", "purser-registry.db"),
		addr:            envOr("PURSER_ADDR", ":8080"),
		grpcAddr:        envOr("PURSER_GRPC_ADDR", ":9443"),
		pkiDir:          envOr("PURSER_PKI_DIR", "pki-state"),
		gatewayAddr:     envOr("PURSER_GATEWAY_ADDR", ""),
		gatewayToken:    envOr("PURSER_GATEWAY_TOKEN", ""),
		clusterID:       envOr("PURSER_CLUSTER_ID", "default"),
		agentPort:       envInt("PURSER_AGENT_PORT", 0),
		internalToken:   envOr("PURSER_INTERNAL_TOKEN", ""),
		hfToken:         envOr("PURSER_HF_TOKEN", ""),
		tlsCert:         envOr("PURSER_TLS_CERT", ""),
		tlsKey:          envOr("PURSER_TLS_KEY", ""),
		tlsAuto:         envBool("PURSER_TLS_AUTO"),
		rateLimitRPS:    envFloat("PURSER_RATE_LIMIT_RPS", 0),
		rateLimitKeyRPS: envFloat("PURSER_RATE_LIMIT_KEY_RPS", 0),
		configPath:      envOr("PURSER_CONFIG", ""),
		configInterval:  envDuration("PURSER_CONFIG_INTERVAL", 30*time.Second),
		// Self-healing route table: the Gateway holds routes in memory only, so
		// the control plane re-pushes the ACTIVE set periodically.
		routeReconcileInterval: envDuration("PURSER_ROUTE_RECONCILE_INTERVAL", orchestrator.DefaultRouteReconcileInterval),
		// Raft — all optional; single-node mode when raftNodeID is empty.
		raftNodeID:    envOr("PURSER_RAFT_NODE_ID", ""),
		raftBindAddr:  envOr("PURSER_RAFT_BIND_ADDR", ":7000"),
		raftDataDir:   envOr("PURSER_RAFT_DATA_DIR", "raft-data"),
		raftBootstrap: os.Getenv("PURSER_RAFT_BOOTSTRAP") == "true",
	}
	flag.StringVar(&c.dbPath, "db", c.dbPath, "path to the SQLite registry file (env PURSER_DB)")
	flag.StringVar(&c.addr, "addr", c.addr, "management API listen address (env PURSER_ADDR)")
	flag.StringVar(&c.grpcAddr, "grpc-addr", c.grpcAddr, "RegistrationService gRPC listen address (env PURSER_GRPC_ADDR)")
	flag.StringVar(&c.pkiDir, "pki-dir", c.pkiDir, "directory for CA key/cert persistence (env PURSER_PKI_DIR)")
	flag.StringVar(&c.gatewayAddr, "gateway-addr", c.gatewayAddr, "Gateway base URL for route sync (env PURSER_GATEWAY_ADDR)")
	flag.StringVar(&c.gatewayToken, "gateway-token", c.gatewayToken, "shared secret for Gateway route sync (env PURSER_GATEWAY_TOKEN)")
	flag.StringVar(&c.clusterID, "cluster-id", c.clusterID, "cluster identifier echoed in join tokens (env PURSER_CLUSTER_ID)")
	flag.IntVar(&c.agentPort, "agent-port", c.agentPort, "AgentService port the orchestrator dials on each node; 0 = default 50151 (env PURSER_AGENT_PORT)")
	flag.StringVar(&c.internalToken, "internal-token", c.internalToken, "shared secret for gateway usage callbacks (env PURSER_INTERNAL_TOKEN)")
	flag.StringVar(&c.configPath, "config", c.configPath, "path to purser.yaml applied at startup (env PURSER_CONFIG)")
	flag.Parse()
	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "true" || v == "1" || v == "yes"
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// envDuration reads key as an integer number of seconds.
// Falls back to def when the variable is unset or unparsable.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Dispatch backup/restore subcommands before the regular flag parse so their
	// own FlagSet can define --db, --output, --input, and --confirm without
	// conflicting with the server's flag set.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup":
			if err := runBackupCmd(logger, os.Args[2:]); err != nil {
				logger.Error("backup failed", "err", err)
				os.Exit(1)
			}
			return
		case "restore":
			if err := runRestoreCmd(logger, os.Args[2:]); err != nil {
				logger.Error("restore failed", "err", err)
				os.Exit(1)
			}
			return
		case "pki":
			if err := runPKICmd(logger, os.Args[2:]); err != nil {
				logger.Error("pki command failed", "err", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(logger); err != nil {
		logger.Error("control plane exited", "err", err)
		os.Exit(1)
	}
}

// runBackupCmd implements the `backup` subcommand.
//
//	control-plane backup --db /var/lib/purser/registry.db --output /backup/purser-20260906.db
func runBackupCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("PURSER_DB", "purser-registry.db"),
		"path to the source SQLite registry (env PURSER_DB)")
	output := fs.String("output", "", "destination path for the backup file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return fmt.Errorf("--output is required")
	}
	logger.Info("starting backup", "src", *dbPath, "dst", *output)
	if err := backup.BackupDB(*dbPath, *output); err != nil {
		return err
	}
	logger.Info("backup complete", "dst", *output)
	return nil
}

// runRestoreCmd implements the `restore` subcommand.
//
//	control-plane restore --input /backup/purser-20260906.db --db /var/lib/purser/registry.db --confirm
//
// --confirm is required so that the command cannot accidentally overwrite
// a live database without an explicit operator acknowledgement.
func runRestoreCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("PURSER_DB", "purser-registry.db"),
		"destination path for the restored database (env PURSER_DB)")
	input := fs.String("input", "", "path to the backup file to restore from (required)")
	confirm := fs.Bool("confirm", false,
		"required: acknowledge that the current database will be replaced")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("--input is required")
	}
	if !*confirm {
		return fmt.Errorf("--confirm is required: restoring will overwrite %s; pass --confirm to proceed", *dbPath)
	}
	logger.Info("starting restore", "src", *input, "dst", *dbPath)
	if err := backup.RestoreDB(*input, *dbPath); err != nil {
		return err
	}
	logger.Info("restore complete", "dst", *dbPath)
	return nil
}

// runPKICmd implements the `pki` subcommand group.
//
//	control-plane pki rotate     --pki-dir <dir> [--db <path>] --confirm
//	control-plane pki revoke-all --pki-dir <dir> [--db <path>] --confirm
//
// These are destructive, rare, operator-only maintenance actions that
// deliberately live OFF the network rather than behind an HTTP route:
//
//   - Rotation replaces the cluster's trust root; revoke-all invalidates every
//     live agent/gateway certificate. Exposing either over the management API
//     would put a cluster-wide outage one stray request away. As a CLI they
//     require shell + filesystem access to the control-plane data directory,
//     which is already the trust boundary that protects `ca.key`.
//   - The RBAC vocabulary (permissions.go) has no PKI-scoped permission, and
//     inventing one is out of scope for this change; gating an HTTP route on an
//     unrelated permission would be worse than not exposing it.
//   - It mirrors the existing `backup`/`restore` CLI pattern for irreversible
//     operations, including the mandatory `--confirm` acknowledgement.
func runPKICmd(logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pki requires a subcommand: rotate | revoke-all")
	}
	switch args[0] {
	case "rotate":
		return runPKIRotateCmd(logger, args[1:])
	case "revoke-all":
		return runPKIRevokeAllCmd(logger, args[1:])
	default:
		return fmt.Errorf("unknown pki subcommand %q (want rotate | revoke-all)", args[0])
	}
}

// openRegistryAndCA opens the registry (honouring the PURSER_DB_* env vars, with
// an optional SQLite path override) and loads the on-disk CA from pkiDir. The
// caller owns the returned registry and must Close it.
func openRegistryAndCA(ctx context.Context, dbOverride, pkiDir string) (registry.Registry, *pki.Authority, error) {
	dbCfg := registry.DBConfigFromEnv()
	if dbOverride != "" {
		// An explicit --db always names a SQLite file.
		dbCfg.Driver = "sqlite"
		dbCfg.DSN = dbOverride
	}
	reg, err := registry.OpenFromConfig(dbCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open registry: %w", err)
	}
	migCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := reg.Migrate(migCtx); err != nil {
		reg.Close()
		return nil, nil, fmt.Errorf("migrate registry: %w", err)
	}
	ca, err := pki.New(ctx, reg, pki.Options{Dir: pkiDir})
	if err != nil {
		reg.Close()
		return nil, nil, fmt.Errorf("init pki: %w", err)
	}
	return reg, ca, nil
}

// runPKIRotateCmd implements `pki rotate`.
//
// It re-issues the trust root: the previous CA is marked rotated in the
// registry, a fresh CA keypair is generated and written to --pki-dir. Because
// this runs as a separate short-lived process, the in-process 72h dual-trust
// grace window is NOT established — after rotation the control plane must be
// restarted to adopt the new CA and every agent must re-enroll. This is
// expected for a root rotation and is documented in
// website/docs/operations/pki-operations.md.
func runPKIRotateCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("pki rotate", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("PURSER_DB", "purser-registry.db"),
		"SQLite registry path (env PURSER_DB; ignored when PURSER_DB_DRIVER=postgres)")
	pkiDir := fs.String("pki-dir", envOr("PURSER_PKI_DIR", "pki-state"),
		"directory holding the CA key/cert to rotate (env PURSER_PKI_DIR)")
	confirm := fs.Bool("confirm", false,
		"required: acknowledge that the active CA is replaced and every agent must re-enroll")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return fmt.Errorf("--confirm is required: rotation replaces the active CA in %s and forces every agent to re-enroll; pass --confirm to proceed", *pkiDir)
	}

	ctx := context.Background()
	reg, ca, err := openRegistryAndCA(ctx, *dbPath, *pkiDir)
	if err != nil {
		return err
	}
	defer reg.Close()

	oldSerial := "none"
	if before := ca.CACert(); before != nil {
		oldSerial = before.SerialNumber.String()
	}
	newCert, err := ca.Rotate(ctx)
	if err != nil {
		return fmt.Errorf("rotate CA: %w", err)
	}
	logger.Info("CA rotated",
		"old_serial", oldSerial,
		"new_serial", newCert.SerialNumber.String(),
		"pki_dir", *pkiDir)
	logger.Warn("restart the control plane to adopt the new CA, then re-enroll agents",
		"note", "the in-process 72h dual-trust grace period does not survive a CLI rotation")
	return nil
}

// runPKIRevokeAllCmd implements `pki revoke-all`.
//
// It marks every issued leaf certificate revoked in the registry. Revocation is
// checked independently of the trust bundle in VerifyClient, so a running
// control plane rejects the revoked certs immediately — no restart required.
func runPKIRevokeAllCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("pki revoke-all", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("PURSER_DB", "purser-registry.db"),
		"SQLite registry path (env PURSER_DB; ignored when PURSER_DB_DRIVER=postgres)")
	pkiDir := fs.String("pki-dir", envOr("PURSER_PKI_DIR", "pki-state"),
		"CA key/cert directory (env PURSER_PKI_DIR)")
	confirm := fs.Bool("confirm", false,
		"required: acknowledge that every issued agent/gateway certificate is revoked immediately")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return fmt.Errorf("--confirm is required: this revokes every issued agent/gateway certificate immediately; pass --confirm to proceed")
	}

	ctx := context.Background()
	reg, ca, err := openRegistryAndCA(ctx, *dbPath, *pkiDir)
	if err != nil {
		return err
	}
	defer reg.Close()

	n, err := ca.RevokeAll(ctx)
	if err != nil {
		return fmt.Errorf("revoke-all: %w", err)
	}
	logger.Info("revoked all issued leaf certificates", "count", n)
	return nil
}

func run(logger *slog.Logger) error {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// OpenTelemetry — initialise before anything else so that instruments
	// created by the server (and any other subsystem) use the real providers.
	// When OTEL_EXPORTER_OTLP_ENDPOINT is unset this is a no-op (zero overhead).
	otelShutdown, err := telemetry.Init(ctx)
	if err != nil {
		return err
	}

	// Enterprise network support: install a custom transport that respects
	// HTTP_PROXY / HTTPS_PROXY / NO_PROXY and loads a CA bundle from
	// PURSER_CA_BUNDLE (when set). Setting http.DefaultTransport propagates
	// the configuration to all net/http clients in this process that do not
	// supply their own transport, including the OIDC provider and the
	// HuggingFace importer.
	customTransport, err := transport.Default()
	if err != nil {
		return fmt.Errorf("configuring HTTP transport: %w", err)
	}
	http.DefaultTransport = customTransport

	// Database configuration — PURSER_DB_DRIVER selects the backend (sqlite or
	// postgres); PURSER_DB_URL carries the PostgreSQL DSN; PURSER_DB carries the
	// SQLite file path. All three fall back to the per-driver defaults when unset.
	dbCfg := registry.DBConfigFromEnv()
	slog.Info("opening registry database", "driver", dbCfg.Driver)
	reg, err := registry.OpenFromConfig(dbCfg)
	if err != nil {
		return fmt.Errorf("open registry: %w", err)
	}
	defer reg.Close()

	migCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := reg.Migrate(migCtx); err != nil {
		return err
	}
	logger.Info("registry ready", "driver", dbCfg.Driver)

	// Seed built-in platform roles (idempotent — safe to run on every start).
	if err := reg.SeedSystemRoles(migCtx); err != nil {
		slog.Warn("failed to seed system roles", "err", err)
		// Not fatal: system roles may already exist or the registry is read-only.
	} else {
		slog.Info("platform system roles seeded")
	}

	// Raft HA — optional; only started when PURSER_RAFT_NODE_ID is set.
	// When absent the control plane runs in single-node (standalone) mode,
	// which is the default deployment and retains full backward compatibility.
	var raftNode *raftcp.Node
	if cfg.raftNodeID != "" {
		fsm := raftcp.NewFSM(reg, logger)
		raftNode, err = raftcp.NewNode(raftcp.Config{
			NodeID:    cfg.raftNodeID,
			BindAddr:  cfg.raftBindAddr,
			DataDir:   cfg.raftDataDir,
			Bootstrap: cfg.raftBootstrap,
		}, fsm)
		if err != nil {
			return fmt.Errorf("start raft node: %w", err)
		}
		defer raftNode.Shutdown() //nolint:errcheck
		logger.Info("raft node started",
			"node_id", cfg.raftNodeID,
			"bind_addr", cfg.raftBindAddr,
			"data_dir", cfg.raftDataDir,
			"bootstrap", cfg.raftBootstrap)
	} else {
		logger.Info("raft disabled — running in standalone mode (set PURSER_RAFT_NODE_ID to enable HA)")
	}

	// Internal PKI (self-signed CA, persisted under pki-dir).
	ca, err := pki.New(ctx, reg, pki.Options{Dir: cfg.pkiDir})
	if err != nil {
		return err
	}
	logger.Info("pki ready", "dir", cfg.pkiDir)

	// Gateway sync (best-effort; no-op if no gateway configured).
	var gateway orchestrator.GatewaySync = orchestrator.NopGatewaySync{}
	if cfg.gatewayAddr != "" {
		gateway = orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{
			Addr:   cfg.gatewayAddr,
			Token:  cfg.gatewayToken,
			Logger: logger,
		})
	}

	// Route reconciler: the Gateway's routing table is in memory only, so a
	// Gateway restart would otherwise leave it empty and every inference request
	// returning 503 until an operator re-deployed a model. This loop re-pushes
	// the desired set (every ACTIVE deployment) at startup and every
	// routeReconcileInterval, and deletes routes whose model is no longer ACTIVE.
	// It is best-effort: an unreachable Gateway logs a warning and is retried on
	// the next pass, never failing control-plane startup.
	routeRC := orchestrator.NewRouteReconciler(reg, gateway, cfg.routeReconcileInterval, logger)
	go func() {
		if err := routeRC.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("route reconciler stopped", "err", err)
		}
	}()
	logger.Info("route reconciler started", "interval", cfg.routeReconcileInterval, "gateway", cfg.gatewayAddr)

	// Orchestrator commands agents over gRPC.
	// PURSER_AGENT_GRPC_INSECURE=true skips TLS — use only in dev/demo mode
	// where agents serve plain gRPC (no mTLS on their bind port).
	var agentClient orchestrator.AgentClient
	if os.Getenv("PURSER_AGENT_GRPC_INSECURE") == "true" {
		logger.Warn("orchestrator: agent gRPC TLS disabled (PURSER_AGENT_GRPC_INSECURE=true) — dev mode only")
		agentClient = orchestrator.NewGRPCAgentClient()
	} else {
		agentClient = orchestrator.NewGRPCAgentClientWithCA(ca.CertPool(), logger)
	}
	orch := orchestrator.New(reg, orchestrator.Deps{
		Agents:   agentClient,
		Resolver: orchestrator.NewRegistryResolver(reg, cfg.agentPort, 0),
		Gateway:  gateway,
		Config:   orchestrator.Config{Logger: logger},
	})

	// Fleet manager + RegistrationService gRPC server.
	mgr := fleet.New(reg, ca)
	regServer := fleet.NewRegistrationServer(mgr, reg, nil, logger)

	grpcSrv := grpc.NewServer()
	purserv1.RegisterRegistrationServiceServer(grpcSrv, regServer)

	// Reconciler control loop.
	rc := reconciler.New(reg, reconciler.NewOrchestratorActuator(orch, reg), reconciler.ConfigFromEnv())
	rc.SetLogger(logger)
	go func() {
		if err := rc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("reconciler stopped", "err", err)
		}
	}()

	// Enterprise license: read $PURSER_LICENSE_KEY and verify it OFFLINE against
	// the embedded public key (no phone-home). An absent key yields the
	// community license (enterprise features off); a present-but-invalid key is
	// fatal so a misconfigured deployment fails loud instead of silently
	// dropping to community.
	lic, err := license.FromEnv()
	if err != nil {
		return err
	}
	if lic.IsCommunity() {
		logger.Info("license: community edition (enterprise features disabled)")
	} else {
		logger.Info("license: enterprise edition", "licensee", lic.Licensee,
			"features", lic.Features, "valid", lic.ValidAt(time.Now()), "expires", lic.Expires)
	}

	// OIDC authentication for the admin UI and management REST API (optional).
	// Read PURSER_OIDC_ISSUER and PURSER_OIDC_CLIENT_ID from the environment.
	// If either is empty, OIDC is disabled — the community default. When both
	// are set the provider is discovered eagerly so a bad issuer URL fails here
	// at startup with a clear message rather than at the first admin request.
	//
	// Authorization Code Flow + PKCE (browser SSO) is activated when
	// PURSER_OIDC_REDIRECT_URI is also set; PURSER_OIDC_CLIENT_SECRET is
	// optional for confidential clients.
	var oidcCfg *server.OIDCConfig
	var oidcVerifier server.TokenVerifier
	var sessionKey []byte
	if oidcIssuer := os.Getenv("PURSER_OIDC_ISSUER"); oidcIssuer != "" {
		oidcClientID := os.Getenv("PURSER_OIDC_CLIENT_ID")
		if oidcClientID == "" {
			return fmt.Errorf("PURSER_OIDC_ISSUER is set but PURSER_OIDC_CLIENT_ID is empty")
		}
		provider, err := oidc.NewProvider(ctx, oidcIssuer)
		if err != nil {
			return fmt.Errorf("OIDC discovery failed for issuer %s: %w", oidcIssuer, err)
		}
		// provider.Endpoint() returns the IdP's AuthURL and TokenURL from its
		// discovery document — no extra import needed.
		ep := provider.Endpoint()
		oidcCfg = &server.OIDCConfig{
			Issuer:        oidcIssuer,
			ClientID:      oidcClientID,
			ClientSecret:  os.Getenv("PURSER_OIDC_CLIENT_SECRET"),
			RedirectURI:   os.Getenv("PURSER_OIDC_REDIRECT_URI"),
			TokenEndpoint: ep.TokenURL,
		}
		// Load optional group-claim → role mappings from the environment.
		if raw := os.Getenv("PURSER_OIDC_GROUP_MAPPINGS"); raw != "" {
			var m map[string]string
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				return fmt.Errorf("PURSER_OIDC_GROUP_MAPPINGS: invalid JSON: %w", err)
			}
			oidcCfg.GroupMappings = m
			logger.Info("OIDC group-claim mapping enabled", "groups", len(m))
		}
		oidcVerifier = server.NewOIDCVerifierAdapter(
			provider.Verifier(&oidc.Config{ClientID: oidcClientID}),
		)
		logger.Info("OIDC authentication enabled",
			"issuer", oidcIssuer,
			"client_id", oidcClientID,
			"pkce_flow", oidcCfg.RedirectURI != "",
		)

		// Session secret for signing session cookies (Authorization Code Flow).
		// PURSER_SESSION_SECRET must be a 64-character hex string (32 bytes).
		// When unset, an ephemeral random key is generated — sessions expire on
		// process restart. Persist the key for long-lived deployments.
		if secretHex := os.Getenv("PURSER_SESSION_SECRET"); secretHex != "" {
			sessionKey, err = hex.DecodeString(secretHex)
			if err != nil {
				return fmt.Errorf("PURSER_SESSION_SECRET must be hex-encoded: %w", err)
			}
			if len(sessionKey) != 32 {
				return fmt.Errorf("PURSER_SESSION_SECRET must be exactly 32 bytes (64 hex chars), got %d", len(sessionKey))
			}
		} else {
			sessionKey = make([]byte, 32)
			if _, err := rand.Read(sessionKey); err != nil {
				return fmt.Errorf("generate ephemeral session secret: %w", err)
			}
			logger.Warn("PURSER_SESSION_SECRET not set; using ephemeral key (sessions expire on restart)")
		}
	} else {
		logger.Info("OIDC authentication disabled (set PURSER_OIDC_ISSUER to enable)")
	}

	// LDAP authentication (optional — enabled when PURSER_LDAP_URL is set).
	// Read config from environment variables; validate and disable on error so a
	// misconfigured LDAP setup never silently prevents startup.
	ldapCfg := ldapauth.FromEnv()
	if ldapCfg != nil {
		if err := ldapCfg.Validate(); err != nil {
			logger.Warn("LDAP configuration invalid — LDAP disabled", "err", err)
			ldapCfg = nil
		} else {
			logger.Info("LDAP authentication enabled", "url", ldapCfg.URL)
		}
	}

	// TLS setup for the management REST API.
	// Priority: explicit cert/key files > auto mode via internal PKI > plain HTTP.
	var tlsCertPEM, tlsKeyPEM []byte
	tlsCert, tlsKey := cfg.tlsCert, cfg.tlsKey
	if cfg.tlsAuto && tlsCert == "" {
		// Issue a short-lived cert for "localhost" and the machine hostname from
		// the internal PKI CA. The PEM bytes are passed directly to the server so
		// no temporary files need to be written to disk.
		hostname, _ := os.Hostname()
		dnsNames := []string{"localhost"}
		if hostname != "" && hostname != "localhost" {
			dnsNames = append(dnsNames, hostname)
		}
		issued, err := ca.Issue(ctx, pki.CertRequest{
			CommonName: "purser-management-api",
			Role:       "management",
			DNSNames:   dnsNames,
		})
		if err != nil {
			return fmt.Errorf("TLS auto: issue management API cert: %w", err)
		}
		tlsCertPEM = issued.CertPEM
		tlsKeyPEM = issued.KeyPEM
		logger.Info("TLS auto: issued self-signed management API certificate",
			"dns_names", dnsNames)
	}

	// Management HTTP API. The Planner turns fleet state into DeploymentPlans
	// for plan-less deploys and the /models fit verdicts.
	//
	// Guard against the classic "nil concrete type in non-nil interface" Go
	// pitfall: only assign RaftNode when the pointer is actually non-nil so
	// that handleClusterStatus can test s.raftNode == nil to detect standalone
	// mode.
	srvCfg := server.Config{
		Addr:            cfg.addr,
		Logger:          logger,
		Deployer:        orch,
		Metrics:         regServer.Metrics(),
		Planner:         planning.New(reg),
		Fleet:           mgr,
		ClusterID:       cfg.clusterID,
		License:         lic,
		OIDC:            oidcCfg,
		OIDCVerifier:    oidcVerifier,
		InternalToken:   cfg.internalToken,
		HFToken:         cfg.hfToken,
		TLSCert:         tlsCert,
		TLSKey:          tlsKey,
		TLSCertPEM:      tlsCertPEM,
		TLSKeyPEM:       tlsKeyPEM,
		RateLimitRPS:    cfg.rateLimitRPS,
		RateLimitKeyRPS: cfg.rateLimitKeyRPS,
		Reconciler:      rc,
		SessionSecret:   sessionKey,
		LDAPConfig:      ldapCfg,
	}
	if raftNode != nil {
		srvCfg.RaftNode = raftNode
	}
	srv := server.New(reg, srvCfg)

	// Start the background OTEL infrastructure metrics collector (nodes ready/
	// total, active deployments). It exits when ctx is cancelled.
	srv.StartInfraMetrics(ctx)

	// --config / PURSER_CONFIG: apply desired state from a purser.yaml at startup.
	// This is the GitOps-friendly path: store purser.yaml in version control and
	// let the control plane converge to the declared state on every restart.
	if cfg.configPath != "" {
		cc, err := configpkg.LoadFile(cfg.configPath)
		if err != nil {
			logger.Error("startup config: load failed", "path", cfg.configPath, "err", err)
		} else {
			applyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			result, err := srv.ApplyClusterConfig(applyCtx, cc)
			cancel()
			if err != nil {
				logger.Error("startup config: apply failed", "path", cfg.configPath, "err", err)
			} else {
				logger.Info("startup config: applied",
					"path", cfg.configPath,
					"models_added", result.ModelsAdded,
					"deployments_added", result.DeploymentsAdded,
					"quotas_upserted", result.QuotasUpserted,
				)
			}
		}

		// GitOps continuous reconciliation: poll purser.yaml every configInterval
		// and re-apply automatically whenever its content changes. The watcher
		// fires immediately on the first tick so it is safe to skip the startup
		// apply above when refactoring — both paths are idempotent.
		watcher := configpkg.NewWatcher(cfg.configPath, cfg.configInterval, func(c *configpkg.ClusterConfig) {
			wCtx, wCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer wCancel()
			result, err := srv.ApplyClusterConfig(wCtx, c)
			if err != nil {
				logger.Error("config watcher: apply failed", "path", cfg.configPath, "err", err)
				return
			}
			logger.Info("config watcher: applied",
				"path", cfg.configPath,
				"models_added", result.ModelsAdded,
				"deployments_added", result.DeploymentsAdded,
				"quotas_upserted", result.QuotasUpserted,
			)
		})
		go func() {
			if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("config watcher stopped", "path", cfg.configPath, "err", err)
			}
		}()
		logger.Info("config watcher started", "path", cfg.configPath, "interval", cfg.configInterval)
	}

	errCh := make(chan error, 2)
	go func() {
		lis, err := net.Listen("tcp", cfg.grpcAddr)
		if err != nil {
			errCh <- err
			return
		}
		logger.Info("serving RegistrationService", "addr", cfg.grpcAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- err
		}
	}()
	go func() {
		logger.Info("serving management API", "addr", cfg.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		grpcSrv.GracefulStop()
		if c, ok := agentClient.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		// Flush and close OTEL exporters before exiting so the last spans and
		// metrics are not lost.
		_ = otelShutdown(shutdownCtx)
		return srv.Shutdown(shutdownCtx)
	}
}
