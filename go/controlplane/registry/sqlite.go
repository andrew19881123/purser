package registry

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/purser/purser/go/controlplane/audit"

	// modernc.org/sqlite is a pure-Go (CGO-free) SQLite driver. It registers
	// itself under the name "sqlite". Keeping the build CGO-free is essential
	// for air-gap and cross-compilation.
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// SQLiteRegistry is the single-node [Registry] implementation backed by an
// embedded SQLite database (pure-Go driver, no CGO). It is safe for concurrent
// use: database/sql serializes access and WAL mode is enabled for readers.
type SQLiteRegistry struct {
	db *sql.DB
	// auditMu serializes AppendAudit so the read-tail / compute-hash / insert
	// sequence is atomic in-process and concurrent writers receive a strictly
	// monotonic, gap-free seq and the correct prev_hash for the chain.
	auditMu sync.Mutex
	// inferenceAuditMu serializes RecordInferenceEvent for the same reason:
	// the read-tail / compute-hash / INSERT OR IGNORE sequence must be atomic
	// so concurrent writers receive a gap-free seq and the correct prev_hash.
	inferenceAuditMu sync.Mutex
}

// compile-time assertion that SQLiteRegistry satisfies Registry.
var _ Registry = (*SQLiteRegistry)(nil)

// Open opens (creating if necessary) a SQLite-backed registry at dsn. dsn is a
// file path (e.g. "/var/lib/purser/registry.db") or ":memory:" for tests.
// The returned registry has not yet been migrated — call Migrate.
func Open(dsn string) (*SQLiteRegistry, error) {
	// Enable foreign keys, WAL journaling and a busy timeout via DSN pragmas so
	// every connection in the pool is configured identically.
	conn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", dsn)
	db, err := sql.Open("sqlite", conn)
	if err != nil {
		return nil, fmt.Errorf("registry: open sqlite: %w", err)
	}
	return &SQLiteRegistry{db: db}, nil
}

// NewWithDB wraps an existing *sql.DB (useful for tests or custom pools).
func NewWithDB(db *sql.DB) *SQLiteRegistry { return &SQLiteRegistry{db: db} }

// DB exposes the underlying handle for advanced callers (backup, diagnostics).
func (r *SQLiteRegistry) DB() *sql.DB { return r.db }

