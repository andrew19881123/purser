//! Factory for a [`reqwest::Client`] pre-configured with proxy and custom CA
//! settings for outbound calls made by the gateway (upstream inference hosts,
//! control-plane usage callbacks).

use std::time::Duration;

use crate::config::Config;

/// Build a [`reqwest::Client`] that honours the proxy and CA-bundle settings
/// carried in `config`, with the supplied `connect_timeout`.
///
/// * `PURSER_GATEWAY_HTTPS_PROXY` — HTTPS proxy; applied first when set.
/// * `PURSER_GATEWAY_HTTP_PROXY`  — fallback proxy for all schemes.
/// * `PURSER_GATEWAY_NO_PROXY`    — comma-separated bypass list.
/// * `PURSER_GATEWAY_CA_BUNDLE`   — path to a PEM file with extra trusted CAs.
pub fn build_http_client(
    config: &Config,
    connect_timeout: Duration,
) -> anyhow::Result<reqwest::Client> {
    let mut builder = reqwest::Client::builder().connect_timeout(connect_timeout);

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
    use std::net::IpAddr;

    fn default_config() -> Config {
        Config::new("127.0.0.1".parse::<IpAddr>().unwrap(), 8080)
    }

    #[test]
    fn builds_client_without_proxy() {
        let cfg = default_config();
        let result = build_http_client(&cfg, Duration::from_millis(2000));
        assert!(result.is_ok(), "default config must produce a valid client");
    }

    #[test]
    fn builds_client_with_invalid_proxy_url_returns_error() {
        let mut cfg = default_config();
        // An empty string is always an invalid proxy URL and reliably causes
        // reqwest to return an error regardless of version-specific leniency.
        cfg.https_proxy = Some(String::new());
        let result = build_http_client(&cfg, Duration::from_millis(2000));
        assert!(result.is_err(), "empty proxy URL must produce an error");
    }

    #[test]
    fn builds_client_with_nonexistent_ca_bundle_returns_error() {
        let mut cfg = default_config();
        cfg.ca_bundle_path = Some("/nonexistent/ca.pem".to_string());
        let result = build_http_client(&cfg, Duration::from_millis(2000));
        assert!(
            result.is_err(),
            "non-existent CA bundle path must produce an error"
        );
    }

    #[test]
    fn builds_client_with_http_proxy_fallback() {
        let mut cfg = default_config();
        cfg.http_proxy = Some("http://proxy.internal:3128".to_string());
        let result = build_http_client(&cfg, Duration::from_millis(2000));
        assert!(
            result.is_ok(),
            "valid HTTP proxy URL must produce a valid client"
        );
    }
}
