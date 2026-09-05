-- Purser control-plane Registry schema.
--
-- Applied idempotently at startup by SQLiteRegistry.Migrate. Timestamps are
-- stored as RFC3339Nano TEXT (sortable, human-readable, timezone-explicit);
-- rich/nested structures are stored as JSON TEXT blobs.

-- nodes: HardwareProfile and state of every enrolled node.
CREATE TABLE IF NOT EXISTS nodes (
    id               TEXT PRIMARY KEY,
    hostname         TEXT NOT NULL DEFAULT '',
    os               TEXT NOT NULL DEFAULT '',
    arch             TEXT NOT NULL DEFAULT '',
    ram_gb           REAL NOT NULL DEFAULT 0,
    vram_gb          REAL NOT NULL DEFAULT 0,
    state            TEXT NOT NULL DEFAULT '',
    -- Addresses the agent advertised at Join time (host:port). Empty when not
    -- advertised; the orchestrator then falls back to hostname + well-known port.
    -- Added additively for existing databases by Migrate (see ensureColumn).
    advertised_agent_addr     TEXT NOT NULL DEFAULT '',
    advertised_inference_addr TEXT NOT NULL DEFAULT '',
    last_seen        TEXT,
    hardware_profile TEXT NOT NULL DEFAULT '{}',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_nodes_state ON nodes (state);

-- links: measured bandwidth/latency matrix between nodes.
CREATE TABLE IF NOT EXISTS links (
    from_node     TEXT NOT NULL,
    to_node       TEXT NOT NULL,
    bandwidth_gbs REAL NOT NULL DEFAULT 0,
    rtt_ms        REAL NOT NULL DEFAULT 0,
    measured_at   TEXT,
    PRIMARY KEY (from_node, to_node)
);

-- models: model catalog (architecture, quantizations, requirements, draft).
CREATE TABLE IF NOT EXISTS models (
    id             TEXT PRIMARY KEY,
    family         TEXT NOT NULL DEFAULT '',
    architecture   TEXT NOT NULL DEFAULT '',
    params_total_b REAL NOT NULL DEFAULT 0,
    engine         TEXT NOT NULL DEFAULT '',
    spec           TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

-- plans: DeploymentPlans produced by the planner (incl. failover plans).
CREATE TABLE IF NOT EXISTS plans (
    id           TEXT PRIMARY KEY,
    model_id     TEXT NOT NULL,
    quantization TEXT NOT NULL DEFAULT '',
    cost         REAL NOT NULL DEFAULT 0,
    plan         TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_plans_model ON plans (model_id);

-- deployments: active deployments and their lifecycle state.
CREATE TABLE IF NOT EXISTS deployments (
    id         TEXT PRIMARY KEY,
    model_id   TEXT NOT NULL,
    plan_id    TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deployments_state ON deployments (state);
CREATE INDEX IF NOT EXISTS idx_deployments_model ON deployments (model_id);

-- api_keys: gateway credentials, quotas and associated tenant. Only a hash of
-- the key material is stored.
CREATE TABLE IF NOT EXISTS api_keys (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    key_hash   TEXT NOT NULL,
    tenant     TEXT NOT NULL DEFAULT '',
    quota      INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys (tenant);

-- sessions: inference sessions for metrics/attribution.
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    api_key_id TEXT NOT NULL DEFAULT '',
    model_id   TEXT NOT NULL DEFAULT '',
    metadata   TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_api_key ON sessions (api_key_id);

-- audit_log: append-only record of administrative actions (who-what-when).
CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    actor      TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL DEFAULT '',
    target     TEXT NOT NULL DEFAULT '',
    details    TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log (created_at);

-- certs: internal PKI state (issued certificates, expiries, revocation).
CREATE TABLE IF NOT EXISTS certs (
    serial     TEXT PRIMARY KEY,
    subject    TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL DEFAULT '',
    pem        TEXT NOT NULL DEFAULT '',
    not_before TEXT,
    not_after  TEXT,
    state      TEXT NOT NULL DEFAULT 'issued',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_certs_state ON certs (state);

-- usage_log: per-request token accounting for chargeback/billing.
CREATE TABLE IF NOT EXISTS usage_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    api_key_id    TEXT NOT NULL,
    model_id      TEXT NOT NULL,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    request_at    TEXT NOT NULL  -- RFC3339
);
CREATE INDEX IF NOT EXISTS idx_usage_log_api_key ON usage_log (api_key_id);
CREATE INDEX IF NOT EXISTS idx_usage_log_request_at ON usage_log (request_at);
