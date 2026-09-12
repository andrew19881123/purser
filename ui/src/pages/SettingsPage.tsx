import { Link } from 'react-router-dom';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
} from '../components/ui';
import {
  useApiKeys,
  useEnterpriseStatus,
  useUsageSummary,
} from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { formatTokenCount } from '../lib/format';
import { errorMessage } from '../lib/errors';

/** Above-the-fold quick stats: edition, active key count, total requests this month. */
function QuickStatsBar() {
  const { data: keys } = useApiKeys();
  const { data: enterprise } = useEnterpriseStatus();
  const { data: usage } = useUsageSummary();

  const activeKeys = (keys ?? []).filter((k) => !k.revoked).length;
  const edition = enterprise?.edition ?? null;
  const totalRequests =
    usage && usage.tenants.length > 0
      ? usage.tenants.reduce((sum, row) => sum + row.totalRequests, 0)
      : null;

  return (
    <div className="stat-grid" style={{ marginBottom: '1rem' }}>
      {edition !== null && (
        <div className="stat">
          <span className="stat__value">{edition === 'enterprise' ? 'Enterprise' : 'Community'}</span>
          <span className="stat__label">Edition</span>
        </div>
      )}
      <div className="stat">
        <span className="stat__value">{activeKeys}</span>
        <span className="stat__label">Active API keys</span>
      </div>
      {totalRequests !== null && (
        <div className="stat">
          <span className="stat__value">{totalRequests.toLocaleString()}</span>
          <span className="stat__label">Requests this month</span>
        </div>
      )}
    </div>
  );
}

function UsageSummaryCard({ t }: { t: TFunc }) {
  const { data, isLoading, isError, error, refetch } = useUsageSummary();

  return (
    <Card title={t('settings.usage.summary.title')}>
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState message={errorMessage(error, t, 'error.apikeys')} onRetry={() => refetch()} />
      )}
      {data && data.tenants.length === 0 && (
        <EmptyState message={t('settings.usage.summary.empty')} />
      )}
      {data && data.tenants.length > 0 && (
        <div className="table-wrap">
          <table className="table" data-testid="usage-summary-table">
            <thead>
              <tr>
                <th scope="col">{t('settings.usage.col.tenant')}</th>
                <th scope="col">{t('settings.usage.col.requests')}</th>
                <th scope="col">{t('settings.usage.col.inputTokens')}</th>
                <th scope="col">{t('settings.usage.col.outputTokens')}</th>
              </tr>
            </thead>
            <tbody>
              {data.tenants.map((row) => (
                <tr key={row.tenant}>
                  <th scope="row">{row.tenant}</th>
                  <td>{row.totalRequests.toLocaleString()}</td>
                  <td>{formatTokenCount(row.inputTokens)}</td>
                  <td>{formatTokenCount(row.outputTokens)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function LicenseCard({ t }: { t: TFunc }) {
  const { data, isLoading, isError } = useEnterpriseStatus();

  if (isLoading) return <Card title={t('settings.license.title')}><LoadingBlock /></Card>;

  // 404 / API error or no license data → fall through to MIT Core community mode gracefully.
  const isCommunity = isError || !data || data.edition === 'community';
  const isExpired = data?.expires ? new Date(data.expires) < new Date() : false;

  // Days remaining until expiry (only meaningful for non-expired enterprise licenses).
  const daysRemaining =
    data?.expires && !isExpired
      ? Math.ceil((new Date(data.expires).getTime() - Date.now()) / 86_400_000)
      : null;

  return (
    <Card title={t('settings.license.title')}>
      <div className="license-section" data-testid="license-section">
        <div className="license-row">
          {isCommunity ? (
            <span data-testid="community-badge">
              <Badge tone="neutral">{t('settings.license.edition.community')}</Badge>
            </span>
          ) : (
            <span data-testid="enterprise-badge">
              <Badge tone="success">{t('settings.license.edition.enterprise')}</Badge>
            </span>
          )}
          {!isCommunity && isExpired && (
            <span data-testid="expired-badge">
              <Badge tone="danger">{t('settings.license.expired')}</Badge>
            </span>
          )}
        </div>

        {isCommunity ? (
          <p className="muted" data-testid="mit-core-desc">
            {t('settings.license.community.desc')}{' '}
            <a href="/docs/enterprise/overview" className="link">
              {t('settings.license.community.link')}
            </a>
          </p>
        ) : (
          <dl className="license-details">
            <dt>{t('settings.license.licensee')}</dt>
            <dd data-testid="license-licensee">{data!.licensee}</dd>

            <dt>{t('settings.license.features')}</dt>
            <dd className="feature-badges" data-testid="feature-badges">
              {data!.features.length === 0 ? (
                <span className="muted">{t('settings.license.no.features')}</span>
              ) : (
                data!.features.map((f) => (
                  <Badge key={f} tone="info">
                    {f}
                  </Badge>
                ))
              )}
            </dd>

            {data?.expires && (
              <>
                <dt>{t('settings.license.expires')}</dt>
                <dd>
                  <span className={isExpired ? 'text--danger' : undefined} data-testid="license-expiry">
                    {new Date(data.expires).toLocaleDateString()}
                  </span>
                  {daysRemaining !== null && (
                    <span className="muted" data-testid="days-remaining">
                      {' '}({daysRemaining} {t('settings.license.daysRemaining')})
                    </span>
                  )}
                </dd>
              </>
            )}
          </dl>
        )}
      </div>
    </Card>
  );
}

export function SettingsPage() {
  const t = useT();

  return (
    <div className="page">
      <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle')} />

      <QuickStatsBar />

      {/* API Keys card — links to the dedicated /api-keys page */}
      <Card title={t('settings.keys.title')}>
        <p style={{ marginBottom: '0.75rem' }}>{t('settings.apikeys.manage.desc')}</p>
        <Link
          to="/api-keys"
          className="btn btn--primary btn--sm link-btn"
          data-testid="manage-api-keys-link"
        >
          {t('settings.apikeys.manage')}
        </Link>
      </Card>

      <UsageSummaryCard t={t} />

      <LicenseCard t={t} />
    </div>
  );
}
