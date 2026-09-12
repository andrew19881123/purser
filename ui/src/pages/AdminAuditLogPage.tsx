// AdminAuditLogPage — enterprise ADMIN action trail.
//
// This is DISTINCT from the inference-audit shown on AuditPage: it records
// administrative actions (logins, API-key operations, deployment approvals,
// gdpr.erasure.completed, …) as a tamper-evident SHA-256 hash chain.
//
// Layout mirrors AuditPage's inference table:
//   ┌─ PageHeader ("Admin Audit Log")            [Show N ▾] [Refresh] ───────┐
//   ┌─ Card ─────────────────────────────────────────────────────────────────┐
//   │  Chain status badge + licensee                                         │
//   │  Filter bar (actor, action)                                            │
//   │  Table (#, Time, Actor, Action, Target, Details) + prev/next          │
//   └────────────────────────────────────────────────────────────────────────┘
//
// Enterprise-gated: GET /api/v1/enterprise/audit-log returns 402
// (license_required) without the "audit" feature — we show the shared locked
// prompt (same pattern as PoliciesPage) instead of crashing.
import { useMemo, useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
} from '../components/ui';
import { IconRefresh, IconShield } from '../components/icons';
import { useAuditLog } from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import { ApiError } from '../api/http';
import type { AuditEntry } from '../api/types';

// ---------------------------------------------------------------------------
// Enterprise license gate detection (same idiom as AuditPage / PoliciesPage)
// ---------------------------------------------------------------------------

function isLicenseRequired(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  const body = error.body as Record<string, unknown> | null | undefined;
  if (!body || typeof body !== 'object') return false;
  const errField = body.error as Record<string, unknown> | null | undefined;
  if (!errField || typeof errField !== 'object') return false;
  return errField.type === 'license_required';
}

/** A 402/403 (license or forbidden) means the operator cannot see this log — we
 *  treat both as the enterprise-gated case and show the upgrade prompt. */
function isGated(error: unknown): boolean {
  if (isLicenseRequired(error)) return true;
  return error instanceof ApiError && (error.status === 402 || error.status === 403);
}

function EnterpriseGate() {
  const t = useT();
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'flex-start',
        gap: '10px',
        background: 'var(--info-bg)',
        border: '1px solid color-mix(in srgb, var(--info-fg) 25%, transparent)',
        borderRadius: 'var(--radius)',
        padding: '20px 24px',
      }}
      role="status"
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
        <span aria-hidden="true" style={{ fontSize: '1.25em', lineHeight: 1, color: 'var(--info-fg)' }}>
          🔒
        </span>
        <strong style={{ color: 'var(--info-fg)', fontSize: '1em', fontWeight: 600 }}>
          {t('adminAudit.enterprise.title')}
        </strong>
      </div>
      <p style={{ margin: 0, color: 'var(--text)', fontSize: '0.9em', lineHeight: 1.5 }}>
        {t('adminAudit.enterprise.desc')}
      </p>
      <a
        href="https://andrew19881123.github.io/purser/enterprise/licensing/"
        target="_blank"
        rel="noreferrer"
        style={{
          color: 'var(--info-fg)',
          fontWeight: 600,
          fontSize: '0.875em',
          textDecoration: 'none',
          borderBottom: '1px solid color-mix(in srgb, var(--info-fg) 40%, transparent)',
          paddingBottom: '1px',
        }}
      >
        {t('adminAudit.enterprise.link')}
      </a>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Details rendering — compact key=value pairs from the JSON details map.
// ---------------------------------------------------------------------------

function DetailsCell({ details }: { details?: Record<string, string> }) {
  if (!details || Object.keys(details).length === 0) {
    return <span className="muted" style={{ opacity: 0.4 }}>—</span>;
  }
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem' }}>
      {Object.entries(details).map(([k, v]) => (
        <code key={k} className="inline-code" style={{ fontSize: '0.78em' }}>
          {k}={String(v)}
        </code>
      ))}
    </div>
  );
}