func (r *SQLiteRegistry) Migrate(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("registry: migrate: %w", err)
	}
	// Additive, idempotent column migrations for databases created before a
	// column existed. CREATE TABLE IF NOT EXISTS never alters an existing table,
	// so promoted columns added after the first release are backfilled here.
	for _, m := range []struct{ table, column, def string }{
		{"nodes", "advertised_agent_addr", "TEXT NOT NULL DEFAULT ''"},
		{"nodes", "advertised_inference_addr", "TEXT NOT NULL DEFAULT ''"},
		// Tamper-evident hash-chain columns for the audit log. Nullable (no
		// default) so rows written before the chain existed remain valid.
		{"audit_log", "seq", "INTEGER"},
		{"audit_log", "prev_hash", "TEXT"},
		{"audit_log", "hash", "TEXT"},
		// Import provenance for models (HuggingFace Hub, s3://, gs://, az://).
		{"models", "source", "TEXT NOT NULL DEFAULT '{}'"},
		// RBAC role for API keys. Default "admin" preserves full access for
		// any key created before this column existed.
		{"api_keys", "role", "TEXT NOT NULL DEFAULT 'admin'"},
		// Tamper-evident hash-chain columns for the inference audit log.
		// Nullable so rows written before the chain existed remain valid.
		{"inference_audit_log", "seq", "INTEGER"},
		{"inference_audit_log", "prev_hash", "TEXT"},
		{"inference_audit_log", "hash", "TEXT"},
	} {
		if err := r.ensureColumn(ctx, m.table, m.column, m.def); err != nil {
			return fmt.Errorf("registry: migrate: %w", err)
		}
	}
	// Enforce chain-position uniqueness once the seq column is guaranteed to
	// exist (created here, after ensureColumn, so it works on both fresh and
	// pre-existing databases). SQLite permits multiple NULLs in a UNIQUE index,
	// so legacy rows with a NULL seq do not collide.
	if _, err := r.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_log_seq ON audit_log (seq)`); err != nil {
		return fmt.Errorf("registry: migrate: %w", err)
	}
	// Same uniqueness enforcement for the inference audit log chain seq column.
	// SQLite permits multiple NULLs in a UNIQUE index, so pre-chain rows with a
	// NULL seq do not collide.
	if _, err := r.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_inference_audit_log_seq ON inference_audit_log (seq)`); err != nil {
		return fmt.Errorf("registry: migrate: %w", err)
	}
	// Unique index on key_hash so GetAPIKeyByHash is an O(1) point lookup.
	if _, err := r.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys (key_hash)`); err != nil {
		return fmt.Errorf("registry: migrate: %w", err)
	}
	return nil
}

// ensureColumn adds column (with the given type/default DDL) to table if it is
// not already present. It is idempotent: a column that already exists is left
// untouched. SQLite has no "ADD COLUMN IF NOT EXISTS", so the current columns
// are inspected via PRAGMA table_info first.
func (r *SQLiteRegistry) ensureColumn(ctx context.Context, table, column, def string) error {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def))
	return err
}

func (r *SQLiteRegistry) Ping(ctx context.Context) error { return r.db.PingContext(ctx) }

func (r *SQLiteRegistry) Close() error { return r.db.Close() }

// --- time / json helpers ---------------------------------------------------

const tsLayout = time.RFC3339Nano

// nowRFC returns the current UTC time formatted for storage.
func nowUTC() time.Time { return time.Now().UTC() }

// fmtTime encodes a time for a NOT NULL TEXT column.
func fmtTime(t time.Time) string { return t.UTC().Format(tsLayout) }

// fmtNullTime encodes a possibly-zero time for a nullable column.
func fmtNullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(tsLayout)
}

// parseTime decodes a nullable TEXT timestamp; a NULL/empty value yields the
// zero time.
func parseTime(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(tsLayout, s.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// jsonOrEmpty returns b if it is non-empty JSON, otherwise "{}", so NOT NULL
// JSON columns always hold valid JSON.
func jsonOrEmpty(b json.RawMessage) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// --- Nodes -----------------------------------------------------------------

func (r *SQLiteRegistry) CreateNode(ctx context.Context, n *Node) error {
	now := nowUTC()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO nodes (id, hostname, os, arch, ram_gb, vram_gb, state, advertised_agent_addr, advertised_inference_addr, last_seen, hardware_profile, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Hostname, n.OS, n.Arch, n.RAMGB, n.VRAMGB, n.State,
		n.AdvertisedAgentAddr, n.AdvertisedInferenceAddr,
		fmtNullTime(n.LastSeen), jsonOrEmpty(n.HardwareProfile),
		fmtTime(n.CreatedAt), fmtTime(n.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create node %q: %w", n.ID, err)
	}
	return nil
}

func scanNode(s interface{ Scan(...any) error }) (*Node, error) {
	var (
		n        Node
		lastSeen sql.NullString
		created  sql.NullString
		updated  sql.NullString
		hw       string
	)
	if err := s.Scan(&n.ID, &n.Hostname, &n.OS, &n.Arch, &n.RAMGB, &n.VRAMGB,
		&n.State, &n.AdvertisedAgentAddr, &n.AdvertisedInferenceAddr,
		&lastSeen, &hw, &created, &updated); err != nil {
		return nil, err
	}
	n.LastSeen = parseTime(lastSeen)
	n.CreatedAt = parseTime(created)
	n.UpdatedAt = parseTime(updated)
	n.HardwareProfile = json.RawMessage(hw)
	return &n, nil
}

const nodeCols = `id, hostname, os, arch, ram_gb, vram_gb, state, advertised_agent_addr, advertised_inference_addr, last_seen, hardware_profile, created_at, updated_at`

func (r *SQLiteRegistry) GetNode(ctx context.Context, id string) (*Node, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id)
	n, err := scanNode(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get node %q: %w", id, err)
	}
	return n, nil
}

func (r *SQLiteRegistry) ListNodes(ctx context.Context) ([]*Node, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("registry: list nodes: %w", err)
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list nodes: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateNode(ctx context.Context, n *Node) error {
	n.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE nodes SET hostname=?, os=?, arch=?, ram_gb=?, vram_gb=?, state=?,
			advertised_agent_addr=?, advertised_inference_addr=?,
			last_seen=?, hardware_profile=?, updated_at=?
		WHERE id=?`,
		n.Hostname, n.OS, n.Arch, n.RAMGB, n.VRAMGB, n.State,
		n.AdvertisedAgentAddr, n.AdvertisedInferenceAddr,
		fmtNullTime(n.LastSeen), jsonOrEmpty(n.HardwareProfile), fmtTime(n.UpdatedAt), n.ID)
	if err != nil {
		return fmt.Errorf("registry: update node %q: %w", n.ID, err)
	}
	return mustAffect(res, "node", n.ID)
}

