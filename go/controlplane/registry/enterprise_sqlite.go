package registry

// enterprise_sqlite.go — Wave B Registry method implementations on
// *SQLiteRegistry. The OIDC session store and PKCE state are fully
// implemented here; other Wave B methods (API-key lifecycle, model pricing,
// tenant quota, etc.) remain as stubs until their respective epics land.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var errNotImplemented = errors.New("registry: not implemented")

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

func (r *SQLiteRegistry) RotateAPIKey(_ context.Context, _ string, _ *APIKey) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) UpdateAPIKeyLastUsed(_ context.Context, _ string, _ time.Time) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) ListAPIKeysExpiringBefore(_ context.Context, _ time.Time) ([]*APIKey, error) {
	return nil, errNotImplemented
}

func (r *SQLiteRegistry) RecordAPIKeyAccess(_ context.Context, _ *APIKeyAccessEntry) error {
	return errNotImplemented
}

func (r *SQLiteRegistry) HasAnyAPIKey(_ context.Context) (bool, error) {
	return false, errNotImplemented
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