function AdminAuditRow({ entry }: { entry: AuditEntry }) {
  const time = entry.createdAt
    ? new Date(entry.createdAt).toLocaleString(undefined, {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      })
    : '—';
  return (
    <tr>
      <td className="muted">{entry.seq}</td>
      <td className="muted" style={{ fontSize: '0.8em', whiteSpace: 'nowrap' }}>{time}</td>
      <td>{entry.actor || <span className="muted">—</span>}</td>
      <td>
        <code className="inline-code" style={{ fontSize: '0.85em' }}>
          {entry.action}
        </code>
      </td>
      <td className="muted" style={{ maxWidth: '16rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {entry.target || <span style={{ opacity: 0.4 }}>—</span>}
      </td>
      <td>
        <DetailsCell details={entry.details} />
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

const PAGE_SIZE = 25;
const LIMIT_OPTIONS = [100, 250, 500] as const;

export function AdminAuditLogPage() {
  const t = useT();
  const [limit, setLimit] = useState<number>(100);
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [offset, setOffset] = useState(0);

  const { data, isLoading, isError, error, isFetching, refetch } = useAuditLog(limit);

  const entries = data?.entries ?? [];

  const filtered = useMemo(
    () =>
      entries.filter(
        (e) =>
          (!actor || e.actor.toLowerCase().includes(actor.toLowerCase())) &&
          (!action || e.action.toLowerCase().includes(action.toLowerCase())),
      ),
    [entries, actor, action],
  );

  const page = filtered.slice(offset, offset + PAGE_SIZE);
  const hasPrev = offset > 0;
  const hasNext = offset + PAGE_SIZE < filtered.length;

  function resetOffset() {
    setOffset(0);
  }

  const gated = isError && isGated(error);

  const pageActions = (
    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
      <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.8em' }}>
        {t('adminAudit.limit')}
        <select
          className="select select--compact"
          value={limit}
          onChange={(e) => {
            setLimit(Number(e.target.value));
            resetOffset();
          }}
          aria-label={t('adminAudit.limit')}
        >
          {LIMIT_OPTIONS.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      </label>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => void refetch()}
        disabled={isFetching}
        aria-label={t('adminAudit.refresh')}
      >
        <IconRefresh style={isFetching ? { animation: 'spin 1s linear infinite' } : undefined} />
        {t('adminAudit.refresh')}
      </Button>
    </div>
  );

  return (
    <div className="page">
      <PageHeader
        title={t('adminAudit.title')}
        subtitle={t('adminAudit.subtitle')}
        actions={pageActions}
      />

      <Card>
        {isLoading && <LoadingBlock />}

        {gated && <EnterpriseGate />}

        {isError && !gated && (
          <ErrorState
            message={errorMessage(error, t, 'error.adminAudit')}
            onRetry={() => void refetch()}
          />
        )}

        {!isLoading && !isError && data && (
          <>
            {/* Chain integrity + licensee */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                flexWrap: 'wrap',
                gap: '1rem',
                marginBottom: '1rem',
              }}
            >
              {data.chain.verified ? (
                <Badge tone="success">
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3em' }}>
                    <IconShield width={14} height={14} strokeWidth={2} />
                    {t('adminAudit.chain.verified')}
                  </span>
                </Badge>
              ) : (
                <Badge tone="danger">{t('adminAudit.chain.broken')}</Badge>
              )}
              <span className="muted" style={{ fontSize: '0.85em' }}>
                {t('adminAudit.chain.length')}: <strong>{data.chain.length.toLocaleString()}</strong>
              </span>
              {data.licensee && (
                <span className="muted" style={{ fontSize: '0.85em' }}>
                  {t('adminAudit.licensee')}: <strong>{data.licensee}</strong>
                </span>
              )}
            </div>

            {/* Filter bar */}
            <div
              style={{
                display: 'flex',
                flexWrap: 'wrap',
                gap: '0.5rem',
                marginBottom: '1rem',
                alignItems: 'flex-end',
              }}
            >
              <label style={{ display: 'flex', flexDirection: 'column', fontSize: '0.8em', gap: '0.2em' }}>
                {t('adminAudit.filter.actor')}
                <input
                  type="text"
                  className="input input--compact"
                  value={actor}
                  onChange={(e) => {
                    setActor(e.target.value);
                    resetOffset();
                  }}
                  placeholder={t('adminAudit.filter.all')}
                  aria-label={t('adminAudit.filter.actor')}
                />
              </label>
              <label style={{ display: 'flex', flexDirection: 'column', fontSize: '0.8em', gap: '0.2em' }}>
                {t('adminAudit.filter.action')}
                <input
                  type="text"
                  className="input input--compact"
                  value={action}
                  onChange={(e) => {
                    setAction(e.target.value);
                    resetOffset();
                  }}
                  placeholder={t('adminAudit.filter.all')}
                  aria-label={t('adminAudit.filter.action')}
                />
              </label>
            </div>

            {filtered.length === 0 ? (
              <EmptyState message={t('adminAudit.empty')} />
            ) : (
              <>
                <div className="table-wrap" style={{ overflowX: 'auto' }}>
                  <table className="table">
                    <thead>
                      <tr>
                        <th scope="col">{t('adminAudit.col.seq')}</th>
                        <th scope="col">{t('adminAudit.col.time')}</th>
                        <th scope="col">{t('adminAudit.col.actor')}</th>
                        <th scope="col">{t('adminAudit.col.action')}</th>
                        <th scope="col">{t('adminAudit.col.target')}</th>
                        <th scope="col">{t('adminAudit.col.details')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {page.map((entry) => (
                        <AdminAuditRow key={`${entry.seq}-${entry.hash}`} entry={entry} />
                      ))}
                    </tbody>
                  </table>
                </div>

                {/* Pagination */}
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    marginTop: '0.75rem',
                    fontSize: '0.85em',
                  }}
                >
                  <span className="muted">
                    {offset + 1}–{Math.min(offset + PAGE_SIZE, filtered.length)} / {filtered.length}
                  </span>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={!hasPrev}
                      onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                      aria-label={t('adminAudit.page.prev')}
                    >
                      {t('adminAudit.page.prev')}
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={!hasNext}
                      onClick={() => setOffset(offset + PAGE_SIZE)}
                      aria-label={t('adminAudit.page.next')}
                    >
                      {t('adminAudit.page.next')}
                    </Button>
                  </div>
                </div>
              </>
            )}
          </>
        )}
      </Card>
    </div>
  );
}
