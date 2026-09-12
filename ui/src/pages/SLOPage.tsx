// Route: /slo — add to router.tsx
//
// SLOPage — per-model TTFT/TBT compliance tracking.
//
// Visual identity: traffic-light aesthetic. Breached models demand attention
// via a red pulsing badge — the signature element. "Met" is calm green.
// Summary KPI row gives an immediate at-a-glance status.
//
// Layout:
//   ┌─ PageHeader ────────────────────────────────────────────────────────┐
//   │  "SLO Contracts" / subtitle    window pills [1h|6h|24h|7d]         │
//   └─────────────────────────────────────────────────────────────────────┘
//   ┌─ KPI summary row ──────────────────────────────────────────────────┐
//   │  [Total] [Met] [Breached] [No data]                                │
//   └─────────────────────────────────────────────────────────────────────┘
//   ┌─ Compliance table ──────────────────────────────────────────────────┐
//   │  Model | TTFT target | TTFT compliance | TBT target | Status | Req │
//   └─────────────────────────────────────────────────────────────────────┘
import { useState } from 'react';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
} from '../components/ui';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import { ApiError } from '../api/http';
import { useSloComplianceFull } from '../hooks/queries';
import type { SloModelEntry } from '../api/types';

// ---------------------------------------------------------------------------
// Pulse animation for breached badges
// ---------------------------------------------------------------------------

const PULSE_CSS = `
@keyframes slo-pulse {
  0%, 100% { opacity: 1; }
  50%       { opacity: 0.55; }
}
`;

function PulseStyle() {
  return <style>{PULSE_CSS}</style>;
}

// ---------------------------------------------------------------------------
// Window selector (pill-style segmented control)
// ---------------------------------------------------------------------------

const WINDOWS: { label: string; hours: number }[] = [
  { label: '1h',  hours: 1  },
  { label: '6h',  hours: 6  },
  { label: '24h', hours: 24 },
  { label: '7d',  hours: 168 },
];

