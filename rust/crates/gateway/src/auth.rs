//! API-key authentication and Control-Plane (management-plane) authorization.
//!
//! Two independent gates live here:
//!
//! * [`ApiKey`] — the **client** gate. Callers authenticate with
//!   `Authorization: Bearer <key>`, exactly like the OpenAI API. Putting the
//!   extractor in a handler's signature *is* the auth gate: a missing/invalid
//!   key short-circuits with `401` before the handler body runs. A validated
//!   key resolves to a stable **api-key id** and a **tenant**, which drive
//!   quota accounting and multi-tenant isolation.
//! * [`ControlPlaneAuth`] — the **management** gate for `/api/v1/routes`. The
//!   Control Plane (a separate Go process) presents a shared secret in the
//!   `X-Purser-Internal-Token` header. Missing → `401`, wrong → `403`.
//!
//! Keys are held in [`AuthConfig`], which the gateway loads from the
//! environment at startup ([`AuthConfig::from_env`]) or receives explicitly in
//! tests. When no keys are configured the gateway runs in **open dev mode**
//! (any non-empty bearer is accepted and mapped to the `default` tenant); once
//! any key is configured, validation is strict.

use std::collections::HashMap;
use std::hash::{Hash, Hasher};

use axum::extract::{FromRef, FromRequestParts};
use axum::http::header::AUTHORIZATION;
use axum::http::request::Parts;
use axum::http::HeaderName;

use crate::error::ApiError;
use crate::state::AppState;

/// Environment variable holding the Control-Plane shared secret.
pub const ENV_INTERNAL_TOKEN: &str = "PURSER_GATEWAY_INTERNAL_TOKEN";
/// Environment variable holding the client API keys. Comma-separated entries,
/// each `secret[:tenant[:key_id]]`. Example: `sk-abc:team-a,sk-def:team-b:k2`.
pub const ENV_API_KEYS: &str = "PURSER_GATEWAY_API_KEYS";

/// Header the Control Plane uses to authenticate management-plane calls.
static INTERNAL_TOKEN_HEADER: HeaderName = HeaderName::from_static("x-purser-internal-token");

/// Resolved metadata for a configured API key.
#[derive(Debug, Clone)]
pub struct ApiKeyInfo {
    /// Stable, non-secret identifier safe to log and use as a metrics label.
    pub id: String,
    /// Tenant/team the key belongs to. Drives isolation and quota buckets.
    pub tenant: String,
}

/// The authenticated caller, produced by the [`ApiKey`] extractor.
///
/// Carries only non-secret identifiers — never the raw key — so it is safe to
/// thread through logs and metrics.
#[derive(Debug, Clone)]
pub struct ApiKey {
    /// Stable key id (from config, or synthesized in dev mode).
    pub id: String,
    /// Tenant/team this request is attributed to.
    pub tenant: String,
}

/// Authentication policy: the set of accepted keys plus the management secret.
#[derive(Debug, Clone, Default)]
pub struct AuthConfig {
    /// `secret -> info`. Empty ⇒ open dev mode (see [`AuthConfig::allow_any`]).
    keys: HashMap<String, ApiKeyInfo>,
    /// When `true`, any non-empty bearer is accepted (dev mode). Set
    /// automatically when `keys` is empty.
    allow_any: bool,
    /// Shared secret for the management plane. `None` ⇒ management writes are
    /// disabled (fail closed).
    pub internal_token: Option<String>,
}

impl AuthConfig {
    /// Strict policy over an explicit key set (used in production and tests).
    pub fn strict(keys: HashMap<String, ApiKeyInfo>, internal_token: Option<String>) -> Self {
        Self {
            keys,
            allow_any: false,
            internal_token,
        }
    }

    /// Open dev policy: accept any non-empty bearer as the `default` tenant.
    pub fn allow_any_dev(internal_token: Option<String>) -> Self {
        Self {
            keys: HashMap::new(),
            allow_any: true,
            internal_token,
        }
    }

    /// Load the policy from the environment (see [`ENV_API_KEYS`],
    /// [`ENV_INTERNAL_TOKEN`]). No keys configured ⇒ open dev mode.
    pub fn from_env() -> Self {
        let internal_token = std::env::var(ENV_INTERNAL_TOKEN)
            .ok()
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty());

