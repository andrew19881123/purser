package registry

// enterprise_sqlite.go — SQLiteRegistry implementations split by wave:
//
//   * Wave B real implementations (OIDC sessions):
//     CreateOIDCSession, GetOIDCSession, RevokeOIDCSessionsBySubject,
//     RevokeOIDCSessionByTokenHash, DeleteExpiredOIDCSessions.
//   * Wave B real implementations (PKCE state):
//     SetPKCEState, ConsumePKCEState, DeleteExpiredPKCEStates.
//   * Wave B real implementations (API key lifecycle):
//     RotateAPIKey, UpdateAPIKeyLastUsed, ListAPIKeysExpiringBefore,
//     RecordAPIKeyAccess, HasAnyAPIKey, ListAPIKeyAccessLog.
//   * Stubs for remaining Wave B methods: model pricing, tenant quotas,
//     policy versions, GDPR erasure — each returns errNotImplemented.
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

// --- Helper functions (API key lifecycle) ------------------------------------

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

// --- Helper functions (OIDC / PKCE) ------------------------------------------

// nullStr converts an empty Go string to a SQL NULL, or returns the string
// unchanged. Use for nullable TEXT columns where an empty string has no
// storage meaning (e.g. refresh_token_enc when no refresh token is available).
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// fmtNullTimePtr encodes a *time.Time for a nullable TEXT column. A nil pointer
// stores as SQL NULL; a non-nil pointer stores as RFC3339Nano UTC.
func fmtNullTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

// --- OIDC Session Store -------------------------------------------------------

// CreateOIDCSession persists a new browser session. INSERT OR REPLACE lets
// callers re-issue a session token without explicit deletion of the old row.
func (r *SQLiteRegistry) CreateOIDCSession(ctx context.Context, s *OIDCSession) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO oidc_sessions
		 (token_hash, sub, email, idp_issuer, auth_method, created_at, expires_at,
		  revoked, refresh_token_enc, access_token_expiry)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		s.TokenHash, s.Sub, s.Email, s.IDPIssuer, s.AuthMethod,
		fmtTime(s.CreatedAt), fmtTime(s.ExpiresAt),
		nullStr(s.RefreshTokenEnc),
		fmtNullTimePtr(s.AccessTokenExpiry),
	)
	return err
}

// GetOIDCSession returns a non-revoked, non-expired session by token_hash.
// Returns ErrNotFound when the hash is unknown, the session is revoked, or it
// has already expired.
func (r *SQLiteRegistry) GetOIDCSession(ctx context.Context, tokenHash string) (*OIDCSession, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT token_hash, sub, email, idp_issuer, auth_method, created_at, expires_at,
		        revoked, revoked_at, refresh_token_enc, access_token_expiry
		 FROM oidc_sessions
		 WHERE token_hash=? AND revoked=0 AND expires_at > ?`,
		tokenHash, fmtTime(time.Now()),
	)
	var s OIDCSession
	var createdAtStr, expiresAtStr string
	var revokedInt int
	var revokedAtNS, refreshEncNS, accessExpiryNS sql.NullString
	err := row.Scan(
		&s.TokenHash, &s.Sub, &s.Email, &s.IDPIssuer, &s.AuthMethod,
		&createdAtStr, &expiresAtStr, &revokedInt, &revokedAtNS,
		&refreshEncNS, &accessExpiryNS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if t, e := time.Parse(tsLayout, createdAtStr); e == nil {
		s.CreatedAt = t
	}
	if t, e := time.Parse(tsLayout, expiresAtStr); e == nil {
		s.ExpiresAt = t
	}
	s.Revoked = revokedInt != 0
	if revokedAtNS.Valid && revokedAtNS.String != "" {
		if t, e := time.Parse(tsLayout, revokedAtNS.String); e == nil {
			s.RevokedAt = &t
		}
	}
	if refreshEncNS.Valid {
		s.RefreshTokenEnc = refreshEncNS.String
	}
	if accessExpiryNS.Valid && accessExpiryNS.String != "" {
		if t, e := time.Parse(tsLayout, accessExpiryNS.String); e == nil {
			s.AccessTokenExpiry = &t
		}
	}
	return &s, nil
}

// RevokeOIDCSessionsBySubject marks all active sessions for sub as revoked.
// Returns the number of rows updated.
func (r *SQLiteRegistry) RevokeOIDCSessionsBySubject(ctx context.Context, sub string) (int, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE oidc_sessions SET revoked=1, revoked_at=? WHERE sub=? AND revoked=0`,
		fmtTime(time.Now()), sub,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RevokeOIDCSessionByTokenHash revokes the single session identified by tokenHash.
func (r *SQLiteRegistry) RevokeOIDCSessionByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE oidc_sessions SET revoked=1, revoked_at=? WHERE token_hash=?`,
		fmtTime(time.Now()), tokenHash,
	)
	return err
}

// DeleteExpiredOIDCSessions removes sessions that have expired or that were
// revoked more than 24 hours ago. Returns the number of rows deleted.
func (r *SQLiteRegistry) DeleteExpiredOIDCSessions(ctx context.Context) (int, error) {
	now := fmtTime(time.Now())
	old := fmtTime(time.Now().Add(-24 * time.Hour))
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM oidc_sessions WHERE expires_at < ? OR (revoked=1 AND revoked_at < ?)`,
		now, old,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- PKCE State ---------------------------------------------------------------

// SetPKCEState stores a PKCE state/verifier pair with the given TTL. INSERT OR
// REPLACE ensures only one entry exists per state_hash at a time.
func (r *SQLiteRegistry) SetPKCEState(ctx context.Context, stateHash, verifier string, ttl time.Duration) error {
	expires := time.Now().Add(ttl)
	_, err := r.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO pkce_state (state_hash, verifier, expires_at) VALUES (?, ?, ?)`,
		stateHash, verifier, fmtTime(expires),
	)
	return err
}

// ConsumePKCEState atomically retrieves and deletes the verifier for stateHash.
// Returns (verifier, true, nil) on success; ("", false, nil) when the state is
// not found or has expired. Expired rows are deleted opportunistically.
func (r *SQLiteRegistry) ConsumePKCEState(ctx context.Context, stateHash string) (verifier string, ok bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck

	var v, expiresStr string
	err = tx.QueryRowContext(ctx,
		`SELECT verifier, expires_at FROM pkce_state WHERE state_hash=?`, stateHash,
	).Scan(&v, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	// Check expiry in Go so we can opportunistically delete the stale row.
	expires, parseErr := time.Parse(tsLayout, expiresStr)
	if parseErr != nil || time.Now().After(expires) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM pkce_state WHERE state_hash=?`, stateHash)
		_ = tx.Commit()
		return "", false, nil
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM pkce_state WHERE state_hash=?`, stateHash); err != nil {
		return "", false, err
	}
	return v, true, tx.Commit()
}

// DeleteExpiredPKCEStates removes all rows whose expires_at is in the past.
// Returns the number of rows deleted.
func (r *SQLiteRegistry) DeleteExpiredPKCEStates(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pkce_state WHERE expires_at < ?`, fmtTime(time.Now()),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
