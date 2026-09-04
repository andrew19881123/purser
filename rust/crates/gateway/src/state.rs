//! Shared, mutable gateway state.
//!
//! The heart of the gateway is the `model -> route` table: given the `model`
//! field of an inference request, the gateway resolves which deployment **host**
//! (the pipeline coordinator) to forward to. The Control Plane keeps this table
//! fresh out-of-band via the management plane (`PUT/DELETE /api/v1/routes`), so
//! it lives behind an `Arc<RwLock<..>>`: cheap to clone into every handler,
//! cheap to read concurrently, writable from the route-sync endpoints.
//!
//! [`AppState`] also carries the auth policy, quota limiter, upstream HTTP
//! client and the Prometheus render handle — everything a request handler needs.

use std::collections::BTreeMap;
use std::sync::Arc;

use metrics_exporter_prometheus::PrometheusHandle;
use tokio::sync::RwLock;

use crate::auth::AuthConfig;
use crate::error::ApiError;
use crate::metrics::prometheus_handle;
use crate::quota::{Limiter, QuotaConfig};
use crate::upstream::HttpClient;

/// A mock model id pre-loaded by [`AppState::with_mock`], used by tests until a
/// real deployment host is registered.
pub const MOCK_MODEL: &str = "purser-mock-7b";

/// Owner label surfaced in `GET /v1/models` (`owned_by`). The route-sync
/// contract does not carry an owner, so the cluster reports itself.
pub const OWNED_BY: &str = "purser";

/// Lifecycle of a served route, mirroring the Control Plane's view.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RouteState {
    /// Accepting new requests.
    Active,
    /// Finishing in-flight work; excluded from new routing and `/v1/models`.
    Draining,
}

/// Where and how a model is served.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ModelRoute {
    /// Base URL of the deployment host, e.g. `http://10.0.0.4:8080`.
    pub endpoint: String,
    /// Opaque deployment id (for correlation/observability).
    pub deployment_id: String,
    /// Quantization label of the served weights (e.g. `Q4_K_M`).
    pub quantization: String,
    /// Lifecycle state.
    pub state: RouteState,
}

impl ModelRoute {
    /// Construct an [`RouteState::Active`] route.
    pub fn active(
        endpoint: impl Into<String>,
        deployment_id: impl Into<String>,
        quantization: impl Into<String>,
    ) -> Self {
        Self {
            endpoint: endpoint.into(),
            deployment_id: deployment_id.into(),
            quantization: quantization.into(),
            state: RouteState::Active,
        }
    }

    /// Construct a [`RouteState::Draining`] route.
    pub fn draining(
        endpoint: impl Into<String>,
        deployment_id: impl Into<String>,
        quantization: impl Into<String>,
    ) -> Self {
        Self {
            state: RouteState::Draining,
            ..Self::active(endpoint, deployment_id, quantization)
        }
    }

    pub fn is_active(&self) -> bool {
        self.state == RouteState::Active
    }
}

/// `model id -> route`. `BTreeMap` keeps `/v1/models` output deterministic.
pub type ModelRegistry = BTreeMap<String, ModelRoute>;

/// Application state shared by every handler (cloned per request — cheap: `Arc`
/// bumps plus a `reqwest::Client`/`PrometheusHandle` clone, both `Arc`-backed).
#[derive(Clone)]
pub struct AppState {
    /// Live `model -> route` routing table.
    pub models: Arc<RwLock<ModelRegistry>>,
    /// Authentication policy (client keys + management secret).
    pub auth: Arc<AuthConfig>,
    /// Quota/rate-limit thresholds.
    pub quota: Arc<QuotaConfig>,
    /// Runtime rate-limiter/backpressure state.
    pub limiter: Arc<Limiter>,
    /// Outbound client to deployment hosts.
    pub http: HttpClient,
    /// Prometheus render handle for `GET /metrics`.
    pub metrics: PrometheusHandle,
}

impl AppState {
    /// Assemble state from its parts (used by `main` and builder helpers).
    pub fn from_parts(
        models: ModelRegistry,
        auth: AuthConfig,
        quota: QuotaConfig,
        http: HttpClient,
    ) -> Self {
        Self {
            models: Arc::new(RwLock::new(models)),
            auth: Arc::new(auth),
            quota: Arc::new(quota),
            limiter: Arc::new(Limiter::new()),
            http,
            metrics: prometheus_handle(),
        }
    }

    /// Empty state — no models served, open dev auth, no management secret.
    pub fn new() -> Self {
        Self::from_parts(
            ModelRegistry::new(),
            AuthConfig::allow_any_dev(None),
            QuotaConfig::default(),
            HttpClient::default_local(),
        )
    }

    /// State pre-seeded with one active mock route and a test management token,
    /// so tests have something to route to and can exercise the route-sync API.
    pub fn with_mock() -> Self {
        let mut registry = ModelRegistry::new();
        // Points at a closed port so a bare proxy attempt surfaces as 503
        // unless a test overrides it with a live mock host.
        registry.insert(
            MOCK_MODEL.to_string(),
            ModelRoute::active("http://127.0.0.1:9", "mock-deployment", "Q4_K_M"),
        );
        Self::from_parts(
            registry,
            AuthConfig::allow_any_dev(Some("test-internal-token".to_string())),
            QuotaConfig::default(),
            HttpClient::default_local(),
        )
    }

    /// Override the quota thresholds (builder style, for tests).
    pub fn with_quota(mut self, quota: QuotaConfig) -> Self {
        self.quota = Arc::new(quota);
        self
    }

    /// Override the auth policy (builder style, for tests).
    pub fn with_auth(mut self, auth: AuthConfig) -> Self {
        self.auth = Arc::new(auth);
        self
    }

    /// Insert/replace a route (used by the management plane and tests).
    pub async fn insert_route(&self, model_id: impl Into<String>, route: ModelRoute) {
        self.models.write().await.insert(model_id.into(), route);
    }

    /// Remove a route; returns whether one existed.
    pub async fn remove_route(&self, model_id: &str) -> bool {
        self.models.write().await.remove(model_id).is_some()
    }

    /// Resolve an **active** route for `model`.
    ///
    /// * unknown model → `404` with the served-model list;
    /// * known but draining → `503` (transient, retry).
    pub async fn resolve_active(&self, model: &str) -> Result<ModelRoute, ApiError> {
        let registry = self.models.read().await;
        match registry.get(model) {
            Some(route) if route.is_active() => Ok(route.clone()),
            Some(_) => Err(ApiError::NodeUnavailable(format!(
                "The model '{model}' is draining and not accepting new requests; retry shortly."
            ))),
            None => Err(ApiError::model_not_found(
                model,
                registry
                    .iter()
                    .filter(|(_, r)| r.is_active())
                    .map(|(id, _)| id.clone())
                    .collect(),
            )),
        }
    }

    /// Ids of all currently-**active** models (sorted), for `GET /v1/models`.
    pub async fn active_model_ids(&self) -> Vec<String> {
        self.models
            .read()
            .await
            .iter()
            .filter(|(_, r)| r.is_active())
            .map(|(id, _)| id.clone())
            .collect()
    }

    /// All model ids in the table (sorted), regardless of state.
    pub async fn model_ids(&self) -> Vec<String> {
        self.models.read().await.keys().cloned().collect()
    }

    /// `(active, total)` route counts, for `GET /readyz`.
    pub async fn route_counts(&self) -> (usize, usize) {
        let registry = self.models.read().await;
        let active = registry.values().filter(|r| r.is_active()).count();
        (active, registry.len())
    }
}

impl Default for AppState {
    fn default() -> Self {
        Self::new()
    }
}
