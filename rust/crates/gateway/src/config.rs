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

/// Fully-resolved gateway configuration.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Config {
    pub host: IpAddr,
    pub port: u16,
}

impl Config {
    /// Build a config from explicit host/port values.
    pub fn new(host: IpAddr, port: u16) -> Self {
        Self { host, port }
    }

    /// The socket address the server should bind to.
    pub fn socket_addr(&self) -> SocketAddr {
        SocketAddr::new(self.host, self.port)
    }

    /// Resolve configuration from the environment.
    ///
    /// Both [`ENV_HOST`] and [`ENV_PORT`] are **required** — there are no
    /// implicit defaults.
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

        Ok(Self { host, port })
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
