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
} from '../components/ui';
import { useApiKeys, useCreateApiKey, useRevokeApiKey } from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { relativeTime } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { ApiKey, ApiKeyWithSecret } from '../api/types';

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
  const nameId = useFieldId('kname');
  const teamId = useFieldId('kteam');
  const quotaId = useFieldId('kquota');

  const submit = () => {
    if (!name.trim() || !team.trim()) return;
    create.mutate(
      { name: name.trim(), team: team.trim(), monthlyQuota: quota ? Number(quota) : null },
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

function KeyRow({ apiKey, t }: { apiKey: ApiKey; t: TFunc }) {
  const revoke = useRevokeApiKey();
  return (
    <tr className={apiKey.revoked ? 'row--muted' : undefined}>
      <th scope="row">{apiKey.name}</th>
      <td>{apiKey.team}</td>
      <td>
        <code className="inline-code">{apiKey.prefix}…</code>
      </td>
      <td className="usage-cell">
        {apiKey.monthlyQuota === null ? (
          <span className="muted">{t('settings.usage.unlimited')}</span>
        ) : (
          <Meter used={apiKey.usedThisMonth} total={apiKey.monthlyQuota} label={apiKey.name} unit="req" />
        )}
      </td>
      <td>{apiKey.lastUsedAt ? relativeTime(apiKey.lastUsedAt) : <span className="muted">{t('settings.usage.never')}</span>}</td>
      <td>
        <Badge tone={apiKey.revoked ? 'neutral' : 'success'}>
          {apiKey.revoked ? t('settings.status.revoked') : t('settings.status.active')}
        </Badge>
      </td>
      <td>
        {!apiKey.revoked && (
          <Button variant="danger" size="sm" disabled={revoke.isPending} onClick={() => revoke.mutate(apiKey.id)}>
            {t('settings.action.revoke')}
          </Button>
        )}
      </td>
    </tr>
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
                  <th scope="col">{t('settings.col.usage')}</th>
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
