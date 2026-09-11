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
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
  type Tone,
} from '../components/ui';
import { useT } from '../i18n';
import { useBillingForecast, useBillingReport } from '../hooks/queries';
import { api } from '../api/client';
import { ApiError } from '../api/http';
import { errorMessage } from '../lib/errors';
import type { BillingForecastEntry, BillingTenantUsage } from '../api/types';

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
// Forecast color logic
// ---------------------------------------------------------------------------

function forecastTone(entry: BillingForecastEntry): Tone {
  if (entry.days_until_exhaustion !== null && entry.days_until_exhaustion < 7) return 'danger';
  if (entry.budget_monthly_usd > 0 && entry.projected_monthly_usd > entry.budget_monthly_usd * 0.8) return 'warning';
  return 'success';
}

// ---------------------------------------------------------------------------
// Forecast card
// ---------------------------------------------------------------------------

function ForecastCard() {
  const t = useT();
  const { data, isLoading, error } = useBillingForecast();

  // Enterprise gate: silently hide when 402 (not licensed) or not loaded yet.
  if (error instanceof ApiError && error.status === 402) return null;
  if (!isLoading && !data) return null;

  return (
    <Card title={t('chargeback.forecast.title')}>
      {isLoading && <LoadingBlock />}
      {error && !(error instanceof ApiError) && (
        <ErrorState message={errorMessage(error, t, 'error.billingForecast')} />
      )}
      {data && data.entries.length === 0 && (
        <EmptyState message={t('chargeback.forecast.empty')} />
      )}
      {data && data.entries.length > 0 && (
        <div className="table-wrap">
          <table className="table" data-testid="forecast-table">
            <thead>
              <tr>
                <th scope="col">{t('chargeback.forecast.col.team')}</th>
                <th scope="col">{t('chargeback.forecast.col.burnRate')}</th>
                <th scope="col">{t('chargeback.forecast.col.projected')}</th>
                <th scope="col">{t('chargeback.forecast.col.daysLeft')}</th>
              </tr>
            </thead>
            <tbody>
              {data.entries.map((entry, i) => {
                const tone = forecastTone(entry);
                return (
                  <tr key={`${entry.org_id}-${entry.team_id}-${i}`}>
                    <td>{entry.team_id || entry.org_id}</td>
                    <td>${entry.burn_rate_daily_usd.toFixed(2)}</td>
                    <td>
                      <Badge tone={tone}>
                        ${entry.projected_monthly_usd.toFixed(2)}
                      </Badge>
                    </td>
                    <td>
                      {entry.days_until_exhaustion !== null
                        ? <Badge tone={tone}>{entry.days_until_exhaustion}</Badge>
                        : <span className="muted">∞</span>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ChargebackPage() {
  const t = useT();
  const [days, setDays] = useState<number>(30);
  const [downloadingXlsx, setDownloadingXlsx] = useState(false);
  const [downloadingPdf, setDownloadingPdf] = useState(false);

  const { data: report, isLoading, error } = useBillingReport({ days });

  function triggerDownload(url: string, filename: string) {
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  function handleExportCsv() {
    const end = new Date().toISOString();
    const start = new Date(Date.now() - days * 86400000).toISOString();
    const url = api.getBillingCsvUrl(start, end);
    triggerDownload(url, `billing-report-${days}d.csv`);
  }

  function handleExportXlsx() {
    setDownloadingXlsx(true);
    const end = new Date().toISOString();
    const start = new Date(Date.now() - days * 86400000).toISOString();
    const url = api.getBillingXlsxUrl(start, end);
    triggerDownload(url, `billing-report-${days}d.xlsx`);
    setTimeout(() => setDownloadingXlsx(false), 1500);
  }

  function handleExportPdf() {
    setDownloadingPdf(true);
    const end = new Date().toISOString();
    const start = new Date(Date.now() - days * 86400000).toISOString();
    const url = api.getBillingPdfUrl(start, end);
    triggerDownload(url, `billing-report-${days}d.pdf`);
    setTimeout(() => setDownloadingPdf(false), 1500);
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
      <Button onClick={handleExportXlsx} disabled={!report || downloadingXlsx}>
        {downloadingXlsx ? t('chargeback.action.downloading') : t('chargeback.action.exportXlsx')}
      </Button>
      <Button onClick={handleExportPdf} disabled={!report || downloadingPdf}>
        {downloadingPdf ? t('chargeback.action.downloading') : t('chargeback.action.exportPdf')}
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

      {/* Spending forecast — enterprise feature; hidden when 402 */}
      <ForecastCard />
    </div>
  );
}