func (r *SQLiteRegistry) DeleteNode(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete node %q: %w", id, err)
	}
	return mustAffect(res, "node", id)
}

// --- Links -----------------------------------------------------------------

func (r *SQLiteRegistry) UpsertLink(ctx context.Context, l *Link) error {
	if l.MeasuredAt.IsZero() {
		l.MeasuredAt = nowUTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO links (from_node, to_node, bandwidth_gbs, rtt_ms, measured_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(from_node, to_node) DO UPDATE SET
			bandwidth_gbs = excluded.bandwidth_gbs,
			rtt_ms        = excluded.rtt_ms,
			measured_at   = excluded.measured_at`,
		l.FromNode, l.ToNode, l.BandwidthGBs, l.RTTMs, fmtNullTime(l.MeasuredAt))
	if err != nil {
		return fmt.Errorf("registry: upsert link %q->%q: %w", l.FromNode, l.ToNode, err)
	}
	return nil
}

const linkCols = `from_node, to_node, bandwidth_gbs, rtt_ms, measured_at`

func scanLink(s interface{ Scan(...any) error }) (*Link, error) {
	var (
		l          Link
		measuredAt sql.NullString
	)
	if err := s.Scan(&l.FromNode, &l.ToNode, &l.BandwidthGBs, &l.RTTMs, &measuredAt); err != nil {
		return nil, err
	}
	l.MeasuredAt = parseTime(measuredAt)
	return &l, nil
}

func (r *SQLiteRegistry) ListLinks(ctx context.Context) ([]*Link, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+linkCols+` FROM links ORDER BY from_node, to_node`)
	if err != nil {
		return nil, fmt.Errorf("registry: list links: %w", err)
	}
	defer rows.Close()
	var out []*Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list links: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// --- Models ----------------------------------------------------------------

func (r *SQLiteRegistry) CreateModel(ctx context.Context, m *Model) error {
	now := nowUTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO models (id, family, architecture, params_total_b, engine, spec, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Family, m.Architecture, m.ParamsTotalB, m.Engine,
		jsonOrEmpty(m.Spec), jsonOrEmpty(m.Source), fmtTime(m.CreatedAt), fmtTime(m.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create model %q: %w", m.ID, err)
	}
	return nil
}

const modelCols = `id, family, architecture, params_total_b, engine, spec, source, created_at, updated_at`

func scanModel(s interface{ Scan(...any) error }) (*Model, error) {
	var (
		m       Model
		spec    string
		source  string
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&m.ID, &m.Family, &m.Architecture, &m.ParamsTotalB, &m.Engine,
		&spec, &source, &created, &updated); err != nil {
		return nil, err
	}
	m.Spec = json.RawMessage(spec)
	m.Source = json.RawMessage(source)
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	return &m, nil
}

func (r *SQLiteRegistry) GetModel(ctx context.Context, id string) (*Model, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+modelCols+` FROM models WHERE id = ?`, id)
	m, err := scanModel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get model %q: %w", id, err)
	}
	return m, nil
}

func (r *SQLiteRegistry) ListModels(ctx context.Context) ([]*Model, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+modelCols+` FROM models ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("registry: list models: %w", err)
	}
	defer rows.Close()
	var out []*Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list models: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateModel(ctx context.Context, m *Model) error {
	m.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE models SET family=?, architecture=?, params_total_b=?, engine=?, spec=?, source=?, updated_at=?
		WHERE id=?`,
		m.Family, m.Architecture, m.ParamsTotalB, m.Engine, jsonOrEmpty(m.Spec), jsonOrEmpty(m.Source), fmtTime(m.UpdatedAt), m.ID)
	if err != nil {
		return fmt.Errorf("registry: update model %q: %w", m.ID, err)
	}
	return mustAffect(res, "model", m.ID)
}

func (r *SQLiteRegistry) DeleteModel(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM models WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete model %q: %w", id, err)
	}
	return mustAffect(res, "model", id)
}

// --- Plans -----------------------------------------------------------------

func (r *SQLiteRegistry) CreatePlan(ctx context.Context, p *Plan) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = nowUTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plans (id, model_id, quantization, cost, plan, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.ModelID, p.Quantization, p.Cost, jsonOrEmpty(p.Plan), fmtTime(p.CreatedAt))
	if err != nil {
		return fmt.Errorf("registry: create plan %q: %w", p.ID, err)
	}
	return nil
}

const planCols = `id, model_id, quantization, cost, plan, created_at`

func scanPlan(s interface{ Scan(...any) error }) (*Plan, error) {
	var (
		p       Plan
		plan    string
		created sql.NullString
	)
	if err := s.Scan(&p.ID, &p.ModelID, &p.Quantization, &p.Cost, &plan, &created); err != nil {
		return nil, err
	}
	p.Plan = json.RawMessage(plan)
	p.CreatedAt = parseTime(created)
	return &p, nil
}

func (r *SQLiteRegistry) GetPlan(ctx context.Context, id string) (*Plan, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+planCols+` FROM plans WHERE id = ?`, id)
	p, err := scanPlan(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get plan %q: %w", id, err)
	}
	return p, nil
}

func (r *SQLiteRegistry) ListPlans(ctx context.Context) ([]*Plan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+planCols+` FROM plans ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("registry: list plans: %w", err)
	}
	defer rows.Close()
	var out []*Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list plans: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) DeletePlan(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM plans WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete plan %q: %w", id, err)
	}
	return mustAffect(res, "plan", id)
}