function WindowSelector({
  value,
  onChange,
}: {
  value: number;
  onChange: (h: number) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Time window"
      style={{
        display: 'inline-flex',
        gap: '2px',
        background: 'var(--surface-2, var(--bg))',
        border: '1px solid var(--border)',
        borderRadius: '6px',
        padding: '2px',
      }}
    >
      {WINDOWS.map((w) => {
        const active = value === w.hours;
        return (
          <button
            key={w.hours}
            type="button"
            onClick={() => onChange(w.hours)}
            aria-pressed={active}
            style={{
              padding: '4px 12px',
              borderRadius: '4px',
              border: 'none',
              cursor: 'pointer',
              fontWeight: active ? 600 : 400,
              fontSize: '0.82em',
              background: active ? 'var(--accent)' : 'transparent',
              color: active ? '#fff' : 'var(--text-muted, var(--text))',
              transition: 'background 0.15s, color 0.15s',
            }}
          >
            {w.label}
          </button>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// KPI summary tile
// ---------------------------------------------------------------------------

function KpiTile({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: 'success' | 'danger' | 'neutral' | 'normal';
}) {
  const valueColor =
    tone === 'success' ? 'var(--success-fg)'
    : tone === 'danger'  ? 'var(--danger-fg)'
    : tone === 'neutral' ? 'var(--text-muted, var(--muted))'
    : 'var(--text)';

  return (
    <div
      style={{
        flex: '1 1 0',
        minWidth: '80px',
        padding: '0.85rem 1rem',
        background: 'var(--surface-2, var(--bg))',
        border: '1px solid var(--border)',
        borderRadius: 'var(--radius)',
        display: 'flex',
        flexDirection: 'column',
        gap: '0.2rem',
      }}
    >
      <span style={{ fontSize: '1.6rem', fontWeight: 700, lineHeight: 1, color: valueColor }}>
        {value}
      </span>
      <span style={{ fontSize: '0.78em', color: 'var(--text-muted, var(--muted))', lineHeight: 1.3 }}>
        {label}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Compliance status badge
// ---------------------------------------------------------------------------

function SloStatusBadge({ status }: { status: SloModelEntry['status'] }) {
  const t = useT();

  if (status === 'breached') {
    return (
      <span
        className="badge badge--danger"
        style={{ animation: 'slo-pulse 1.8s ease-in-out infinite' }}
        role="status"
      >
        {t('slo.status.breached')}
      </span>
    );
  }
  if (status === 'met') {
    return <Badge tone="success">{t('slo.status.met')}</Badge>;
  }
  return <Badge tone="neutral">{t('slo.status.insufficientData')}</Badge>;
}

// ---------------------------------------------------------------------------
// Compliance table row
// ---------------------------------------------------------------------------

function ComplianceRow({ model }: { model: SloModelEntry }) {
  const ttftPct = model.actual.ttft_compliance !== null
    ? `${(model.actual.ttft_compliance * 100).toFixed(1)}%`
    : '—';
  const targetPct = `${(model.slo.target_compliance * 100).toFixed(0)}%`;

  return (
    <tr>
      <td>
        <code className="inline-code" style={{ fontSize: '0.875em' }}>
          {model.model_id}
        </code>
      </td>
      <td className="muted">{model.slo.ttft_ms.toLocaleString()}</td>
      <td>
        {model.actual.ttft_compliance !== null ? (
          <span style={{
            fontWeight: 600,
            color: model.status === 'breached' ? 'var(--danger-fg)' : model.status === 'met' ? 'var(--success-fg)' : 'var(--text)',
          }}>
            {ttftPct}
          </span>
        ) : (
          <span className="muted">—</span>
        )}
      </td>
      <td className="muted">{model.slo.tbt_ms.toLocaleString()}</td>
      <td className="muted">{targetPct}</td>
      <td><SloStatusBadge status={model.status} /></td>
      <td className="muted">{model.actual.request_count.toLocaleString()}</td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Enterprise gate (in case the SLO endpoint gets gated in future releases)
// ---------------------------------------------------------------------------

function isLicenseRequired(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  const body = error.body as Record<string, unknown> | null | undefined;
  if (!body || typeof body !== 'object') return false;
  const errField = body.error as Record<string, unknown> | null | undefined;
  if (!errField || typeof errField !== 'object') return false;
  return errField.type === 'license_required';
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

export function SLOPage() {
  const t = useT();
  const [windowHours, setWindowHours] = useState(24);
  const { data, isLoading, isError, error, refetch } = useSloComplianceFull(windowHours);

  const models = data?.models ?? [];
  const metCount     = models.filter((m) => m.status === 'met').length;
  const breachedCount = models.filter((m) => m.status === 'breached').length;
  const noDataCount  = models.filter((m) => m.status === 'insufficient_data').length;

  return (
    <div className="page">
      <PulseStyle />

      <PageHeader
        title={t('slo.contracts.title')}
        subtitle={t('slo.contracts.subtitle')}
        actions={
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
            <WindowSelector value={windowHours} onChange={setWindowHours} />
            <a
              href="https://andrew19881123.github.io/purser/enterprise/slo/"
              target="_blank"
              rel="noreferrer"
              style={{ fontSize: '0.875em', color: 'var(--accent)', textDecoration: 'none', fontWeight: 500 }}
            >
              {t('slo.configLink')}
            </a>
          </div>
        }
      />

      {/* KPI summary row */}
      <div
        data-testid="slo-summary"
        style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', marginBottom: '1rem' }}
      >
        <KpiTile label={t('slo.summary.total')}   value={models.length}   tone="normal"  />
        <KpiTile label={t('slo.summary.met')}      value={metCount}        tone="success" />
        <KpiTile label={t('slo.summary.breached')} value={breachedCount}   tone="danger"  />
        <KpiTile label={t('slo.summary.noData')}   value={noDataCount}     tone="neutral" />
      </div>

      <Card>
        {isLoading && <LoadingBlock />}

        {isError && isLicenseRequired(error) && (
          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: '10px',
              background: 'var(--info-bg)',
              border: '1px solid color-mix(in srgb, var(--info-fg) 25%, transparent)',
              borderRadius: 'var(--radius)',
              padding: '20px 24px',
            }}
            role="status"
          >
            <strong style={{ color: 'var(--info-fg)' }}>{t('slo.enterprise.title')}</strong>
            <p style={{ margin: 0, fontSize: '0.9em' }}>{t('slo.enterprise.desc')}</p>
            <a
              href="https://andrew19881123.github.io/purser/enterprise/licensing/"
              target="_blank"
              rel="noreferrer"
              style={{ color: 'var(--info-fg)', fontWeight: 600, fontSize: '0.875em' }}
            >
              {t('policies.enterprise.link')}
            </a>
          </div>
        )}

        {isError && !isLicenseRequired(error) && (
          <ErrorState
            message={errorMessage(error, t, 'error.slo')}
            onRetry={() => void refetch()}
          />
        )}

        {!isLoading && !isError && models.length === 0 && (
          <EmptyState
            title={t('slo.contracts.empty.title')}
            message={t('slo.contracts.empty.msg')}
          />
        )}

        {models.length > 0 && (
          <div className="table-wrap" style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('slo.col.model')}</th>
                  <th scope="col">{t('slo.col.ttftTarget')}</th>
                  <th scope="col">{t('slo.col.ttftCompliance')}</th>
                  <th scope="col">{t('slo.col.tbtTarget')}</th>
                  <th scope="col">{t('slo.col.targetCompliance')}</th>
                  <th scope="col">{t('slo.col.status')}</th>
                  <th scope="col">{t('slo.col.requests')}</th>
                </tr>
              </thead>
              <tbody>
                {models.map((m) => (
                  <ComplianceRow key={m.model_id} model={m} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* "Configure SLOs" hint at the bottom when data exists */}
      {!isLoading && !isError && models.length > 0 && (
        <p style={{ fontSize: '0.82em', color: 'var(--text-muted, var(--muted))', marginTop: '0.5rem' }}>
          SLO thresholds are configured in{' '}
          <a
            href="https://andrew19881123.github.io/purser/enterprise/slo/"
            target="_blank"
            rel="noreferrer"
            style={{ color: 'var(--accent)' }}
          >
            purser.yaml
          </a>.
        </p>
      )}
    </div>
  );
}

// Also exported as SloPage for backwards compat with any existing import
export { SLOPage as SloPage };

// Add a named export using the window selector for test access
export { WindowSelector };
export { KpiTile };
