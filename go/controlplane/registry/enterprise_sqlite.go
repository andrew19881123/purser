package registry

// enterprise_sqlite.go — stub implementations of the Wave B Registry methods
// on *SQLiteRegistry.  Each stub returns a "not implemented" error so the
// compile-time interface assertion in sqlite.go continues to pass while the
// real implementations land in subsequent epics.
//
// NOTE: Do NOT add production logic here. Individual Wave B epics own their
// own *_sqlite.go files (e.g. oidc_sqlite.go, quota_sqlite.go). These stubs
// exist solely to satisfy the interface contract during the foundational
// schema epic.

import (
	"context"
	"errors"
	"time"
)

var errNotImplemented = errors.New("registry: not implemented")

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