// --- Deployments -----------------------------------------------------------

func (r *SQLiteRegistry) CreateDeployment(ctx context.Context, d *Deployment) error {
	now := nowUTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO deployments (id, model_id, plan_id, state, detail, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ModelID, d.PlanID, d.State, jsonOrEmpty(d.Detail),
		fmtTime(d.CreatedAt), fmtTime(d.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create deployment %q: %w", d.ID, err)
	}
	return nil
}

const deploymentCols = `id, model_id, plan_id, state, detail, created_at, updated_at`

func scanDeployment(s interface{ Scan(...any) error }) (*Deployment, error) {
	var (
		d       Deployment
		detail  string
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&d.ID, &d.ModelID, &d.PlanID, &d.State, &detail, &created, &updated); err != nil {
		return nil, err
	}
	d.Detail = json.RawMessage(detail)
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return &d, nil
}

func (r *SQLiteRegistry) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE id = ?`, id)
	d, err := scanDeployment(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get deployment %q: %w", id, err)
	}
	return d, nil
}

func (r *SQLiteRegistry) ListDeployments(ctx context.Context) ([]*Deployment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+deploymentCols+` FROM deployments ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("registry: list deployments: %w", err)
	}
	defer rows.Close()
	var out []*Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list deployments: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateDeployment(ctx context.Context, d *Deployment) error {
	d.UpdatedAt = nowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE deployments SET model_id=?, plan_id=?, state=?, detail=?, updated_at=?
		WHERE id=?`,
		d.ModelID, d.PlanID, d.State, jsonOrEmpty(d.Detail), fmtTime(d.UpdatedAt), d.ID)
	if err != nil {
		return fmt.Errorf("registry: update deployment %q: %w", d.ID, err)
	}
	return mustAffect(res, "deployment", d.ID)
}

func (r *SQLiteRegistry) DeleteDeployment(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM deployments WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete deployment %q: %w", id, err)
	}
	return mustAffect(res, "deployment", id)
}

// --- API keys --------------------------------------------------------------

func (r *SQLiteRegistry) CreateAPIKey(ctx context.Context, k *APIKey) error {
	now := nowUTC()
	if k.CreatedAt.IsZero() {
		k.CreatedAt = now
	}
	k.UpdatedAt = now
	role := k.Role
	if role == "" {
		role = "admin"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, name, key_hash, tenant, role, quota, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.KeyHash, k.Tenant, role, k.Quota, boolToInt(k.Enabled),
		fmtTime(k.CreatedAt), fmtTime(k.UpdatedAt))
	if err != nil {
		return fmt.Errorf("registry: create api_key %q: %w", k.ID, err)
	}
	k.Role = role
	return nil
}

const apiKeyCols = `id, name, key_hash, tenant, role, quota, enabled, created_at, updated_at`

func scanAPIKey(s interface{ Scan(...any) error }) (*APIKey, error) {
	var (
		k       APIKey
		enabled int64
		created sql.NullString
		updated sql.NullString
	)
	if err := s.Scan(&k.ID, &k.Name, &k.KeyHash, &k.Tenant, &k.Role, &k.Quota, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	k.CreatedAt = parseTime(created)
	k.UpdatedAt = parseTime(updated)
	return &k, nil
}

func (r *SQLiteRegistry) GetAPIKey(ctx context.Context, id string) (*APIKey, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get api_key %q: %w", id, err)
	}
	return k, nil
}

// GetAPIKeyByHash returns the enabled API key whose key_hash column equals
// keyHash. The WHERE clause hits the idx_api_keys_hash unique index, making
// the lookup O(1) — the preferred path for the RBAC middleware hot-path.
func (r *SQLiteRegistry) GetAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash = ? AND enabled = 1 LIMIT 1`,
		keyHash)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get api_key by hash: %w", err)
	}
	return k, nil
}

