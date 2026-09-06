// AuditPage — enterprise tamper-evident audit log viewer.
//
// Gated on a valid license with the "audit" feature entitlement.  Without it
// the control plane returns 402 and the page shows a clear "Enterprise license
// required" empty state so the operator knows the feature exists and how to
// enable it.
//
// Columns: Seq | Timestamp | Actor | Action | Target | Details
// Action badges are colour-coded: created/minted → success, deleted → neutral,
// fleet events → warning.  The chain verification badge is shown in the page
// header and is visually prominent — a broken chain is an operator alert.
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
  type Tone,
} from '../components/ui';
import { IconRefresh, IconShield } from '../components/icons';
import { useAuditLog } from '../hooks/queries';
import { useT } from '../i18n';
import { ApiError } from '../api/http';
import { errorMessage } from '../lib/errors';
import type { AuditEntry, AuditChainVerification } from '../api/types';

// ---------------------------------------------------------------------------
// Action badge helpers
// ---------------------------------------------------------------------------

function actionTone(action: string): Tone {
  if (action.endsWith('.created') || action.endsWith('.minted')) return 'success';
  if (action.endsWith('.deleted') || action.endsWith('.decommissioned')) return 'neutral';
  if (action.startsWith('fleet.')) return 'warning';
  return 'info';
}

function ActionBadge({ action }: { action: string }) {
  return <Badge tone={actionTone(action)}>{action}</Badge>;
}

// ---------------------------------------------------------------------------
// Chain verification badge
// ---------------------------------------------------------------------------

function ChainBadge({ chain }: { chain: AuditChainVerification }) {
  const t = useT();
  if (chain.verified) {
    return (
      <Badge tone="success">
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.3em' }}>
          <IconShield width={14} height={14} strokeWidth={2} />
          {t('audit.chain.verified')}
        </span>
      </Badge>
    );
  }
  const seq = chain.break?.seq ?? '?';
  return (
    <Badge tone="warning">
      {t('audit.chain.broken', { seq: String(seq) })}
    </Badge>
  );
}

// ---------------------------------------------------------------------------
// Table row
// ---------------------------------------------------------------------------

function EntryRow({ entry }: { entry: AuditEntry }) {
  const ts = new Date(entry.createdAt).toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
  const detailText = entry.details
    ? Object.entries(entry.details)
        .map(([k, v]) => `${k}=${v}`)
        .join(', ')
    : '';

  return (
    <tr>
      <td className="muted">{entry.seq}</td>
      <td>
        <code className="inline-code" style={{ fontSize: '0.78em' }}>
          {ts}
        </code>
      </td>
      <td>{entry.actor}</td>
      <td>
        <ActionBadge action={entry.action} />
      </td>
      <td>
        <code className="inline-code">{entry.target}</code>
      </td>
      <td className="muted" style={{ fontSize: '0.8em' }}>
        {detailText}
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

const LIMIT_OPTIONS = [50, 100, 200] as const;

export function AuditPage() {
  const t = useT();
  const [limit, setLimit] = useState<(typeof LIMIT_OPTIONS)[number]>(100);
  const { data, isLoading, isError, error, refetch, isFetching } = useAuditLog(limit);

  // 402 = no enterprise license
  const is402 = isError && error instanceof ApiError && error.status === 402;

  const pageActions = (
    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
      <label
        htmlFor="audit-limit"
        className="muted"
        style={{ fontSize: '0.85em', whiteSpace: 'nowrap' }}
      >
        {t('audit.limit')}
      </label>
      <select
        id="audit-limit"
        className="select select--compact"
        value={limit}
        onChange={(e) => setLimit(Number(e.target.value) as (typeof LIMIT_OPTIONS)[number])}
      >
        {LIMIT_OPTIONS.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => void refetch()}
        disabled={isFetching}
        aria-label={t('audit.refresh')}
        title={t('audit.refresh')}
      >
        <IconRefresh />
      </Button>
    </div>
  );

  return (
    <div className="page">
      <PageHeader
        title={t('audit.title')}
        actions={
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
            {data && <ChainBadge chain={data.chain} />}
            {pageActions}
          </div>
        }
      />

      <Card title={t('audit.title')}>
        {isLoading && <LoadingBlock />}

        {is402 && (
          <EmptyState
            icon={<IconShield />}
            message={t('audit.enterprise.required')}
            action={
              <a
                href="https://purser.dev/docs/enterprise/audit-log"
                target="_blank"
                rel="noopener noreferrer"
                className="btn btn--secondary btn--sm"
              >
                {t('audit.docs.link')}
              </a>
            }
          />
        )}

        {isError && !is402 && (
          <ErrorState
            message={errorMessage(error, t, 'error.audit')}
            onRetry={() => void refetch()}
          />
        )}

        {data && data.entries.length === 0 && (
          <EmptyState message={t('audit.empty')} />
        )}

        {data && data.entries.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('audit.col.seq')}</th>
                  <th scope="col">{t('audit.col.timestamp')}</th>
                  <th scope="col">{t('audit.col.actor')}</th>
                  <th scope="col">{t('audit.col.action')}</th>
                  <th scope="col">{t('audit.col.target')}</th>
                  <th scope="col">{t('audit.col.details')}</th>
                </tr>
              </thead>
              <tbody>
                {data.entries.map((entry) => (
                  <EntryRow key={entry.seq} entry={entry} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
