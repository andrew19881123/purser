// Command controlplane is the Purser control plane entrypoint.
//
// It opens the SQLite-backed Registry, initializes the internal PKI, and hosts
// the control-plane subsystems: the management REST API (/api/v1), the
// RegistrationService gRPC server (Join/Heartbeat from Agents), the
// Orchestration Controller and the Reconciler control loop. The Planner and
// Gateway are separate processes; the orchestrator notifies the Gateway over
// HTTP when deployments change.
//
// Subcommands:
//
//	control-plane backup  --db <src>  --output <dst>
//	control-plane restore --input <src> --db <dst> --confirm
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
	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/planning"
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

	reg, err := registry.Open(cfg.dbPath)
	if err != nil {
		return err
	}
	defer reg.Close()

	migCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := reg.Migrate(migCtx); err != nil {
		return err
	}
	logger.Info("registry ready", "db", cfg.dbPath)

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

	// Orchestrator commands agents over gRPC.
	// Use the internal CA pool so agent server certificates are verified.
	// Falls back to insecure if PKI is absent (dev mode).
	agentClient := orchestrator.NewGRPCAgentClientWithCA(ca.CertPool(), logger)
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
	srv := server.New(reg, server.Config{
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
	})

	// Start the background OTEL infrastructure metrics collector (nodes ready/
	// total, active deployments). It exits when ctx is cancelled.
	srv.StartInfraMetrics(ctx)

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
		_ = agentClient.Close()
		// Flush and close OTEL exporters before exiting so the last spans and
		// metrics are not lost.
		_ = otelShutdown(shutdownCtx)
		return srv.Shutdown(shutdownCtx)
	}
}