func (r *SQLiteRegistry) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("registry: list api_keys: %w", err)
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list api_keys: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateAPIKey(ctx context.Context, k *APIKey) error {
	k.UpdatedAt = nowUTC()
	role := k.Role
	if role == "" {
		role = "admin"
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE api_keys SET name=?, key_hash=?, tenant=?, role=?, quota=?, enabled=?, updated_at=?
		WHERE id=?`,
		k.Name, k.KeyHash, k.Tenant, role, k.Quota, boolToInt(k.Enabled), fmtTime(k.UpdatedAt), k.ID)
	if err != nil {
		return fmt.Errorf("registry: update api_key %q: %w", k.ID, err)
	}
	return mustAffect(res, "api_key", k.ID)
}

func (r *SQLiteRegistry) DeleteAPIKey(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("registry: delete api_key %q: %w", id, err)
	}
	return mustAffect(res, "api_key", id)
}

// --- Certs (internal PKI) --------------------------------------------------

func (r *SQLiteRegistry) CreateCert(ctx context.Context, c *Cert) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = nowUTC()
	}
	if c.State == "" {
		c.State = "issued"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO certs (serial, subject, role, pem, not_before, not_after, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Serial, c.Subject, c.Role, c.PEM,
		fmtNullTime(c.NotBefore), fmtNullTime(c.NotAfter), c.State, fmtTime(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("registry: create cert %q: %w", c.Serial, err)
	}
	return nil
}

const certCols = `serial, subject, role, pem, not_before, not_after, state, created_at`

func scanCert(s interface{ Scan(...any) error }) (*Cert, error) {
	var (
		c         Cert
		notBefore sql.NullString
		notAfter  sql.NullString
		created   sql.NullString
	)
	if err := s.Scan(&c.Serial, &c.Subject, &c.Role, &c.PEM, &notBefore, &notAfter, &c.State, &created); err != nil {
		return nil, err
	}
	c.NotBefore = parseTime(notBefore)
	c.NotAfter = parseTime(notAfter)
	c.CreatedAt = parseTime(created)
	return &c, nil
}

func (r *SQLiteRegistry) GetCert(ctx context.Context, serial string) (*Cert, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certs WHERE serial = ?`, serial)
	c, err := scanCert(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get cert %q: %w", serial, err)
	}
	return c, nil
}

func (r *SQLiteRegistry) ListCerts(ctx context.Context) ([]*Cert, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+certCols+` FROM certs ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("registry: list certs: %w", err)
	}
	defer rows.Close()
	var out []*Cert
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list certs: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *SQLiteRegistry) UpdateCert(ctx context.Context, c *Cert) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE certs SET subject=?, role=?, pem=?, not_before=?, not_after=?, state=?
		WHERE serial=?`,
		c.Subject, c.Role, c.PEM, fmtNullTime(c.NotBefore), fmtNullTime(c.NotAfter), c.State, c.Serial)
	if err != nil {
		return fmt.Errorf("registry: update cert %q: %w", c.Serial, err)
	}
	return mustAffect(res, "cert", c.Serial)
}

