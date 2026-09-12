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
  Tabs,
  TabPanel,
  type TabItem,
  type Tone,
} from '../components/ui';
import { useT, type TFunc } from '../i18n';
import {
  useBillingForecast,
  useBillingReport,
  useModelAdoption,
  useOrgBilling,
  useTeamBilling,
} from '../hooks/queries';
import { api } from '../api/client';
import { ApiError } from '../api/http';
import { errorMessage } from '../lib/errors';
import type {
  BillingForecastEntry,
  BillingTenantUsage,
  ModelAdoptionSeries,
  OrgBillingReport,
  TeamBillingReport,
  TenantSLAStat,
} from '../api/types';

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
// Model adoption panel — GET /billing/models/adoption time-series.
// Enterprise-gated (billing); 402 shows the same upgrade prompt as the report.
// ---------------------------------------------------------------------------

/** Inline sparkline for a model's per-bucket request counts. */
function Sparkline({ values }: { values: number[] }) {
  if (values.length === 0) return <span className="muted">—</span>;
  const max = Math.max(...values, 1);
  const w = 90;
  const h = 22;
  const step = values.length > 1 ? w / (values.length - 1) : 0;
  const pts = values
    .map((v, i) => `${(i * step).toFixed(1)},${(h - (v / max) * h).toFixed(1)}`)
    .join(' ');
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true" style={{ display: 'block' }}>
      <polyline points={pts} fill="none" stroke="#0d9488" strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

function AdoptionRow({ series }: { series: ModelAdoptionSeries }) {
  const totalRequests = series.buckets.reduce((s, b) => s + b.requests, 0);
  const totalTokens = series.buckets.reduce((s, b) => s + b.tokens_out, 0);
  return (
    <tr>
      <td><code className="inline-code" style={{ fontSize: '0.85em' }}>{series.model_id}</code></td>
      <td>{fmtNum(totalRequests)}</td>
      <td>{fmtNum(totalTokens)}</td>
      <td><Sparkline values={series.buckets.map((b) => b.requests)} /></td>
    </tr>
  );
}

function ModelAdoptionPanel() {
  const t = useT();
  const [window, setWindow] = useState<'daily' | 'weekly'>('daily');
  const { data, isLoading, error } = useModelAdoption({ window, days: 30 });

  if (error instanceof ApiError && error.status === 402) {
    return <EmptyState message={t('chargeback.adoption.enterprise.required')} />;
  }

  const actions = (
    <select
      value={window}
      onChange={(e) => setWindow(e.target.value as 'daily' | 'weekly')}
      className="select"
      aria-label={t('chargeback.adoption.window.label')}
    >
      <option value="daily">{t('chargeback.adoption.window.daily')}</option>
      <option value="weekly">{t('chargeback.adoption.window.weekly')}</option>
    </select>
  );

  return (
    <Card title={t('chargeback.adoption.title')} action={actions}>
      <p style={{ color: 'var(--text-muted)', fontSize: '13px', marginTop: 0 }}>
        {t('chargeback.adoption.subtitle')}
      </p>
      {isLoading ? (
        <LoadingBlock />
      ) : error ? (
        <ErrorState message={errorMessage(error, t, 'error.modelAdoption')} />
      ) : !data || data.series.length === 0 ? (
        <EmptyState message={t('chargeback.adoption.empty')} />
      ) : (
        <div className="table-wrap" style={{ overflowX: 'auto' }}>
          <table className="table" data-testid="adoption-table">
            <thead>
              <tr>
                <th scope="col">{t('chargeback.adoption.col.model')}</th>
                <th scope="col">{t('chargeback.adoption.col.requests')}</th>
                <th scope="col">{t('chargeback.adoption.col.tokensOut')}</th>
                <th scope="col">{t('chargeback.adoption.col.trend')}</th>
              </tr>
            </thead>
            <tbody>
              {data.series.map((s) => (
                <AdoptionRow key={s.model_id} series={s} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// SLA compliance panel — requests the report with a sla_threshold_ms so the
// backend attaches per-tenant compliance stats.
// ---------------------------------------------------------------------------

function slaTone(rate: number): Tone {
  if (rate >= 0.99) return 'success';
  if (rate >= 0.95) return 'warning';
  return 'danger';
}

function SlaCompliancePanel({ days }: { days: number }) {
  const t = useT();
  const [threshold, setThreshold] = useState<number>(2000);
  const { data, isLoading, error } = useBillingReport({ days, slaThresholdMs: threshold });

  if (error instanceof ApiError && error.status === 402) {
    return <EmptyState message={t('chargeback.enterprise.required')} />;
  }

  const stats: TenantSLAStat[] = data?.sla_stats ?? [];

  const actions = (
    <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '13px' }}>
      {t('chargeback.sla.threshold')}
      <input
        type="number"
        min={1}
        step={100}
        value={threshold}
        onChange={(e) => setThreshold(Math.max(1, Number(e.target.value) || 1))}
        className="input input--compact"
        style={{ width: 110 }}
        aria-label={t('chargeback.sla.threshold')}
        data-testid="sla-threshold-input"
      />
    </label>
  );

  return (
    <Card title={t('chargeback.sla.title')} action={actions}>
      <p style={{ color: 'var(--text-muted)', fontSize: '13px', marginTop: 0 }}>
        {t('chargeback.sla.subtitle')}
      </p>
      {isLoading ? (
        <LoadingBlock />
      ) : error ? (
        <ErrorState message={errorMessage(error, t, 'error.billing')} />
      ) : stats.length === 0 ? (
        <EmptyState message={t('chargeback.sla.empty')} />
      ) : (
        <div className="table-wrap" style={{ overflowX: 'auto' }}>
          <table className="table" data-testid="sla-table">
            <thead>
              <tr>
                <th scope="col">{t('chargeback.sla.col.tenant')}</th>
                <th scope="col">{t('chargeback.sla.col.rate')}</th>
                <th scope="col">{t('chargeback.sla.col.threshold')}</th>
              </tr>
            </thead>
            <tbody>
              {stats.map((s, i) => (
                <tr key={`${s.tenant_id}-${i}`}>
                  <td>{s.tenant_id}</td>
                  <td>
                    <Badge tone={slaTone(s.sla_compliance_rate)}>
                      {(s.sla_compliance_rate * 100).toFixed(1)}%
                    </Badge>
                  </td>
                  <td>{fmtNum(s.sla_threshold_ms)} ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Per-org / per-team billing panel.
// ---------------------------------------------------------------------------

function StatRow({ report }: { report: OrgBillingReport | TeamBillingReport }) {
  const t = useT();
  const isOrg = 'teams' in report;
  return (
    <Card>
      <div style={{ display: 'flex', gap: '2rem', flexWrap: 'wrap', padding: '0.5rem 0' }}>
        {!isOrg && (
          <StatTile label={t('chargeback.tenants.stat.requests')} value={fmtNum((report as TeamBillingReport).total_requests)} />
        )}
        <StatTile label={t('chargeback.tenants.stat.tokens')} value={fmtNum(report.total_tokens)} />
        <StatTile label={t('chargeback.tenants.stat.cost')} value={`$${report.total_cost_usd.toFixed(2)}`} />
        {isOrg && (
          <StatTile label={t('chargeback.tenants.stat.teams')} value={(report as OrgBillingReport).teams.length} />
        )}
      </div>
    </Card>
  );
}

function TeamBillingTable({ report }: { report: TeamBillingReport }) {
  const t = useT();
  const rows = report.by_model ?? [];
  if (rows.length === 0) return <EmptyState message={t('chargeback.tenants.empty')} />;
  return (
    <div className="table-wrap" style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th scope="col">{t('chargeback.tenants.col.model')}</th>
            <th scope="col">{t('chargeback.tenants.col.requests')}</th>
            <th scope="col">{t('chargeback.tenants.col.tokens')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={`${row.model_id}-${i}`}>
              <td>{row.model_id}</td>
              <td>{fmtNum(row.request_count)}</td>
              <td>{fmtNum(row.total_tokens)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function OrgBillingTable({ report }: { report: OrgBillingReport }) {
  const t = useT();
  if (report.teams.length === 0) return <EmptyState message={t('chargeback.tenants.empty')} />;
  return (
    <div className="table-wrap" style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th scope="col">{t('chargeback.tenants.col.team')}</th>
            <th scope="col">{t('chargeback.tenants.col.requests')}</th>
            <th scope="col">{t('chargeback.tenants.col.tokens')}</th>
            <th scope="col">{t('chargeback.tenants.col.cost')}</th>
          </tr>
        </thead>
        <tbody>
          {report.teams.map((tm, i) => (
            <tr key={`${tm.team_id}-${i}`}>
              <td>{tm.team_name || tm.team_id}</td>
              <td>{fmtNum(tm.total_requests)}</td>
              <td>{fmtNum(tm.total_tokens)}</td>
              <td>${tm.total_cost_usd.toFixed(2)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Body of the org scope — isolated so its hook only runs when an id is set. */
function OrgBillingBody({ orgId, days, t }: { orgId: string; days: number; t: TFunc }) {
  const { data, isLoading, error } = useOrgBilling(orgId, days);
  if (error instanceof ApiError && error.status === 402) {
    return <EmptyState message={t('chargeback.enterprise.required')} />;
  }
  if (isLoading) return <LoadingBlock />;
  if (error) return <ErrorState message={errorMessage(error, t, 'error.orgBilling')} />;
  if (!data) return null;
  return (
    <>
      <StatRow report={data} />
      <div style={{ marginTop: '1rem' }}>
        <OrgBillingTable report={data} />
      </div>
    </>
  );
}

/** Body of the team scope — isolated so its hook only runs when an id is set. */
function TeamBillingBody({ teamId, days, t }: { teamId: string; days: number; t: TFunc }) {
  const { data, isLoading, error } = useTeamBilling(teamId, days);
  if (error instanceof ApiError && error.status === 402) {
    return <EmptyState message={t('chargeback.enterprise.required')} />;
  }
  if (isLoading) return <LoadingBlock />;
  if (error) return <ErrorState message={errorMessage(error, t, 'error.teamBilling')} />;
  if (!data) return null;
  return (
    <>
      <StatRow report={data} />
      <div style={{ marginTop: '1rem' }}>
        <TeamBillingTable report={data} />
      </div>
    </>
  );
}

function TenantBillingPanel({ days }: { days: number }) {
  const t = useT();
  const [scope, setScope] = useState<'org' | 'team'>('org');
  const [draftId, setDraftId] = useState('');
  const [loadedId, setLoadedId] = useState('');

  function load() {
    setLoadedId(draftId.trim());
  }

  return (
    <Card title={t('chargeback.tenants.title')}>
      <p style={{ color: 'var(--text-muted)', fontSize: '13px', marginTop: 0 }}>
        {t('chargeback.tenants.subtitle')}
      </p>
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', flexWrap: 'wrap', marginBottom: '1rem' }}>
        <label style={{ display: 'flex', flexDirection: 'column', fontSize: '12px', gap: '0.2rem' }}>
          {t('chargeback.tenants.scope.label')}
          <select
            value={scope}
            onChange={(e) => { setScope(e.target.value as 'org' | 'team'); setLoadedId(''); }}
            className="select"
            aria-label={t('chargeback.tenants.scope.label')}
            data-testid="tenants-scope"
          >
            <option value="org">{t('chargeback.tenants.scope.org')}</option>
            <option value="team">{t('chargeback.tenants.scope.team')}</option>
          </select>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', fontSize: '12px', gap: '0.2rem', flex: 1, minWidth: 220 }}>
          {t('chargeback.tenants.id.label')}
          <input
            className="input"
            value={draftId}
            placeholder={t('chargeback.tenants.id.placeholder')}
            onChange={(e) => setDraftId(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') load(); }}
            aria-label={t('chargeback.tenants.id.label')}
            data-testid="tenants-id-input"
          />
        </label>
        <Button variant="primary" onClick={load} disabled={!draftId.trim()} data-testid="tenants-load-btn">
          {t('chargeback.tenants.load')}
        </Button>
      </div>
      {!loadedId ? (
        <EmptyState message={t('chargeback.tenants.prompt')} />
      ) : scope === 'org' ? (
        <OrgBillingBody orgId={loadedId} days={days} t={t} />
      ) : (
        <TeamBillingBody teamId={loadedId} days={days} t={t} />
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

type ChargebackTab = 'usage' | 'adoption' | 'sla' | 'tenants';

export function ChargebackPage() {
  const t = useT();
  const [tab, setTab] = useState<ChargebackTab>('usage');
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

  const periodPicker = (
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
  );

  const pageActions = (
    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
      {periodPicker}
      {tab === 'usage' && (
        <>
          <Button onClick={handleExportCsv} disabled={!report}>
            {t('chargeback.action.exportCsv')}
          </Button>
          <Button onClick={handleExportXlsx} disabled={!report || downloadingXlsx}>
            {downloadingXlsx ? t('chargeback.action.downloading') : t('chargeback.action.exportXlsx')}
          </Button>
          <Button onClick={handleExportPdf} disabled={!report || downloadingPdf}>
            {downloadingPdf ? t('chargeback.action.downloading') : t('chargeback.action.exportPdf')}
          </Button>
        </>
      )}
    </div>
  );

  const tabs: TabItem[] = [
    { id: 'usage', label: t('chargeback.tab.usage') },
    { id: 'adoption', label: t('chargeback.tab.adoption') },
    { id: 'sla', label: t('chargeback.tab.sla') },
    { id: 'tenants', label: t('chargeback.tab.tenants') },
  ];

  return (
    <div className="page">
      <PageHeader
        title={t('chargeback.title')}
        subtitle={t('chargeback.subtitle')}
        actions={pageActions}
      />

      <div style={{ marginBottom: '1rem' }}>
        <Tabs tabs={tabs} active={tab} onChange={(id) => setTab(id as ChargebackTab)} ariaLabel={t('chargeback.title')} />
      </div>

      {tab === 'usage' && (
        <TabPanel id="usage">
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
        </TabPanel>
      )}

      {tab === 'adoption' && (
        <TabPanel id="adoption">
          <ModelAdoptionPanel />
        </TabPanel>
      )}

      {tab === 'sla' && (
        <TabPanel id="sla">
          <SlaCompliancePanel days={days} />
        </TabPanel>
      )}

      {tab === 'tenants' && (
        <TabPanel id="tenants">
          <TenantBillingPanel days={days} />
        </TabPanel>
      )}
    </div>
  );
}
