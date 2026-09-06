//! Gateway configuration.
//!
//! The bind address and port are **explicit** — there are no magic defaults.
//! `Config::from_env` fails loudly when they are missing, so a misconfigured
//! deployment never silently listens on an unexpected socket.
//!
//! The gateway serves **plaintext HTTP**. TLS is terminated upstream at the
//! ingress / load balancer, consistent with Purser's trusted-LAN model — the
//! gateway process itself never sees certificate or key material.

use std::net::{IpAddr, SocketAddr};

/// Environment variable holding the bind IP address (e.g. `0.0.0.0`).
pub const ENV_HOST: &str = "PURSER_GATEWAY_HOST";
/// Environment variable holding the bind TCP port (e.g. `8080`).
pub const ENV_PORT: &str = "PURSER_GATEWAY_PORT";
/// Environment variable holding the Control-Plane base URL for usage reporting
/// (e.g. `http://control-plane:8080`). When unset, usage recording is skipped
/// (backward-compatible).
pub const ENV_CONTROL_PLANE_URL: &str = "PURSER_CONTROL_PLANE_URL";

/// HTTP proxy for outbound calls from the gateway (e.g. upstream timeouts).
pub const ENV_HTTP_PROXY: &str = "PURSER_GATEWAY_HTTP_PROXY";
/// HTTPS proxy for outbound TLS calls; overrides `HTTP_PROXY` for TLS targets.
pub const ENV_HTTPS_PROXY: &str = "PURSER_GATEWAY_HTTPS_PROXY";
/// Comma-separated proxy bypass list.
pub const ENV_NO_PROXY: &str = "PURSER_GATEWAY_NO_PROXY";
/// Path to a PEM file with additional CA certificates for outbound TLS.
pub const ENV_CA_BUNDLE: &str = "PURSER_GATEWAY_CA_BUNDLE";

/// Fully-resolved gateway configuration.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Config {
    pub host: IpAddr,
    pub port: u16,
    /// Optional HTTP proxy for outbound plain-HTTP traffic.
    pub http_proxy: Option<String>,
    /// Optional HTTPS proxy for outbound TLS traffic.
    pub https_proxy: Option<String>,
    /// Optional proxy bypass list (comma-separated hosts/ranges).
    pub no_proxy: Option<String>,
    /// Optional path to a PEM file with extra trusted CA certificates.
    pub ca_bundle_path: Option<String>,
}

impl Config {
    /// Build a config from explicit host/port values (no proxy/CA).
    pub fn new(host: IpAddr, port: u16) -> Self {
        Self {
            host,
            port,
            http_proxy: None,
            https_proxy: None,
            no_proxy: None,
            ca_bundle_path: None,
        }
    }

    /// The socket address the server should bind to.
    pub fn socket_addr(&self) -> SocketAddr {
        SocketAddr::new(self.host, self.port)
    }

    /// Resolve configuration from the environment.
    ///
    /// [`ENV_HOST`] and [`ENV_PORT`] are **required** — there are no
    /// implicit defaults. Proxy and CA-bundle variables are optional.
    pub fn from_env() -> Result<Self, ConfigError> {
        let host_raw = std::env::var(ENV_HOST).map_err(|_| ConfigError::Missing(ENV_HOST))?;
        let port_raw = std::env::var(ENV_PORT).map_err(|_| ConfigError::Missing(ENV_PORT))?;

        let host: IpAddr = host_raw
            .trim()
            .parse()
            .map_err(|_| ConfigError::Invalid(ENV_HOST, host_raw))?;
        let port: u16 = port_raw
            .trim()
            .parse()
            .map_err(|_| ConfigError::Invalid(ENV_PORT, port_raw))?;

        Ok(Self {
            host,
            port,
            http_proxy: non_empty(std::env::var(ENV_HTTP_PROXY).ok()),
            https_proxy: non_empty(std::env::var(ENV_HTTPS_PROXY).ok()),
            no_proxy: non_empty(std::env::var(ENV_NO_PROXY).ok()),
            ca_bundle_path: non_empty(std::env::var(ENV_CA_BUNDLE).ok()),
        })
    }
}

fn non_empty(v: Option<String>) -> Option<String> {
    v.filter(|s| !s.trim().is_empty())
}

/// Error produced while resolving [`Config`] from the environment.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ConfigError {
    /// A required variable was not set.
    Missing(&'static str),
    /// A variable was set but could not be parsed.
    Invalid(&'static str, String),
}

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ConfigError::Missing(var) => {
                write!(f, "missing required environment variable `{var}`")
            }
            ConfigError::Invalid(var, val) => {
                write!(f, "invalid value `{val}` for environment variable `{var}`")
            }
        }
    }
}

impl std::error::Error for ConfigError {}