func (r *SQLiteRegistry) DeleteCert(ctx context.Context, serial string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM certs WHERE serial=?`, serial)
	if err != nil {
		return fmt.Errorf("registry: delete cert %q: %w", serial, err)
	}
	return mustAffect(res, "cert", serial)
}

// --- Audit log -------------------------------------------------------------

// ChainEntry reconstructs the tamper-evident audit.Entry that AppendAudit
// hashed for this row. It is the single, shared definition of how a stored
// audit row maps onto the hash chain, used both when a row is written (to
// compute its Hash) and when it is read back (to re-verify it). Because the
// same fields flow through the same rule in both directions, the bytes hashed
// at write time and re-derived at read time are identical, so audit.Verify
// passes on an untouched log and fails if any stored field was mutated.
//
// The time that is hashed is CreatedAt.UnixNano(); created_at is persisted as
// RFC3339Nano TEXT, which round-trips to the exact UnixNano. Details is mapped
// to a deterministic map[string]string by detailsToMap.
func (e *AuditEntry) ChainEntry() audit.Entry {
	return audit.Entry{
		Seq:          e.Seq,
		TimeUnixNano: e.CreatedAt.UnixNano(),
		Actor:        e.Actor,
		Action:       e.Action,
		Target:       e.Target,
		Details:      detailsToMap(e.Details),
		PrevHash:     e.PrevHash,
		Hash:         e.Hash,
	}
}

// detailsToMap converts a stored audit "details" JSON blob into the
// deterministic map[string]string that the hash chain covers. The rule is
// fixed so equal input bytes always yield an equal map at both write and read
// time:
//
//   - a null/empty/non-object or unparseable blob yields an empty (nil) map,
//     which the audit engine treats identically to an empty map;
//   - a JSON string value is used verbatim (unquoted);
//   - any non-string value (number, bool, array, nested object, null) is
//     encoded as its compact JSON form, giving a stable, order-preserving
//     stringification.
//
// The map is order-independent because audit.Entry.CanonicalBytes sorts keys.
func detailsToMap(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return nil
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
			continue
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, v); err == nil {
			out[k] = buf.String()
		} else {
			out[k] = string(v)
		}
	}
	return out
}

// AppendAudit appends one entry and extends the tamper-evident hash chain. It
// reads the current chain tail, assigns the next contiguous Seq and the tail's
// Hash as PrevHash, computes this entry's Hash over its content (via
// ChainEntry), and persists the row with its chain fields. The whole
// read-tail/compute/insert sequence runs under auditMu and a single
// transaction so concurrent writers get a monotonic, gap-free seq and a correct
// prev_hash. The assigned ID, Seq, PrevHash and Hash are written back into e.
func (r *SQLiteRegistry) AppendAudit(ctx context.Context, e *AuditEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = nowUTC()
	}

	r.auditMu.Lock()
	defer r.auditMu.Unlock()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("registry: append audit: begin: %w", err)
	}
	defer tx.Rollback()

	var (
		lastSeq  sql.NullInt64
		lastHash sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
		SELECT seq, hash FROM audit_log
		WHERE seq IS NOT NULL
		ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &lastHash)
	switch {
	case err == sql.ErrNoRows:
		e.Seq = audit.FirstSeq
		e.PrevHash = audit.GenesisPrevHash
	case err != nil:
		return fmt.Errorf("registry: append audit: read tail: %w", err)
	default:
		e.Seq = uint64(lastSeq.Int64) + 1
		e.PrevHash = lastHash.String
	}

	// jsonOrEmpty is the canonical stored form; ChainEntry re-parses the same
	// bytes at read time, so hashing detailsToMap(e.Details) here is consistent
	// (an empty/nil Details and "{}" both map to an empty map).
	detailsJSON := jsonOrEmpty(e.Details)
	hash, err := e.ChainEntry().ComputeHash()
	if err != nil {
		return fmt.Errorf("registry: append audit: hash: %w", err)
	}
	e.Hash = hash

	res, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (actor, action, target, details, created_at, seq, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Actor, e.Action, e.Target, detailsJSON, fmtTime(e.CreatedAt),
		int64(e.Seq), e.PrevHash, e.Hash)
	if err != nil {
		return fmt.Errorf("registry: append audit: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		e.ID = id
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("registry: append audit: commit: %w", err)
	}

	// OTEL audit-log bridge: emit each committed audit event as a span event
	// on the active trace span (if any). When an OTLP endpoint is configured
	// this ships audit records to Splunk / Elastic / Dynatrace in real time,
	// correlated with the originating HTTP span. When no tracer is active (or
	// OTEL is not configured) IsRecording() is false and this is a no-op.
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.AddEvent("purser.audit",
			trace.WithAttributes(
				attribute.Int64("audit.seq", int64(e.Seq)),
				attribute.String("audit.actor", e.Actor),
				attribute.String("audit.action", e.Action),
				attribute.String("audit.target", e.Target),
			),
		)
	}
	return nil
}

func (r *SQLiteRegistry) ListAudit(ctx context.Context, limit int) ([]*AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, actor, action, target, details, created_at, seq, prev_hash, hash
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("registry: list audit: %w", err)
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		var (
			e        AuditEntry
			details  string
			created  sql.NullString
			seq      sql.NullInt64
			prevHash sql.NullString
			hash     sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &details, &created, &seq, &prevHash, &hash); err != nil {
			return nil, fmt.Errorf("registry: list audit: %w", err)
		}
		e.Details = json.RawMessage(details)
		e.CreatedAt = parseTime(created)
		if seq.Valid {
			e.Seq = uint64(seq.Int64)
		}
		e.PrevHash = prevHash.String
		e.Hash = hash.String
		out = append(out, &e)
	}
	return out, rows.Err()
}

// --- Usage log -------------------------------------------------------------

// RecordUsage inserts one usage_log row for a completed inference request.
func (r *SQLiteRegistry) RecordUsage(ctx context.Context, apiKeyID, modelID string, inputTokens, outputTokens int64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_log (api_key_id, model_id, input_tokens, output_tokens, request_at)
		VALUES (?, ?, ?, ?, ?)`,
		apiKeyID, modelID, inputTokens, outputTokens, fmtTime(nowUTC()))
	if err != nil {
		return fmt.Errorf("registry: record usage: %w", err)
	}
	return nil
}

