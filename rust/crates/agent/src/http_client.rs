//! Factory for a [`reqwest::Client`] pre-configured with proxy and custom CA
//! settings from [`AgentConfig`].
//!
//! Only compiled when the `http-fetch` Cargo feature is enabled, because
//! `reqwest` itself is an optional dependency gated on that feature.
//! The module declaration in `lib.rs` is already gated with
//! `#[cfg(feature = "http-fetch")]`.

use crate::config::AgentConfig;

/// Build a [`reqwest::Client`] that honours the proxy and CA-bundle settings
/// carried in `config`.
///
/// * `PURSER_AGENT_HTTPS_PROXY` — HTTPS proxy; applied first when set.
/// * `PURSER_AGENT_HTTP_PROXY`  — fallback proxy for all schemes.
/// * `PURSER_AGENT_NO_PROXY`    — comma-separated bypass list.
/// * `PURSER_AGENT_CA_BUNDLE`   — path to a PEM file with extra trusted CAs.
pub fn build_http_client(config: &AgentConfig) -> anyhow::Result<reqwest::Client> {
    let mut builder = reqwest::Client::builder().timeout(std::time::Duration::from_secs(30));

    if let Some(proxy_url) = &config.https_proxy {
        let mut proxy = reqwest::Proxy::https(proxy_url)?;
        if let Some(no_proxy_str) = &config.no_proxy {
            proxy = proxy.no_proxy(reqwest::NoProxy::from_string(no_proxy_str));
        }
        builder = builder.proxy(proxy);
    } else if let Some(proxy_url) = &config.http_proxy {
        let mut proxy = reqwest::Proxy::all(proxy_url)?;
        if let Some(no_proxy_str) = &config.no_proxy {
            proxy = proxy.no_proxy(reqwest::NoProxy::from_string(no_proxy_str));
        }
        builder = builder.proxy(proxy);
    }

    if let Some(ca_path) = &config.ca_bundle_path {
        let pem = std::fs::read(ca_path)?;
        let cert = reqwest::Certificate::from_pem(&pem)?;
        builder = builder.add_root_certificate(cert);
    }

    Ok(builder.build()?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn builds_client_without_proxy() {
        let cfg = AgentConfig::default();
        let client = build_http_client(&cfg);
        assert!(client.is_ok(), "default config must produce a valid client");
    }

    #[test]
    fn builds_client_with_invalid_proxy_url_returns_error() {
        // An empty string is always an invalid proxy URL and reliably causes
        // reqwest to return an error regardless of version-specific leniency.
        let cfg = AgentConfig {
            https_proxy: Some(String::new()),
            ..AgentConfig::default()
        };
        let client = build_http_client(&cfg);
        assert!(client.is_err(), "empty proxy URL must produce an error");
    }

    #[test]
    fn builds_client_with_nonexistent_ca_bundle_returns_error() {
        let cfg = AgentConfig {
            ca_bundle_path: Some("/nonexistent/ca.pem".to_string()),
            ..AgentConfig::default()
        };
        let client = build_http_client(&cfg);
        assert!(
            client.is_err(),
            "non-existent CA bundle path must produce an error"
        );
    }

    #[test]
    fn builds_client_with_http_proxy_fallback() {
        let cfg = AgentConfig {
            http_proxy: Some("http://proxy.internal:3128".to_string()),
            ..AgentConfig::default()
        };
        let client = build_http_client(&cfg);
        assert!(
            client.is_ok(),
            "valid HTTP proxy URL must produce a valid client"
        );
    }

    #[test]
    fn https_proxy_takes_precedence_over_http_proxy() {
        let cfg = AgentConfig {
            http_proxy: Some("http://http-proxy.internal:3128".to_string()),
            https_proxy: Some("http://https-proxy.internal:3128".to_string()),
            ..AgentConfig::default()
        };
        let client = build_http_client(&cfg);
        assert!(
            client.is_ok(),
            "https_proxy takes precedence; both set must still build successfully"
        );
    }
}
