// AuditPage — operator-facing audit and access-log viewer.
//
// Layout:
//   ┌─ Chain Integrity Panel (always visible) ──────────────────────────────┐
//   │  [Chain verified ✓ | Block count: 1420 | Last verified: …] [Verify Now] │
//   └───────────────────────────────────────────────────────────────────────┘
//   ┌─ Tabs: [Inference Audit] [Access Log] ────────────────────────────────┐
//   │  Inference Audit: filter bar → table (Seq, Model, Tenant, API Key,   │
//   │    Latency, Tokens, Status, Time) + prev/next pagination + CSV export │
//   │  Access Log: filter by API Key → table + Refresh                     │
//   └───────────────────────────────────────────────────────────────────────┘
//
// The chain integrity panel is Purser's unique differentiator — cryptographic
// proof that the inference log has not been tampered with after the fact.
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
import { useInferenceAudit, useAuditChainVerify, useAccessLog } from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import type { InferenceAuditEvent, AccessLogEntry } from '../api/types';

// ---------------------------------------------------------------------------
// Tab type
// ---------------------------------------------------------------------------

type Tab = 'inference' | 'access';

// ---------------------------------------------------------------------------
// Chain Integrity Panel
// ---------------------------------------------------------------------------

function ChainIntegrityPanel() {
  const t = useT();
  const { data, isLoading, isError, error, isFetching, refetch } = useAuditChainVerify();

  const statusBadge = useMemo(() => {
    if (!data) return null;
    if (data.verified) {
      return (
        <Badge tone="success">
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3em' }}>
            <IconShield width={14} height={14} strokeWidth={2} />
            {t('audit.chain.verified')}
          </span>
        </Badge>
      );
    }
    return (
      <Badge tone="warning">
        {t('audit.chain.brokenAt', { seq: String(data.brokenAtSeq ?? '?') })}
      </Badge>
    );
  }, [data, t]);

  return (
    <Card title={t('audit.chain.title')} className="audit-chain-panel">
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: '1rem',
        }}
      >
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState
            message={errorMessage(error, t, 'error.chainVerify')}
            onRetry={() => void refetch()}
          />
        )}
        {data && (
          <>
            {statusBadge}
            <span className="muted" style={{ fontSize: '0.85em' }}>
              {t('audit.chain.blockCount')}: <strong>{data.blockCount.toLocaleString()}</strong>
            </span>
            <span className="muted" style={{ fontSize: '0.85em' }}>
              {t('audit.chain.lastVerified')}:{' '}
              <strong>{new Date(data.lastVerifiedAt).toLocaleString()}</strong>
            </span>
          </>
        )}
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void refetch()}
          disabled={isFetching}
          aria-label={isFetching ? t('audit.chain.verifying') : t('audit.chain.verifyNow')}
        >
          <IconRefresh
            style={isFetching ? { animation: 'spin 1s linear infinite' } : undefined}
          />
          {isFetching ? t('audit.chain.verifying') : t('audit.chain.verifyNow')}
        </Button>
      </div>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Inference Audit Tab
// ---------------------------------------------------------------------------

const PAGE_SIZE = 50;

