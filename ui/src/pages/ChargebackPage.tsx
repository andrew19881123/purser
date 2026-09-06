// ChargebackPage — multi-tenancy billing report (FinOps).
//
// Enterprise-gated: requires the "billing" feature.  Without a valid license
// the control plane returns 402 and this page shows an upgrade prompt.
//
// Features:
//   - Period picker (7 / 30 / 90 days)
//   - Summary stats row (total requests, total tokens, active tenants)
//   - Usage table grouped by tenant+model, sorted by total_tokens DESC
//   - CSV export button (direct download from the API endpoint)
import { useState } from 'react';
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
} from '../components/ui';
import { useT } from '../i18n';
import { useBillingReport } from '../hooks/queries';
import { api } from '../api/client';
import { ApiError } from '../api/http';
import { errorMessage } from '../lib/errors';
import type { BillingTenantUsage } from '../api/types';

// ---------------------------------------------------------------------------
// Period picker
// ---------------------------------------------------------------------------

const PERIOD_OPTIONS = [
  { label: 'Last 7 days', days: 7 },
  { label: 'Last 30 days', days: 30 },
  { label: 'Last 90 days', days: 90 },
] as const;

// ---------------------------------------------------------------------------
// Stat tile
// ---------------------------------------------------------------------------

function StatTile({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="stat-tile">
      <p className="stat-tile__label">{label}</p>
      <p className="stat-tile__value">{value}</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Number formatters
// ---------------------------------------------------------------------------

function fmtNum(n: number): string {
  return new Intl.NumberFormat().format(n);
}

// ---------------------------------------------------------------------------
// Usage table
// ---------------------------------------------------------------------------

function UsageTable({ rows }: { rows: BillingTenantUsage[] }) {
  const t = useT();
  if (rows.length === 0) {
    return <EmptyState message={t('chargeback.empty')} />;
  }
  return (
    <div className="table-wrap" style={{ overflowX: 'auto' }}>
      <table className="data-table">
        <thead>
          <tr>
            <th title={t('chargeback.col.tenant.hint')}>{t('chargeback.col.tenant')}</th>
            <th title={t('chargeback.col.model.hint')}>{t('chargeback.col.model')}</th>
            <th title={t('chargeback.col.requests.hint')}>{t('chargeback.col.requests')}</th>
            <th title={t('chargeback.col.promptTokens.hint')}>{t('chargeback.col.promptTokens')}</th>
            <th title={t('chargeback.col.completionTokens.hint')}>{t('chargeback.col.completionTokens')}</th>
            <th title={t('chargeback.col.avgLatency.hint')}>{t('chargeback.col.avgLatency')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={`${row.tenant_id}-${row.model_id}-${i}`}>
              <td>{row.tenant_id}</td>
              <td>{row.model_id}</td>
              <td>{fmtNum(row.request_count)}</td>
              <td>{fmtNum(row.prompt_tokens)}</td>
              <td>{fmtNum(row.completion_tokens)}</td>
              <td>{row.avg_latency_ms.toFixed(1)} ms</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ChargebackPage() {
  const t = useT();
  const [days, setDays] = useState<number>(30);

  const { data: report, isLoading, error } = useBillingReport({ days });

  function handleExportCsv() {
    const end = new Date().toISOString();
    const start = new Date(Date.now() - days * 86400000).toISOString();
    const url = api.getBillingCsvUrl(start, end);
    const a = document.createElement('a');
    a.href = url;
    a.download = `billing-report-${days}d.csv`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  // Enterprise gate: 402 → show upgrade prompt.
  if (error instanceof ApiError && error.status === 402) {
    return (
      <div className="page">
        <PageHeader
          title={t('chargeback.title')}
          subtitle={t('chargeback.subtitle')}
        />
        <EmptyState message={t('chargeback.enterprise.required')} />
      </div>
    );
  }

  const pageActions = (
    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
      <select
        value={days}
        onChange={(e) => setDays(Number(e.target.value))}
        className="select"
        aria-label={t('chargeback.period.label')}
      >
        {PERIOD_OPTIONS.map((o) => (
          <option key={o.days} value={o.days}>
            {o.label}
          </option>
        ))}
      </select>
      <Button onClick={handleExportCsv} disabled={!report}>
        {t('chargeback.action.exportCsv')}
      </Button>
    </div>
  );

  return (
    <div className="page">
      <PageHeader
        title={t('chargeback.title')}
        subtitle={t('chargeback.subtitle')}
        actions={pageActions}
      />

      {/* Summary stats */}
      {report && (
        <div style={{ marginBottom: '1rem' }}>
          <Card>
            <div style={{ display: 'flex', gap: '2rem', flexWrap: 'wrap', padding: '0.5rem 0' }}>
              <StatTile label={t('chargeback.stat.totalRequests')} value={fmtNum(report.total_requests)} />
              <StatTile label={t('chargeback.stat.totalTokens')} value={fmtNum(report.total_tokens)} />
              <StatTile
                label={t('chargeback.stat.activeTenants')}
                value={new Set(report.tenants.map((tu) => tu.tenant_id)).size}
              />
            </div>
          </Card>
        </div>
      )}

      {/* Usage table */}
      <Card>
        {isLoading ? (
          <LoadingBlock />
        ) : error ? (
          <ErrorState message={errorMessage(error, t, 'error.billing')} />
        ) : report ? (
          <UsageTable rows={report.tenants} />
        ) : null}
      </Card>
    </div>
  );
}
