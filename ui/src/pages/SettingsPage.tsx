import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  CopyButton,
  EmptyState,
  ErrorState,
  Field,
  LoadingBlock,
  Meter,
  Modal,
  PageHeader,
  useFieldId,
  type Tone,
} from '../components/ui';
import {
  useApiKeys,
  useCreateApiKey,
  useEnterpriseStatus,
  useKeyUsage,
  useRevokeApiKey,
  useUsageSummary,
} from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { formatTokenCount, relativeTime } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { ApiKey, ApiKeyRole, ApiKeyWithSecret } from '../api/types';

const ROLE_TONES: Record<ApiKeyRole, Tone> = {
  admin: 'warning',
  viewer: 'info',
  inference: 'success',
};

function RoleBadge({ role, t }: { role: ApiKeyRole; t: TFunc }) {
  const key = `settings.role.${role}` as const;
  return <Badge tone={ROLE_TONES[role] ?? 'neutral'}>{t(key)}</Badge>;
}

function CreateKeyModal({
  onClose,
  onCreated,
  t,
}: {
  onClose: () => void;
  onCreated: (k: ApiKeyWithSecret) => void;
  t: TFunc;
}) {
  const create = useCreateApiKey();
  const [name, setName] = useState('');
  const [team, setTeam] = useState('');
  const [quota, setQuota] = useState('');
  const [role, setRole] = useState<ApiKeyRole>('admin');
  const nameId = useFieldId('kname');
  const teamId = useFieldId('kteam');
  const quotaId = useFieldId('kquota');
  const roleId = useFieldId('krole');

  const submit = () => {
    if (!name.trim() || !team.trim()) return;
    create.mutate(
      { name: name.trim(), team: team.trim(), monthlyQuota: quota ? Number(quota) : null, role },
      { onSuccess: (k) => onCreated(k) },
    );
  };

  return (
    <Modal
      title={t('settings.create.title')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t('action.cancel')}
          </Button>
          <Button variant="primary" onClick={submit} disabled={create.isPending || !name.trim() || !team.trim()}>
            {t('settings.create.submit')}
          </Button>
        </>
      }
    >
      <Field label={t('settings.create.name')} htmlFor={nameId}>
        <input id={nameId} className="input" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label={t('settings.create.team')} htmlFor={teamId}>
        <input id={teamId} className="input" value={team} onChange={(e) => setTeam(e.target.value)} />
      </Field>
      <Field
        label={t('settings.create.role')}
        htmlFor={roleId}
        hint={t(`settings.role.${role}.hint`)}
      >
        <select
          id={roleId}
          className="input"
          value={role}
          onChange={(e) => setRole(e.target.value as ApiKeyRole)}
        >
          <option value="admin">{t('settings.role.admin')}</option>
          <option value="viewer">{t('settings.role.viewer')}</option>
          <option value="inference">{t('settings.role.inference')}</option>
        </select>
      </Field>
      <Field label={t('settings.create.quota')} htmlFor={quotaId} hint={t('settings.create.quotaHint')}>
        <input
          id={quotaId}
          className="input"
          type="number"
          min={0}
          value={quota}
          onChange={(e) => setQuota(e.target.value)}
        />
      </Field>
    </Modal>
  );
}

function CreatedKeyModal({ keyData, onClose, t }: { keyData: ApiKeyWithSecret; onClose: () => void; t: TFunc }) {
  return (
    <Modal
      title={t('settings.created.title')}
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t('action.close')}
        </Button>
      }
    >
      <div className="notice notice--warning" role="alert">
        {t('settings.created.warning')}
      </div>
      <div className="token-row">
        <code className="token">{keyData.secret}</code>
        <CopyButton value={keyData.secret} />
      </div>
    </Modal>
  );
}

/** Sub-component so useKeyUsage is called once per row, avoiding hook-in-loop. */
function KeyTokenUsageCell({ keyId, t }: { keyId: string; t: TFunc }) {
  const { data, isLoading, isError } = useKeyUsage(keyId);
  if (isLoading) return <span className="muted">{t('settings.usage.loading')}</span>;
  if (isError || !data) return <span className="muted">{t('settings.usage.error')}</span>;
  return (
    <span className="token-usage" data-testid="key-token-usage">
      {formatTokenCount(data.inputTokens)} in / {formatTokenCount(data.outputTokens)} out
    </span>
  );
}

