//! Purser API Gateway binary.
//!
//! Resolves an **explicit** bind configuration from the environment (see
//! [`purser_gateway::config`]), loads the auth policy, quota thresholds and
//! upstream timeouts, installs the Prometheus recorder, and serves the
//! OpenAI-compatible API. The routing table starts **empty**: the Control Plane
//! populates it at runtime via `PUT /api/v1/routes`. The gateway serves
//! plaintext HTTP; TLS is terminated upstream at the ingress / load balancer,
//! consistent with Purser's trusted-LAN model.

use std::process::ExitCode;

use purser_gateway::auth::AuthConfig;
use purser_gateway::quota::QuotaConfig;
use purser_gateway::state::ModelRegistry;
use purser_gateway::upstream::HttpClient;
use purser_gateway::{app, AppState, Config};

#[tokio::main]
async fn main() -> ExitCode {
    // Initialise structured logging and, when OTEL_EXPORTER_OTLP_ENDPOINT is
    // set, the OTLP trace pipeline. The guard must be kept alive until we exit
    // so pending spans are flushed on shutdown.
    let _otel_guard = purser_gateway::telemetry::init();

    let config = match Config::from_env() {
        Ok(config) => config,
        Err(err) => {
            eprintln!(
                "purser-gateway: configuration error: {err}\n\
                 set {host} (e.g. 0.0.0.0) and {port} (e.g. 8080).",
                host = purser_gateway::config::ENV_HOST,
                port = purser_gateway::config::ENV_PORT,
            );
            return ExitCode::from(2);
        }
    };

    let auth = AuthConfig::from_env();
    let quota = QuotaConfig::from_env();
    let http = HttpClient::from_env();

    if auth.configured_keys() == 0 {
        tracing::warn!(
            "no API keys configured (PURSER_GATEWAY_API_KEYS unset): running in OPEN DEV MODE, \
             accepting any non-empty bearer token"
        );
    }
    if auth.internal_token.is_none() {
        tracing::warn!(
            "no management token configured (PURSER_GATEWAY_INTERNAL_TOKEN unset): route-sync \
             (PUT/DELETE /api/v1/routes) is DISABLED"
        );
    }

    // The routing table is populated by the Control Plane at runtime.
    let mut state = AppState::from_parts(ModelRegistry::new(), auth, quota, http);
    if let Ok(cp_url) = std::env::var(purser_gateway::config::ENV_CONTROL_PLANE_URL) {
        let cp_url = cp_url.trim().to_string();
        if !cp_url.is_empty() {
            tracing::info!(url = %cp_url, "usage reporting enabled: will POST to control plane");
            state = state.with_control_plane_url(cp_url);
        }
    }
    let router = app(state);

    let addr = config.socket_addr();
    let listener = match tokio::net::TcpListener::bind(addr).await {
        Ok(listener) => listener,
        Err(err) => {
            eprintln!("purser-gateway: failed to bind {addr}: {err}");
            return ExitCode::FAILURE;
        }
    };

    println!(
        "purser-gateway serving plaintext HTTP on {addr}; \
         terminate TLS at your ingress / load balancer"
    );

    if let Err(err) = axum::serve(listener, router).await {
        eprintln!("purser-gateway: server error: {err}");
        return ExitCode::FAILURE;
    }

    ExitCode::SUCCESS
}
