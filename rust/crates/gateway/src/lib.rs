//! Purser API Gateway — the cluster's single front door.
//!
//! The gateway exposes a distributed Purser cluster as if it were **one**
//! OpenAI-compatible inference server (transparency principle): clients don't
//! know — and don't need to know — that a multi-node pipeline sits behind the
//! endpoint. It authenticates, resolves the requested model to its deployment
//! host, and streams tokens back with minimal buffering.
//!
//! The gateway **reverse-proxies** real inference: it resolves the requested
//! model to its deployment host and streams the host's tokens (SSE) back to the
//! client with minimal buffering. It authenticates API keys, enforces per-tenant
//! quota/rate-limits with backpressure, and exposes Prometheus metrics. The
//! Control Plane keeps the routing table fresh out-of-band via the management
//! plane (`PUT/DELETE /api/v1/routes`).
//!
//! ## Layout
//! - [`config`] — explicit bind config, TLS-ready.
//! - [`state`] — shared `model -> route` table plus auth/quota/http/metrics.
//! - [`openai`] — OpenAI-compatible wire types.
//! - [`error`] — reusable OpenAI-style error mapping.
//! - [`auth`] — client `Bearer` auth + management `X-Purser-Internal-Token`.
//! - [`quota`] — per-tenant token bucket, concurrency and backpressure.
//! - [`upstream`] — outbound proxy client to deployment hosts.
//! - [`metrics`] — Prometheus recorder + `/metrics` endpoint.
//! - [`routes`] — the `/v1` (inference) and `/api/v1` (management) planes.

pub mod auth;
pub mod config;
pub mod error;
pub mod metrics;
pub mod openai;
pub mod quota;
pub mod routes;
pub mod state;
pub mod upstream;

pub use auth::{ApiKey, ApiKeyInfo, AuthConfig, ControlPlaneAuth};
pub use config::{Config, ConfigError};
pub use error::ApiError;
pub use quota::{Limiter, QuotaConfig};
pub use routes::app;
pub use state::{AppState, ModelRoute, RouteState, MOCK_MODEL};
pub use upstream::HttpClient;