        let raw = std::env::var(ENV_API_KEYS).unwrap_or_default();
        let mut keys = HashMap::new();
        for entry in raw.split(',').map(str::trim).filter(|s| !s.is_empty()) {
            let mut parts = entry.split(':');
            let secret = parts.next().unwrap_or("").trim().to_string();
            if secret.is_empty() {
                continue;
            }
            let tenant = parts
                .next()
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .unwrap_or("default")
                .to_string();
            let id = parts
                .next()
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .map(String::from)
                .unwrap_or_else(|| synthetic_id(&secret));
            keys.insert(secret, ApiKeyInfo { id, tenant });
        }

        let allow_any = keys.is_empty();
        Self {
            keys,
            allow_any,
            internal_token,
        }
    }

    /// Number of explicitly-configured keys (0 ⇒ dev mode).
    pub fn configured_keys(&self) -> usize {
        self.keys.len()
    }

    /// Validate a presented bearer token, resolving id + tenant.
    pub fn validate(&self, token: &str) -> Result<ApiKey, ApiError> {
        let token = token.trim();
        if token.is_empty() {
            return Err(ApiError::Unauthorized(
                "Empty API key. Provide a valid key via 'Authorization: Bearer <key>'.".to_string(),
            ));
        }
        if let Some(info) = self.keys.get(token) {
            return Ok(ApiKey {
                id: info.id.clone(),
                tenant: info.tenant.clone(),
            });
        }
        if self.allow_any {
            return Ok(ApiKey {
                id: synthetic_id(token),
                tenant: "default".to_string(),
            });
        }
        Err(ApiError::Unauthorized(
            "Invalid API key. The presented key is not recognized by this cluster.".to_string(),
        ))
    }
}

/// Derive a stable, non-secret id from a key, so logs/metrics never carry the
/// raw secret. Not cryptographic — just a deterministic label.
fn synthetic_id(token: &str) -> String {
    let mut hasher = std::collections::hash_map::DefaultHasher::new();
    token.hash(&mut hasher);
    format!("key-{:016x}", hasher.finish())
}

impl<S> FromRequestParts<S> for ApiKey
where
    AppState: FromRef<S>,
    S: Send + Sync,
{
    type Rejection = ApiError;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        let app = AppState::from_ref(state);

        let header = parts
            .headers
            .get(AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .ok_or_else(|| {
                ApiError::Unauthorized(
                    "Missing Authorization header. Expected 'Authorization: Bearer <api-key>'."
                        .to_string(),
                )
            })?;

        let token = header.strip_prefix("Bearer ").ok_or_else(|| {
            ApiError::Unauthorized(
                "Malformed Authorization header. Expected 'Authorization: Bearer <api-key>'."
                    .to_string(),
            )
        })?;

        app.auth.validate(token)
    }
}

/// Management-plane gate: verifies the `X-Purser-Internal-Token` shared secret.
///
/// Present as a zero-sized extractor so that placing it in a handler signature
/// enforces the check before the body is parsed. Missing header → `401`;
/// wrong value → `403`; no secret configured → `403` (fail closed).
#[derive(Debug, Clone, Copy)]
pub struct ControlPlaneAuth;

impl<S> FromRequestParts<S> for ControlPlaneAuth
where
    AppState: FromRef<S>,
    S: Send + Sync,
{
    type Rejection = ApiError;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        let app = AppState::from_ref(state);

        let expected =
            match app.auth.internal_token.as_deref() {
                Some(t) if !t.is_empty() => t,
                _ => return Err(ApiError::Forbidden(
                    "Management API is disabled: no internal token is configured on the gateway."
                        .to_string(),
                )),
            };

        let provided = parts
            .headers
            .get(&INTERNAL_TOKEN_HEADER)
            .and_then(|value| value.to_str().ok());

        match provided {
            None => Err(ApiError::Unauthorized(
                "Missing X-Purser-Internal-Token header.".to_string(),
            )),
            Some(p) if constant_time_eq(p.as_bytes(), expected.as_bytes()) => Ok(ControlPlaneAuth),
            Some(_) => Err(ApiError::Forbidden(
                "Invalid X-Purser-Internal-Token.".to_string(),
            )),
        }
    }
}

/// Length-independent-ish constant-time comparison to avoid trivially leaking
/// the secret length/prefix via timing. Not a substitute for a real MAC, but
/// appropriate for a shared-secret header check.
fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let mut diff = 0u8;
    for (x, y) in a.iter().zip(b.iter()) {
        diff |= x ^ y;
    }
    diff == 0
}
