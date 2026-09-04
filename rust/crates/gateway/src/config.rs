//! Gateway configuration.
//!
//! The bind address and port are **explicit** — there are no magic defaults.
//! `Config::from_env` fails loudly when they are missing, so a misconfigured
//! deployment never silently listens on an unexpected socket.
//!
//! TLS is *structurally* ready: [`TlsConfig`] captures the certificate/key
//! paths and is threaded through [`Config`]. Actually terminating TLS is a
//! phase-2 concern; for now the presence of a [`TlsConfig`] is only surfaced
//! in the startup log.

use std::net::{IpAddr, SocketAddr};

/// Environment variable holding the bind IP address (e.g. `0.0.0.0`).
pub const ENV_HOST: &str = "PURSER_GATEWAY_HOST";
/// Environment variable holding the bind TCP port (e.g. `8080`).
pub const ENV_PORT: &str = "PURSER_GATEWAY_PORT";
/// Optional: path to the TLS certificate chain (PEM).
pub const ENV_TLS_CERT: &str = "PURSER_GATEWAY_TLS_CERT";
/// Optional: path to the TLS private key (PEM).
pub const ENV_TLS_KEY: &str = "PURSER_GATEWAY_TLS_KEY";

/// TLS material. Wiring an actual TLS acceptor is deferred to phase 2; this
/// type exists so the rest of the code can be written TLS-aware today.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TlsConfig {
    pub cert_path: String,
    pub key_path: String,
}

/// Fully-resolved gateway configuration.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Config {
    pub host: IpAddr,
    pub port: u16,
    pub tls: Option<TlsConfig>,
}

impl Config {
    /// Build a config from explicit host/port values.
    pub fn new(host: IpAddr, port: u16) -> Self {
        Self {
            host,
            port,
            tls: None,
        }
    }

    /// The socket address the server should bind to.
    pub fn socket_addr(&self) -> SocketAddr {
        SocketAddr::new(self.host, self.port)
    }

    /// Whether TLS material was configured.
    pub fn tls_enabled(&self) -> bool {
        self.tls.is_some()
    }

    /// Resolve configuration from the environment.
    ///
    /// Both [`ENV_HOST`] and [`ENV_PORT`] are **required** — there are no
    /// implicit defaults. TLS is enabled only when *both* [`ENV_TLS_CERT`]
    /// and [`ENV_TLS_KEY`] are present.
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

        let tls = match (std::env::var(ENV_TLS_CERT), std::env::var(ENV_TLS_KEY)) {
            (Ok(cert_path), Ok(key_path)) => Some(TlsConfig {
                cert_path,
                key_path,
            }),
            _ => None,
        };

        Ok(Self { host, port, tls })
    }
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
