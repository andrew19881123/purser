package registry

// enterprise_sqlite.go — SQLiteRegistry implementations split by wave:
//
//   * Wave A stubs: OIDC sessions, PKCE state, model pricing, tenant quotas,
//     policy versions, GDPR erasure — each returns errNotImplemented.
//   * Wave B real implementations: API key lifecycle (RotateAPIKey,
//     UpdateAPIKeyLastUsed, ListAPIKeysExpiringBefore, RecordAPIKeyAccess,
//     HasAnyAPIKey, ListAPIKeyAccessLog).
//
// NOTE: Do NOT add production logic to the stub sections. Individual Wave B
// epics own their own *_sqlite.go files. Stubs exist solely to satisfy the
// interface contract while real implementations land in subsequent epics.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var errNotImplemented = errors.New("registry: not implemented")

// apiKeyColsExt extends the base apiKeyCols with the enterprise lifecycle
// columns so methods that need ExpiresAt (e.g. ListAPIKeysExpiringBefore)
// can scan them without changing the shared constant used by the hot paths.
const apiKeyColsExt = apiKeyCols + `, expires_at, last_used_at, predecessor_id, rotated_at, scopes`

// fmtNullTimePt encodes a possibly-nil *time.Time for a nullable column.
// Returns nil (SQLite NULL) when t is nil; otherwise delegates to fmtNullTime.
func fmtNullTimePt(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtNullTime(*t)
}

// scopesJSON encodes a slice of permission strings as a compact JSON array.
// Returns "[]" for nil/empty slices so NOT NULL DEFAULT '[]' columns stay valid.
func scopesJSON(scopes []string) string {
	if len(scopes) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(scopes)
	return string(b)
}

// scanAPIKeyExt scans a row that was selected with apiKeyColsExt, populating
// all enterprise lifecycle fields in addition to the base fields.
func scanAPIKeyExt(s interface{ Scan(...any) error }) (*APIKey, error) {
	var (
		k          APIKey
		enabled    int64
		created    sql.NullString
		updated    sql.NullString
		expiresAt  sql.NullString
		lastUsedAt sql.NullString
		rotatedAt  sql.NullString
		scopes     string
	)
	if err := s.Scan(
		&k.ID, &k.Name, &k.KeyHash, &k.Tenant, &k.Role, &k.Quota, &enabled,
		&created, &updated,
		&expiresAt, &lastUsedAt, &k.PredecessorID, &rotatedAt, &scopes,
	); err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	k.CreatedAt = parseTime(created)
	k.UpdatedAt = parseTime(updated)
	if t := parseTime(expiresAt); !t.IsZero() {
		k.ExpiresAt = &t
	}
	if t := parseTime(lastUsedAt); !t.IsZero() {
		k.LastUsedAt = &t
	}
	if t := parseTime(rotatedAt); !t.IsZero() {
		k.RotatedAt = &t
	}
	if scopes != "" && scopes != "[]" {
		_ = json.Unmarshal([]byte(scopes), &k.Scopes)
	}
	return &k, nil
}

// --- OIDC Session Store -------------------------------------------------------

func (r *SQLiteRegistry) CreateOIDCSession(_ context.Context, _ *OIDCSession) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) GetOIDCSession(_ context.Context, _ string) (*OIDCSession, error) {
	return nil, errNotImplemented
}

func (r *SQLiteRegistry) RevokeOIDCSessionsBySubject(_ context.Context, _ string) (int, error) {
	return 0, errNotImplemented
}

func (r *SQLiteRegistry) RevokeOIDCSessionByTokenHash(_ context.Context, _ string) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) DeleteExpiredOIDCSessions(_ context.Context) (int, error) {
	return 0, errNotImplemented
}

// --- PKCE State ---------------------------------------------------------------

func (r *SQLiteRegistry) SetPKCEState(_ context.Context, _, _ string, _ time.Duration) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) ConsumePKCEState(_ context.Context, _ string) (string, bool, error) {
	return "", false, errNotImplemented
}

func (r *SQLiteRegistry) DeleteExpiredPKCEStates(_ context.Context) (int, error) {
	return 0, errNotImplemented
}

// --- API Key Lifecycle --------------------------------------------------------

// RotateAPIKey atomically creates a successor key and disables the predecessor.
// The transaction ensures neither half-state is visible to concurrent readers.
func (r *SQLiteRegistry) RotateAPIKey(ctx context.Context, oldID string, newKey *APIKey) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("registry: rotate_api_key begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := nowUTC()
	role := newKey.Role
	if role == "" {
		role = "admin"
	}

	// Insert the new (successor) key with a back-link to its predecessor.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys
		 (id, name, key_hash, tenant, role, quota, enabled,
		  predecessor_id, scopes, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		newKey.ID, newKey.Name, newKey.KeyHash, newKey.Tenant, role,
		newKey.Quota, oldID, scopesJSON(newKey.Scopes),
		fmtNullTimePt(newKey.ExpiresAt), fmtTime(now), fmtTime(now),
	); err != nil {
		return fmt.Errorf("registry: rotate_api_key insert: %w", err)
	}

	// Disable the old key and record when it was rotated.
	if _, err := tx.ExecContext(ctx,
		`UPDATE api_keys SET enabled=0, rotated_at=?, updated_at=? WHERE id=?`,
		fmtTime(now), fmtTime(now), oldID,
	); err != nil {
		return fmt.Errorf("registry: rotate_api_key disable: %w", err)
	}

	return tx.Commit()
}