function KeyRow({ apiKey, t }: { apiKey: ApiKey; t: TFunc }) {
  const revoke = useRevokeApiKey();
  const role: ApiKeyRole = apiKey.role ?? 'admin';
  const [showRevokeModal, setShowRevokeModal] = useState(false);
  return (
    <>
      <tr className={apiKey.revoked ? 'row--muted' : undefined}>
        <th scope="row">{apiKey.name}</th>
        <td>{apiKey.team}</td>
        <td>
          <code className="inline-code">{apiKey.prefix}…</code>
        </td>
        <td>
          <RoleBadge role={role} t={t} />
        </td>
        <td className="usage-cell">
          {apiKey.monthlyQuota === null ? (
            <span className="muted">{t('settings.usage.unlimited')}</span>
          ) : (
            <Meter used={apiKey.usedThisMonth} total={apiKey.monthlyQuota} label={apiKey.name} unit="req" />
          )}
        </td>
        <td>
          <KeyTokenUsageCell keyId={apiKey.id} t={t} />
        </td>
        <td>{apiKey.lastUsedAt ? relativeTime(apiKey.lastUsedAt) : <span className="muted">{t('settings.usage.never')}</span>}</td>
        <td>
          <Badge tone={apiKey.revoked ? 'neutral' : 'success'}>
            {apiKey.revoked ? t('settings.status.revoked') : t('settings.status.active')}
          </Badge>
        </td>
        <td>
          {!apiKey.revoked && (
            <Button
              variant="danger"
              size="sm"
              disabled={revoke.isPending}
              onClick={() => setShowRevokeModal(true)}
            >
              {t('settings.action.revoke')}
            </Button>
          )}
        </td>
      </tr>
      {showRevokeModal && (
        <Modal
          title={t('settings.confirm.revokeTitle')}
          onClose={() => setShowRevokeModal(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setShowRevokeModal(false)}>
                {t('action.cancel')}
              </Button>
              <Button
                variant="danger"
                onClick={() => {
                  revoke.mutate(apiKey.id);
                  setShowRevokeModal(false);
                }}
              >
                {t('settings.action.revoke')}
              </Button>
            </>
          }
        >
          {t('settings.confirm.revokeBody', { name: apiKey.name })}
        </Modal>
      )}
    </>
  );
}

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
  const { data, isLoading, isError, error, refetch } = useEnterpriseStatus();

  if (isLoading) return <Card title={t('settings.license.title')}><LoadingBlock /></Card>;
  if (isError) return (
    <Card title={t('settings.license.title')}>
      <ErrorState message={errorMessage(error, t, 'error.apikeys')} onRetry={() => refetch()} />
    </Card>
  );
  if (!data) return null;

  const isCommunity = data.edition === 'community';
  const isExpired = data.expires ? new Date(data.expires) < new Date() : false;

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
          {isExpired && (
            <span data-testid="expired-badge">
              <Badge tone="danger">{t('settings.license.expired')}</Badge>
            </span>
          )}
        </div>

        {isCommunity ? (
          <p className="muted">
            {t('settings.license.community.desc')}{' '}
            <a href="/docs/enterprise/overview" className="link">
              {t('settings.license.community.link')}
            </a>
          </p>
        ) : (
          <dl className="license-details">
            <dt>{t('settings.license.licensee')}</dt>
            <dd>{data.licensee}</dd>

            <dt>{t('settings.license.features')}</dt>
            <dd className="feature-badges" data-testid="feature-badges">
              {data.features.length === 0 ? (
                <span className="muted">{t('settings.license.no.features')}</span>
              ) : (
                data.features.map((f) => (
                  <Badge key={f} tone="info">
                    {f}
                  </Badge>
                ))
              )}
            </dd>

            {data.expires && (
              <>
                <dt>{t('settings.license.expires')}</dt>
                <dd>
                  <span className={isExpired ? 'text--danger' : undefined}>
                    {new Date(data.expires).toLocaleDateString()}
                  </span>
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
  const { data, isLoading, isError, error, refetch } = useApiKeys();
  const [showCreate, setShowCreate] = useState(false);
  const [created, setCreated] = useState<ApiKeyWithSecret | null>(null);

  return (
    <div className="page">
      <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle')} />

      <QuickStatsBar />

      <Card
        title={t('settings.keys.title')}
        action={
          <Button variant="primary" size="sm" onClick={() => setShowCreate(true)}>
            {t('settings.keys.new')}
          </Button>
        }
      >
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState message={errorMessage(error, t, 'error.apikeys')} onRetry={() => refetch()} />
        )}
        {data && data.length === 0 && <EmptyState message={t('settings.keys.empty')} />}
        {data && data.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('settings.col.name')}</th>
                  <th scope="col">{t('settings.col.team')}</th>
                  <th scope="col">{t('settings.col.key')}</th>
                  <th scope="col">{t('settings.col.role')}</th>
                  <th scope="col">{t('settings.col.usage')}</th>
                  <th scope="col">{t('settings.col.tokens')}</th>
                  <th scope="col">{t('settings.col.lastUsed')}</th>
                  <th scope="col">{t('settings.col.status')}</th>
                  <th scope="col">
                    <span className="visually-hidden">{t('fleet.col.actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {data.map((k) => (
                  <KeyRow key={k.id} apiKey={k} t={t} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <UsageSummaryCard t={t} />

      <LicenseCard t={t} />

      {showCreate && (
        <CreateKeyModal
          t={t}
          onClose={() => setShowCreate(false)}
          onCreated={(k) => {
            setShowCreate(false);
            setCreated(k);
          }}
        />
      )}
      {created && <CreatedKeyModal keyData={created} onClose={() => setCreated(null)} t={t} />}
    </div>
  );
}
