// Route: /platform/service-accounts — add to router.tsx when ready
//
// ServiceAccountsPage — machine identity management for CI/CD pipelines,
// monitoring agents, and automation tooling.
//
// Design language: monospace-first, technical. Client IDs are rendered in
// <code> elements throughout; the table reads like a credentials manifest.
// The "shown once" pattern for client_secret mirrors the ApiKeysPage approach.
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
  Modal,
  PageHeader,
  useFieldId,
  type Tone,
} from '../components/ui';
import {
  useServiceAccounts,
  useCreateServiceAccount,
  useRevokeServiceAccount,
} from '../hooks/queries';
import { relativeTime } from '../lib/format';
import type { ServiceAccount, ServiceAccountWithSecret } from '../api/types';

// ---------------------------------------------------------------------------
// Role badge
// ---------------------------------------------------------------------------

const ROLE_TONE: Record<string, Tone> = {
  admin: 'warning',
  inference: 'success',
  viewer: 'info',
};

function RoleBadge({ role }: { role: string }) {
  return (
    <span data-testid="role-badge">
      <Badge tone={ROLE_TONE[role] ?? 'neutral'}>{role}</Badge>
    </span>
  );
}

// ---------------------------------------------------------------------------
// Create modal
// ---------------------------------------------------------------------------

function CreateServiceAccountModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (sa: ServiceAccountWithSecret) => void;
}) {
  const create = useCreateServiceAccount();
  const [name, setName] = useState('');
  const [teamId, setTeamId] = useState('');
  const [description, setDescription] = useState('');
  const [role, setRole] = useState('inference');
  const nameId = useFieldId('sa-name');
  const teamId_ = useFieldId('sa-team');
  const descId = useFieldId('sa-desc');
  const roleId = useFieldId('sa-role');

  const submit = () => {
    if (!name.trim() || !teamId.trim()) return;
    create.mutate(
      { name: name.trim(), teamId: teamId.trim(), description: description.trim(), role },
      { onSuccess: (sa) => onCreated(sa) },
    );
  };

  return (
    <Modal
      title="New Service Account"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            onClick={submit}
            disabled={create.isPending || !name.trim() || !teamId.trim()}
            data-testid="create-sa-submit"
          >
            {create.isPending ? 'Creating…' : 'Create'}
          </Button>
        </>
      }
    >
      <Field label="Name" htmlFor={nameId}>
        <input
          id={nameId}
          className="input"
          value={name}
          placeholder="e.g. ci-pipeline-prod"
          onChange={(e) => setName(e.target.value)}
          data-testid="sa-name-input"
        />
      </Field>
      <Field label="Team" htmlFor={teamId_} hint="Team this service account belongs to">
        <input
          id={teamId_}
          className="input"
          value={teamId}
          placeholder="e.g. platform"
          onChange={(e) => setTeamId(e.target.value)}
          data-testid="sa-team-input"
        />
      </Field>
      <Field label="Role" htmlFor={roleId} hint="Determines which API endpoints this identity can access">
        <select
          id={roleId}
          className="input"
          value={role}
          onChange={(e) => setRole(e.target.value)}
          data-testid="sa-role-select"
        >
          <option value="inference">inference — Gateway /v1/ only</option>
          <option value="viewer">viewer — read-only management plane</option>
          <option value="admin">admin — full management access</option>
        </select>
      </Field>
      <Field label="Description" htmlFor={descId}>
        <input
          id={descId}
          className="input"
          value={description}
          placeholder="Optional — e.g. GitHub Actions prod deploy"
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Secret shown-once modal
// ---------------------------------------------------------------------------

function CreatedSaModal({ sa, onClose }: { sa: ServiceAccountWithSecret; onClose: () => void }) {
  return (
    <Modal
      title="Service account created — save the secret"
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose} data-testid="sa-secret-close">
          I have saved the secret
        </Button>
      }
    >
      <div className="notice notice--warning" role="alert" data-testid="sa-secret-warning">
        The client secret will not be shown again. Copy it now and store it in your
        CI/CD secrets manager or vault.
      </div>

      <div>
        <p style={{ fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '6px' }}>
          Client ID
        </p>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <code
            style={{ fontFamily: 'var(--font-mono)', fontSize: '13px', flex: 1, background: 'var(--surface-2)', padding: '6px 10px', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)' }}
            data-testid="sa-client-id"
          >
            {sa.clientId}
          </code>
          <CopyButton value={sa.clientId} />
        </div>
      </div>

      <div>
        <p style={{ fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '6px' }}>
          Client secret
        </p>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <code
            style={{ fontFamily: 'var(--font-mono)', fontSize: '13px', flex: 1, wordBreak: 'break-all', background: 'var(--surface-2)', padding: '6px 10px', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)' }}
            data-testid="sa-client-secret"
          >
            {sa.clientSecret}
          </code>
          <CopyButton value={sa.clientSecret} />
        </div>
      </div>

      <p style={{ fontSize: '12px', color: 'var(--text-muted)' }}>
        Use these credentials with the OAuth2 <code style={{ fontFamily: 'var(--font-mono)' }}>client_credentials</code> grant
        at <code style={{ fontFamily: 'var(--font-mono)' }}>POST /auth/token</code>.
      </p>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Revoke confirmation modal
// ---------------------------------------------------------------------------

function RevokeConfirmModal({
  sa,
  onClose,
  onConfirm,
  isPending,
}: {
  sa: ServiceAccount;
  onClose: () => void;
  onConfirm: () => void;
  isPending: boolean;
}) {
  return (
    <Modal
      title="Revoke service account?"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="danger" onClick={onConfirm} disabled={isPending} data-testid="revoke-sa-confirm">
            Revoke
          </Button>
        </>
      }
    >
      <p>
        Revoke <strong>{sa.name}</strong>? Any running pipeline using this identity will lose
        access immediately. This cannot be undone.
      </p>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Service account row
// ---------------------------------------------------------------------------

function ServiceAccountRow({ sa }: { sa: ServiceAccount }) {
  const revoke = useRevokeServiceAccount();
  const [showRevoke, setShowRevoke] = useState(false);

  return (
    <>
      <tr className={!sa.enabled ? 'row--muted' : undefined}>
        <th scope="row">
          <span style={{ fontWeight: 600 }}>{sa.name}</span>
          {sa.description && (
            <span style={{ display: 'block', fontSize: '12px', color: 'var(--text-muted)', fontWeight: 400 }}>
              {sa.description}
            </span>
          )}
        </th>
        <td>
          <code
            style={{ fontFamily: 'var(--font-mono)', fontSize: '12px', background: 'var(--surface-2)', padding: '2px 7px', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)' }}
            data-testid="sa-client-id-cell"
          >
            {sa.clientId}
          </code>
        </td>
        <td>
          <span
            style={{ fontFamily: 'var(--font-mono)', fontSize: '12px', background: 'var(--surface-2)', padding: '2px 7px', borderRadius: 'var(--radius-sm)' }}
          >
            {sa.tenant}
          </span>
        </td>
        <td><RoleBadge role={sa.role} /></td>
        <td>
          <span data-testid="sa-status-badge">
            <Badge tone={sa.enabled ? 'success' : 'neutral'}>
              {sa.enabled ? 'enabled' : 'disabled'}
            </Badge>
          </span>
        </td>
        <td>
          {sa.lastUsedAt ? (
            <span style={{ fontSize: '13px' }}>{relativeTime(sa.lastUsedAt)}</span>
          ) : (
            <span className="muted">never</span>
          )}
        </td>
        <td>
          {sa.enabled && (
            <Button
              variant="danger"
              size="sm"
              disabled={revoke.isPending}
              onClick={() => setShowRevoke(true)}
              data-testid="revoke-sa-btn"
            >
              Revoke
            </Button>
          )}
        </td>
      </tr>
      {showRevoke && (
        <RevokeConfirmModal
          sa={sa}
          onClose={() => setShowRevoke(false)}
          onConfirm={() => {
            revoke.mutate(sa.id);
            setShowRevoke(false);
          }}
          isPending={revoke.isPending}
        />
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ServiceAccountsPage() {
  const { data, isLoading, isError, error, refetch } = useServiceAccounts();
  const [showCreate, setShowCreate] = useState(false);
  const [created, setCreated] = useState<ServiceAccountWithSecret | null>(null);

  return (
    <div className="page">
      <PageHeader
        title="Service Accounts"
        subtitle="Machine identities for CI/CD pipelines, monitoring agents, and automation."
        actions={
          <Button
            variant="primary"
            size="sm"
            onClick={() => setShowCreate(true)}
            data-testid="new-sa-btn"
          >
            New Service Account
          </Button>
        }
      />

      <Card title="Service Accounts">
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState
            message={error instanceof Error ? error.message : 'Failed to load service accounts'}
            onRetry={() => refetch()}
          />
        )}
        {data && data.length === 0 && (
          <EmptyState
            message="No service accounts yet. Create one for each pipeline, agent, or integration that needs programmatic access."
          />
        )}
        {data && data.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">Client ID</th>
                  <th scope="col">Team</th>
                  <th scope="col">Role</th>
                  <th scope="col">Status</th>
                  <th scope="col">Last used</th>
                  <th scope="col"><span className="visually-hidden">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {data.map((sa) => (
                  <ServiceAccountRow key={sa.id} sa={sa} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {showCreate && (
        <CreateServiceAccountModal
          onClose={() => setShowCreate(false)}
          onCreated={(sa) => {
            setShowCreate(false);
            setCreated(sa);
          }}
        />
      )}
      {created && (
        <CreatedSaModal sa={created} onClose={() => setCreated(null)} />
      )}
    </div>
  );
}