// GetKeyUsage returns aggregate token usage (count + sums) for a single API key.
// All sums default to 0 for a key with no recorded requests.
func (r *SQLiteRegistry) GetKeyUsage(ctx context.Context, apiKeyID string) (*KeyUsageSummary, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
		FROM usage_log WHERE api_key_id = ?`, apiKeyID)
	s := &KeyUsageSummary{APIKeyID: apiKeyID}
	if err := row.Scan(&s.TotalRequests, &s.InputTokens, &s.OutputTokens); err != nil {
		return nil, fmt.Errorf("registry: get key usage %q: %w", apiKeyID, err)
	}
	return s, nil
}

// GetUsageSummary returns per-tenant token usage since since (zero = all time).
// Rows are ordered by tenant name. Tenants that have no usage records are omitted.
func (r *SQLiteRegistry) GetUsageSummary(ctx context.Context, since time.Time) ([]TenantUsage, error) {
	const base = `
		SELECT k.tenant, COUNT(*) AS total_requests,
			   COALESCE(SUM(u.input_tokens), 0), COALESCE(SUM(u.output_tokens), 0)
		FROM usage_log u
		JOIN api_keys k ON k.id = u.api_key_id
		%s
		GROUP BY k.tenant
		ORDER BY k.tenant`

	var (
		rows *sql.Rows
		err  error
	)
	if since.IsZero() {
		rows, err = r.db.QueryContext(ctx, fmt.Sprintf(base, ""))
	} else {
		rows, err = r.db.QueryContext(ctx, fmt.Sprintf(base, "WHERE u.request_at >= ?"), fmtTime(since))
	}
	if err != nil {
		return nil, fmt.Errorf("registry: get usage summary: %w", err)
	}
	defer rows.Close()
	var out []TenantUsage
	for rows.Next() {
		var tu TenantUsage
		if err := rows.Scan(&tu.Tenant, &tu.TotalRequests, &tu.InputTokens, &tu.OutputTokens); err != nil {
			return nil, fmt.Errorf("registry: get usage summary: %w", err)
		}
		out = append(out, tu)
	}
	return out, rows.Err()
}

// --- shared helpers --------------------------------------------------------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mustAffect converts a zero-rows-affected result into ErrNotFound.
func mustAffect(res sql.Result, kind, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("registry: %s %q: rows affected: %w", kind, id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
