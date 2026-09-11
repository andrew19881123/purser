// ApiKeysPage — dedicated API key management page.
//
// This is the "control room for credentials": create, revoke, and audit
// every API key in the cluster. The quota bar is the one live element on
// this page — it uses semantic colours (green → warning → danger) so an
// over-quota key stands out at a glance without requiring the operator to
// read numbers.
//
// Moved from SettingsPage in v0.6 to give API key management its own URL
// (/api-keys) and nav entry, making it directly bookmarkable and linkable.
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
  useApiKeyTeamSlugs,
  useApiKeys,
  useCreateApiKey,
  useKeyUsage,
  useRevokeApiKey,
} from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { formatTokenCount, relativeTime } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { ApiKey, ApiKeyRole, ApiKeyWithSecret } from '../api/types';

// ---------------------------------------------------------------------------
// Role badge — coloured pill for admin / viewer / inference.
// ---------------------------------------------------------------------------

const ROLE_TONES: Record<ApiKeyRole, Tone> = {
  admin: 'warning',
  viewer: 'info',
  inference: 'success',
};

function RoleBadge({ role, t }: { role: ApiKeyRole; t: TFunc }) {
  const key = `settings.role.${role}` as const;
  return <Badge tone={ROLE_TONES[role] ?? 'neutral'}>{t(key)}</Badge>;
}

// ---------------------------------------------------------------------------
// CSV export (client-side, no server round-trip)
// ---------------------------------------------------------------------------

function exportApiKeysCsv(keys: ApiKey[]) {
  const headers = ['id', 'name', 'team', 'prefix', 'role', 'last_used_at', 'used_this_month', 'monthly_quota', 'status'];
  const rows = keys.map((k) =>
    [
      k.id,
      k.name,
      k.team,
      k.prefix,
      k.role,
      k.lastUsedAt ?? '',
      k.usedThisMonth,
      k.monthlyQuota ?? '',
      k.revoked ? 'revoked' : 'active',
    ].join(','),
  );
  const csv = [headers.join(','), ...rows].join('\n');
  const blob = new Blob([csv], { type: 'text/csv' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `api-keys-${new Date().toISOString().slice(0, 10)}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

// ---------------------------------------------------------------------------
// Create key modal
// ---------------------------------------------------------------------------

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
  const teamSlugs = useApiKeyTeamSlugs();
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
    // Convert quota: empty or "0" → null (unlimited); otherwise use the number.
    const parsedQuota = quota.trim() && Number(quota) > 0 ? Number(quota) : null;
    create.mutate(
      { name: name.trim(), team: team.trim(), monthlyQuota: parsedQuota, role },
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
        {teamSlugs.length > 0 ? (
          <select
            id={teamId}
            className="input"
            value={team}
            onChange={(e) => setTeam(e.target.value)}
          >
            <option value="">Select team…</option>
            {teamSlugs.map((slug) => (
              <option key={slug} value={slug}>{slug}</option>
            ))}
          </select>
        ) : (
          <input
            id={teamId}
            className="input"
            value={team}
            placeholder="e.g. platform"
            onChange={(e) => setTeam(e.target.value)}
          />
        )}
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
          placeholder={t('settings.create.quotaPlaceholder')}
          value={quota}
          onChange={(e) => setQuota(e.target.value)}
        />
      </Field>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Created key modal — shows the secret once
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Per-key token usage cell — separate component to avoid hook-in-loop
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Key row
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ApiKeysPage() {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useApiKeys();
  const [showCreate, setShowCreate] = useState(false);
  const [created, setCreated] = useState<ApiKeyWithSecret | null>(null);

  const keys = data ?? [];
  const canExport = keys.length > 0;

  return (
    <div className="page">
      <PageHeader
        title={t('apikeys.title')}
        subtitle={t('apikeys.subtitle')}
        actions={
          <div style={{ display: 'flex', gap: '8px' }}>
            <Button
              variant="secondary"
              size="sm"
              disabled={!canExport}
              onClick={() => exportApiKeysCsv(keys)}
              aria-label={t('apikeys.csv')}
            >
              {t('apikeys.csv')}
            </Button>
            <Button variant="primary" size="sm" onClick={() => setShowCreate(true)}>
              {t('settings.keys.new')}
            </Button>
          </div>
        }
      />

      <Card title={t('settings.keys.title')}>
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
