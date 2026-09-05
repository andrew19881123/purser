// Command controlplane is the Purser control plane entrypoint.
//
// It opens the SQLite-backed Registry, initializes the internal PKI, and hosts
// the control-plane subsystems: the management REST API (/api/v1), the
// RegistrationService gRPC server (Join/Heartbeat from Agents), the
// Orchestration Controller and the Reconciler control loop. The Planner and
// Gateway are separate processes; the orchestrator notifies the Gateway over
// HTTP when deployments change.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/purser/purser/enterprise/license"
	"github.com/purser/purser/go/controlplane/fleet"
	"github.com/purser/purser/go/controlplane/orchestrator"
	"github.com/purser/purser/go/controlplane/pki"
	"github.com/purser/purser/go/controlplane/planning"
	"github.com/purser/purser/go/controlplane/reconciler"
	"github.com/purser/purser/go/controlplane/registry"
	"github.com/purser/purser/go/controlplane/server"
	"github.com/purser/purser/go/controlplane/telemetry"
	purserv1 "github.com/purser/purser/go/gen/purser/v1"
	"google.golang.org/grpc"
)

// config holds runtime configuration, resolved from flags with env fallbacks.
type config struct {
	dbPath       string
	addr         string
	grpcAddr     string
	pkiDir       string
	gatewayAddr  string
	gatewayToken string
	clusterID    string
	agentPort    int
}

func loadConfig() config {
	c := config{
		dbPath:       envOr("PURSER_DB", "purser-registry.db"),
		addr:         envOr("PURSER_ADDR", ":8080"),
		grpcAddr:     envOr("PURSER_GRPC_ADDR", ":9443"),
		pkiDir:       envOr("PURSER_PKI_DIR", "pki-state"),
		gatewayAddr:  envOr("PURSER_GATEWAY_ADDR", ""),
		gatewayToken: envOr("PURSER_GATEWAY_TOKEN", ""),
		clusterID:    envOr("PURSER_CLUSTER_ID", "default"),
		agentPort:    envInt("PURSER_AGENT_PORT", 0),
	}
	flag.StringVar(&c.dbPath, "db", c.dbPath, "path to the SQLite registry file (env PURSER_DB)")
	flag.StringVar(&c.addr, "addr", c.addr, "management API listen address (env PURSER_ADDR)")
	flag.StringVar(&c.grpcAddr, "grpc-addr", c.grpcAddr, "RegistrationService gRPC listen address (env PURSER_GRPC_ADDR)")
	flag.StringVar(&c.pkiDir, "pki-dir", c.pkiDir, "directory for CA key/cert persistence (env PURSER_PKI_DIR)")
	flag.StringVar(&c.gatewayAddr, "gateway-addr", c.gatewayAddr, "Gateway base URL for route sync (env PURSER_GATEWAY_ADDR)")
	flag.StringVar(&c.gatewayToken, "gateway-token", c.gatewayToken, "shared secret for Gateway route sync (env PURSER_GATEWAY_TOKEN)")
	flag.StringVar(&c.clusterID, "cluster-id", c.clusterID, "cluster identifier echoed in join tokens (env PURSER_CLUSTER_ID)")
	flag.IntVar(&c.agentPort, "agent-port", c.agentPort, "AgentService port the orchestrator dials on each node; 0 = default 50151 (env PURSER_AGENT_PORT)")
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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("control plane exited", "err", err)
		os.Exit(1)
	}
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
	agentClient := orchestrator.NewGRPCAgentClient()
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
	rc := reconciler.New(reg, reconciler.NewOrchestratorActuator(orch, reg), reconciler.DefaultConfig())
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

	// Management HTTP API. The Planner turns fleet state into DeploymentPlans
	// for plan-less deploys and the /models fit verdicts.
	srv := server.New(reg, server.Config{
		Addr:      cfg.addr,
		Logger:    logger,
		Deployer:  orch,
		Metrics:   regServer.Metrics(),
		Planner:   planning.New(reg),
		Fleet:     mgr,
		ClusterID: cfg.clusterID,
		License:   lic,
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