function exportInferenceCsv(events: InferenceAuditEvent[]) {
  const headers = ['seq', 'model_id', 'tenant', 'api_key_id', 'input_tokens', 'output_tokens', 'latency_ms', 'status', 'created_at'];
  const rows = events.map((e) =>
    [e.seq, e.modelId, e.tenant, e.apiKeyId, e.inputTokens, e.outputTokens, e.latencyMs, e.status, e.createdAt].join(','),
  );
  const csv = [headers.join(','), ...rows].join('\n');
  const blob = new Blob([csv], { type: 'text/csv' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `inference-audit-${new Date().toISOString().slice(0, 10)}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

function InferenceAuditTab() {
  const t = useT();
  const [offset, setOffset] = useState(0);
  const [modelId, setModelId] = useState('');
  const [tenant, setTenant] = useState('');
  const [since, setSince] = useState('');
  const [until, setUntil] = useState('');

  const params = useMemo(
    () => ({
      limit: PAGE_SIZE,
      offset,
      ...(modelId ? { modelId } : {}),
      ...(tenant ? { tenant } : {}),
      ...(since ? { since: new Date(since).toISOString() } : {}),
      ...(until ? { until: new Date(until).toISOString() } : {}),
    }),
    [offset, modelId, tenant, since, until],
  );

  const { data, isLoading, isError, error, isFetching, refetch } = useInferenceAudit(params);

  const events = data?.events ?? [];
  const total = data?.total ?? 0;

  // Unique models and tenants from the current page — used to populate dropdowns.
  const models = useMemo(() => [...new Set(events.map((e) => e.modelId))].sort(), [events]);
  const tenants = useMemo(() => [...new Set(events.map((e) => e.tenant))].sort(), [events]);

  const hasPrev = offset > 0;
  const hasNext = offset + PAGE_SIZE < total;

  function resetOffset() {
    setOffset(0);
  }

  return (
    <Card
      title={t('audit.tab.inference')}
      action={
        <Button
          variant="secondary"
          size="sm"
          onClick={() => exportInferenceCsv(events)}
          disabled={events.length === 0}
          aria-label={t('audit.export.csv')}
        >
          {t('audit.export.csv')}
        </Button>
      }
    >
      {/* Filter bar */}
      <div
        className="filter-bar"
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          gap: '0.5rem',
          marginBottom: '1rem',
          alignItems: 'flex-end',
        }}
      >
        <label style={{ display: 'flex', flexDirection: 'column', fontSize: '0.8em', gap: '0.2em' }}>
          {t('audit.filter.model')}
          <select
            aria-label={t('audit.filter.model')}
            className="select select--compact"
            value={modelId}
            onChange={(e) => { setModelId(e.target.value); resetOffset(); }}
          >
            <option value="">{t('audit.filter.all')}</option>
            {models.map((m) => (
              <option key={m} value={m}>{m}</option>
            ))}
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', fontSize: '0.8em', gap: '0.2em' }}>
          {t('audit.filter.tenant')}
          <select
            aria-label={t('audit.filter.tenant')}
            className="select select--compact"
            value={tenant}
            onChange={(e) => { setTenant(e.target.value); resetOffset(); }}
          >
            <option value="">{t('audit.filter.all')}</option>
            {tenants.map((tn) => (
              <option key={tn} value={tn}>{tn}</option>
            ))}
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', fontSize: '0.8em', gap: '0.2em' }}>
          {t('audit.filter.since')}
          <input
            type="date"
            className="input input--compact"
            value={since}
            onChange={(e) => { setSince(e.target.value); resetOffset(); }}
            aria-label={t('audit.filter.since')}
          />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', fontSize: '0.8em', gap: '0.2em' }}>
          {t('audit.filter.until')}
          <input
            type="date"
            className="input input--compact"
            value={until}
            onChange={(e) => { setUntil(e.target.value); resetOffset(); }}
            aria-label={t('audit.filter.until')}
          />
        </label>

        <Button
          variant="secondary"
          size="sm"
          onClick={() => void refetch()}
          disabled={isFetching}
          aria-label={t('audit.refresh')}
        >
          <IconRefresh />
        </Button>
      </div>

      {isLoading && <LoadingBlock />}

      {isError && (
        <ErrorState
          message={errorMessage(error, t, 'error.inferenceAudit')}
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !isError && events.length === 0 && (
        <EmptyState message={t('audit.inference.empty')} />
      )}

      {events.length > 0 && (
        <>
          <div className="table-wrap" style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('audit.col.seq')}</th>
                  <th scope="col">{t('audit.inference.col.model')}</th>
                  <th scope="col">{t('audit.inference.col.tenant')}</th>
                  <th scope="col">{t('audit.inference.col.apiKey')}</th>
                  <th scope="col">{t('audit.inference.col.latency')}</th>
                  <th scope="col">{t('audit.inference.col.tokens')}</th>
                  <th scope="col">{t('audit.inference.col.status')}</th>
                  <th scope="col">{t('audit.inference.col.time')}</th>
                </tr>
              </thead>
              <tbody>
                {events.map((event) => (
                  <InferenceEventRow key={event.seq} event={event} />
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
              {offset + 1}–{Math.min(offset + PAGE_SIZE, total)} / {total}
            </span>
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <Button
                variant="secondary"
                size="sm"
                disabled={!hasPrev}
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                aria-label={t('audit.page.prev')}
              >
                {t('audit.page.prev')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={!hasNext}
                onClick={() => setOffset(offset + PAGE_SIZE)}
                aria-label={t('audit.page.next')}
              >
                {t('audit.page.next')}
              </Button>
            </div>
          </div>
        </>
      )}
    </Card>
  );
}

function InferenceEventRow({ event }: { event: InferenceAuditEvent }) {
  const time = new Date(event.createdAt).toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
  const statusTone = event.status === 'ok' ? 'success' : 'danger';
  return (
    <tr>
      <td className="muted">{event.seq}</td>
      <td>
        <code className="inline-code" style={{ fontSize: '0.85em' }}>
          {event.modelId}
        </code>
      </td>
      <td>{event.tenant || <span className="muted">—</span>}</td>
      <td>
        <code className="inline-code" style={{ fontSize: '0.85em' }}>
          {event.apiKeyId}
        </code>
      </td>
      <td className="muted">{event.latencyMs}ms</td>
      <td className="muted">
        {event.inputTokens}/{event.outputTokens}
      </td>
      <td>
        <Badge tone={statusTone}>{event.status}</Badge>
      </td>
      <td className="muted" style={{ fontSize: '0.8em', whiteSpace: 'nowrap' }}>
        {time}
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Access Log Tab
// ---------------------------------------------------------------------------

function AccessLogTab() {
  const t = useT();
  const [apiKeyId, setApiKeyId] = useState('');

  const params = useMemo(
    () => ({ limit: 50, ...(apiKeyId ? { apiKeyId } : {}) }),
    [apiKeyId],
  );

  const { data, isLoading, isError, error, isFetching, refetch } = useAccessLog(params);

  const entries = data?.entries ?? [];

  return (
    <Card
      title={t('audit.tab.access')}
      action={
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void refetch()}
          disabled={isFetching}
          aria-label={t('audit.access.refresh')}
        >
          <IconRefresh />
          {t('audit.access.refresh')}
        </Button>
      }
    >
      {/* Filter */}
      <div style={{ marginBottom: '1rem' }}>
        <label
          htmlFor="access-log-filter"
          style={{ fontSize: '0.8em', display: 'block', marginBottom: '0.25em' }}
        >
          {t('audit.access.filter.label')}
        </label>
        <input
          id="access-log-filter"
          type="text"
          className="input input--compact"
          placeholder={t('audit.filter.all')}
          value={apiKeyId}
          onChange={(e) => setApiKeyId(e.target.value)}
          aria-label={t('audit.access.filter.label')}
        />
      </div>

      {isLoading && <LoadingBlock />}

      {isError && (
        <ErrorState
          message={errorMessage(error, t, 'error.accessLog')}
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !isError && entries.length === 0 && (
        <EmptyState message={t('audit.access.empty')} />
      )}

      {entries.length > 0 && (
        <div className="table-wrap" style={{ overflowX: 'auto' }}>
          <table className="table">
            <thead>
              <tr>
                <th scope="col">{t('audit.inference.col.apiKey')}</th>
                <th scope="col">{t('audit.access.col.method')}</th>
                <th scope="col">{t('audit.access.col.path')}</th>
                <th scope="col">{t('audit.access.col.ip')}</th>
                <th scope="col">{t('audit.access.col.agent')}</th>
                <th scope="col">{t('audit.access.col.statusCode')}</th>
                <th scope="col">{t('audit.inference.col.time')}</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <AccessLogRow key={entry.id} entry={entry} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function AccessLogRow({ entry }: { entry: AccessLogEntry }) {
  const time = new Date(entry.requestAt).toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
  const sc = entry.statusCode;
  const statusTone = sc >= 500 ? 'danger' : sc >= 400 ? 'warning' : 'success';
  return (
    <tr>
      <td>
        <code className="inline-code" style={{ fontSize: '0.85em' }}>
          {entry.apiKeyId}
        </code>
      </td>
      <td>
        <Badge tone="neutral">{entry.method}</Badge>
      </td>
      <td>
        <code className="inline-code" style={{ fontSize: '0.85em' }}>
          {entry.path}
        </code>
      </td>
      <td className="muted">{entry.ipPrefix}</td>
      <td className="muted" style={{ fontSize: '0.78em', maxWidth: '14rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {entry.userAgent}
      </td>
      <td>
        <Badge tone={statusTone}>{entry.statusCode}</Badge>
      </td>
      <td className="muted" style={{ fontSize: '0.8em', whiteSpace: 'nowrap' }}>
        {time}
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

export function AuditPage() {
  const t = useT();
  const [tab, setTab] = useState<Tab>('inference');

  return (
    <div className="page">
      <PageHeader title={t('audit.title')} />

      <ChainIntegrityPanel />

      {/* Tab navigation */}
      <div
        role="tablist"
        aria-label={t('audit.title')}
        style={{ display: 'flex', gap: '0.25rem', marginTop: '1.25rem', marginBottom: '0.75rem' }}
      >
        <button
          role="tab"
          aria-selected={tab === 'inference'}
          className={`btn btn--secondary btn--sm${tab === 'inference' ? ' btn--active' : ''}`}
          onClick={() => setTab('inference')}
        >
          {t('audit.tab.inference')}
        </button>
        <button
          role="tab"
          aria-selected={tab === 'access'}
          className={`btn btn--secondary btn--sm${tab === 'access' ? ' btn--active' : ''}`}
          onClick={() => setTab('access')}
        >
          {t('audit.tab.access')}
        </button>
      </div>

      {tab === 'inference' ? <InferenceAuditTab /> : <AccessLogTab />}
    </div>
  );
}