// UpdateAPIKeyLastUsed sets the last_used_at timestamp for the given key.
// Callers should throttle calls to at most once per 5 minutes per key to bound
// write amplification on the auth hot-path.
func (r *SQLiteRegistry) UpdateAPIKeyLastUsed(ctx context.Context, keyID string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at=?, updated_at=? WHERE id=?`,
		fmtTime(at), fmtTime(at), keyID,
	)
	return err
}

// ListAPIKeysExpiringBefore returns all enabled keys whose expires_at is set
// and falls before the given cutoff. Results are sorted by expires_at ASC so
// the soonest-expiring keys appear first.
func (r *SQLiteRegistry) ListAPIKeysExpiringBefore(ctx context.Context, before time.Time) ([]*APIKey, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+apiKeyColsExt+` FROM api_keys
		 WHERE enabled=1 AND expires_at IS NOT NULL AND expires_at < ?
		 ORDER BY expires_at ASC`,
		fmtTime(before),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: list_api_keys_expiring_before: %w", err)
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanAPIKeyExt(rows)
		if err != nil {
			return nil, fmt.Errorf("registry: list_api_keys_expiring_before scan: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RecordAPIKeyAccess appends one row to api_key_access_log. The caller is
// responsible for extracting only the /24 prefix of the client IP before
// calling this method (GDPR Art.5 data minimisation).
func (r *SQLiteRegistry) RecordAPIKeyAccess(ctx context.Context, entry *APIKeyAccessEntry) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO api_key_access_log
		 (api_key_id, key_hash, method, path, ip_prefix, user_agent, status_code, request_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.APIKeyID, entry.KeyHash, entry.Method, entry.Path,
		entry.IPPrefix, entry.UserAgent, entry.StatusCode, fmtTime(entry.RequestAt),
	)
	return err
}

// HasAnyAPIKey returns true when at least one enabled API key exists. Used by
// auth fail-closed logic to distinguish a fresh installation (no keys) from a
// misconfigured one (keys exist but none match).
func (r *SQLiteRegistry) HasAnyAPIKey(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_keys WHERE enabled=1 LIMIT 1",
	).Scan(&count)
	return count > 0, err
}

// ListAPIKeyAccessLog returns the most recent access-log entries for the given
// API key, newest first, capped at limit (limit <= 0 → default 50, max 1000).
// This method is not on the core Registry interface; callers that need it
// may type-assert to *SQLiteRegistry or use the accessLogQuerier local
// interface in the server package.
func (r *SQLiteRegistry) ListAPIKeyAccessLog(ctx context.Context, apiKeyID string, limit int) ([]*APIKeyAccessEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, api_key_id, key_hash, method, path, ip_prefix, user_agent, status_code, request_at
		 FROM api_key_access_log WHERE api_key_id = ?
		 ORDER BY request_at DESC LIMIT ?`,
		apiKeyID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("registry: list_api_key_access_log: %w", err)
	}
	defer rows.Close()
	var out []*APIKeyAccessEntry
	for rows.Next() {
		var (
			e         APIKeyAccessEntry
			requestAt sql.NullString
		)
		if err := rows.Scan(
			&e.ID, &e.APIKeyID, &e.KeyHash, &e.Method, &e.Path,
			&e.IPPrefix, &e.UserAgent, &e.StatusCode, &requestAt,
		); err != nil {
			return nil, fmt.Errorf("registry: list_api_key_access_log scan: %w", err)
		}
		e.RequestAt = parseTime(requestAt)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// --- Model Pricing ------------------------------------------------------------

func (r *SQLiteRegistry) UpsertModelPricing(_ context.Context, _ *ModelPricing) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) GetCurrentModelPricing(_ context.Context, _ string) (*ModelPricing, error) {
	return nil, errNotImplemented
}

// --- Tenant Quota -------------------------------------------------------------

func (r *SQLiteRegistry) UpsertTenantQuota(_ context.Context, _ *TenantQuota) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) GetTenantQuota(_ context.Context, _ string) (*TenantQuota, error) {
	return nil, errNotImplemented
}

func (r *SQLiteRegistry) IncrementTenantUsage(_ context.Context, _ string, _ time.Time, _ UsageDelta) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) GetTenantUsage(_ context.Context, _ string, _ time.Time) (*TenantQuotaUsage, error) {
	return nil, errNotImplemented
}

// --- Policy Versioning --------------------------------------------------------

func (r *SQLiteRegistry) GetPolicyVersions(_ context.Context, _ string, _ int) ([]*PolicyVersion, error) {
	return nil, errNotImplemented
}

func (r *SQLiteRegistry) SavePolicyVersion(_ context.Context, _ *PolicyVersion) error {
	return errNotImplemented
}

// --- GDPR ---------------------------------------------------------------------

func (r *SQLiteRegistry) RecordGDPRErasure(_ context.Context, _ *GDPRErasureLog) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) EraseInferenceEventsBySubject(_ context.Context, _ string) (int64, error) {
	return 0, errNotImplemented
}
