// TeamPage — detail view for a single team (v0.4 platform model).
//
// Shows:
//   1. Members table with invite/remove actions.
//   2. Node pool assignment card (which pool this team can use).
//   3. My Permissions card (what the current user can do in this team).
import { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  LoadingBlock,
  Modal,
  PageHeader,
  useFieldId,
  type Tone,
} from '../components/ui';
import { IconUsers, IconServer, IconTrash } from '../components/icons';
import {
  useTeam,
  useTeamMembers,
  useAddTeamMember,
  useRemoveTeamMember,
  useNodePools,
  useMyTeamPermissions,
  useRoles,
} from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import type { TeamMember } from '../api/types';

// ---------------------------------------------------------------------------
// Invite Member modal
// ---------------------------------------------------------------------------

interface InviteMemberModalProps {
  orgId: string | undefined;
  teamId: string;
  onClose: () => void;
}

function InviteMemberModal({ orgId, teamId, onClose }: InviteMemberModalProps) {
  const t = useT();
  const { data: rolesData } = useRoles(orgId);
  const roles = rolesData?.roles ?? [];
  const [userId, setUserId] = useState('');
  const [roleId, setRoleId] = useState('');
  const userIdField = useFieldId('invite-user');
  const roleIdField = useFieldId('invite-role');
  const addMember = useAddTeamMember(teamId);

  // Assigning a role is now a pick from the org's roles (built-in + custom),
  // not a typed string. Default to the first role until the operator chooses.
  const selectedRole = roleId || roles[0]?.id || '';

  function handleSubmit() {
    if (!userId.trim()) return;
    void addMember.mutateAsync({ user_id: userId.trim(), role_id: selectedRole || 'developer' })
      .then(onClose);
  }

  return (
    <Modal
      title={t('platform.teams.inviteMember')}
      onClose={onClose}
      footer={
        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <Button variant="secondary" size="sm" onClick={onClose}>
            {t('action.cancel')}
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={handleSubmit}
            disabled={!userId.trim() || addMember.isPending}
          >
            {t('platform.teams.inviteMember')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        <Field label={t('platform.teams.addMember.userId')} htmlFor={userIdField}>
          <input
            id={userIdField}
            className="input"
            type="text"
            value={userId}
            onChange={(e) => setUserId(e.target.value)}
            placeholder="user@example.com"
            autoFocus
          />
        </Field>
        <Field label={t('roles.assign.label')} htmlFor={roleIdField} hint={t('roles.assign.hint')}>
          <select
            id={roleIdField}
            className="select"
            value={selectedRole}
            onChange={(e) => setRoleId(e.target.value)}
          >
            {roles.length === 0 && (
              <option value="" disabled>
                {t('roles.assign.loading')}
              </option>
            )}
            {roles.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
        </Field>
        {addMember.isError && (
          <p style={{ color: 'var(--color-danger)', fontSize: '0.85em' }}>
            {addMember.error instanceof Error ? addMember.error.message : 'Error inviting member'}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Members card
// ---------------------------------------------------------------------------

interface MembersCardProps {
  orgId: string | undefined;
  teamId: string;
}

function MembersCard({ orgId, teamId }: MembersCardProps) {
  const t = useT();
  const [showInvite, setShowInvite] = useState(false);
  const { data, isLoading, isError, error, refetch } = useTeamMembers(teamId);
  const removeMember = useRemoveTeamMember(teamId);
  const members = data?.members ?? [];

  function MemberRow({ member }: { member: TeamMember }) {
    const [confirming, setConfirming] = useState(false);
    const email = member.user?.email ?? member.user_id;
    const roleName = member.role?.name ?? member.role_id;
    const joined = member.created_at
      ? new Date(member.created_at).toLocaleDateString()
      : '—';

    return (
      <tr>
        <td style={{ fontSize: '0.9em' }}>{email}</td>
        <td><Badge tone="info">{roleName}</Badge></td>
        <td style={{ color: 'var(--color-text-muted)', fontSize: '0.85em' }}>{joined}</td>
        <td>
          {confirming ? (
            <div style={{ display: 'flex', gap: '0.4rem' }}>
              <Button
                variant="danger"
                size="sm"
                onClick={() => void removeMember.mutateAsync(member.user_id).then(() => setConfirming(false))}
                disabled={removeMember.isPending}
              >
                {t('platform.teams.remove')}
              </Button>
              <Button variant="secondary" size="sm" onClick={() => setConfirming(false)}>
                {t('action.cancel')}
              </Button>
            </div>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setConfirming(true)}
              aria-label={t('platform.teams.remove')}
            >
              <IconTrash />
            </Button>
          )}
        </td>
      </tr>
    );
  }

  return (
    <>
      <Card
        title={t('platform.teams.members')}
        action={
          <Button variant="primary" size="sm" onClick={() => setShowInvite(true)}>
            {t('platform.teams.inviteMember')}
          </Button>
        }
      >
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState
            message={errorMessage(error, t, 'error.teams')}
            onRetry={() => void refetch()}
          />
        )}
        {!isLoading && !isError && members.length === 0 && (
          <EmptyState icon={<IconUsers />} message={t('platform.teams.noMembers')} />
        )}
        {members.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('platform.teams.col.user')}</th>
                  <th scope="col">{t('platform.teams.col.role')}</th>
                  <th scope="col">{t('platform.teams.col.joined')}</th>
                  <th scope="col">{t('platform.teams.col.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {members.map((m) => (
                  <MemberRow key={m.id} member={m} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      {showInvite && <InviteMemberModal orgId={orgId} teamId={teamId} onClose={() => setShowInvite(false)} />}
    </>
  );
}

// ---------------------------------------------------------------------------
// Node Pool card
// ---------------------------------------------------------------------------

interface NodePoolCardProps {
  teamId: string;
}

function NodePoolCard({ teamId }: NodePoolCardProps) {
  const t = useT();
  // In v0.4, a team uses the pool assigned to it by the platform admin.
  // We fetch all pools and find the one owned by this team.
  const { data, isLoading } = useNodePools();
  const pools = data?.pools ?? [];
  const teamPool = pools.find((p) => p.owner_type === 'team' && p.owner_id === teamId);

  return (
    <Card title={t('platform.teams.nodePool')}>
      {isLoading && <LoadingBlock />}
      {!isLoading && !teamPool && (
        <EmptyState icon={<IconServer />} message={t('platform.teams.noPool')} />
      )}
      {teamPool && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
          <div>
            <p style={{ fontWeight: 600, marginBottom: '0.25rem' }}>{teamPool.name}</p>
            <Badge tone={teamPool.policy === 'exclusive' ? 'success' : 'info'}>
              {teamPool.policy === 'exclusive'
                ? t('platform.pools.exclusive')
                : t('platform.pools.shared')}
            </Badge>
          </div>
          <Link
            to={`/platform/pools`}
            className="btn btn--secondary btn--sm"
            style={{ marginLeft: 'auto' }}
          >
            {t('platform.pools.title')}
          </Link>
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// My Permissions card
// ---------------------------------------------------------------------------

interface MyPermissionsCardProps {
  teamId: string;
}

function MyPermissionsCard({ teamId }: MyPermissionsCardProps) {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useMyTeamPermissions(teamId);

  const permTone = (perm: string): Tone => {
    if (perm.startsWith('admin') || perm === 'delete') return 'warning';
    if (perm.startsWith('deploy') || perm.startsWith('write')) return 'info';
    return 'neutral';
  };

  return (
    <Card title={t('platform.teams.myPermissions')}>
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState
          message={errorMessage(error, t, 'error.teams')}
          onRetry={() => void refetch()}
        />
      )}
      {data && (
        <div>
          {data.is_org_admin && (
            <div style={{ marginBottom: '0.75rem' }}>
              <Badge tone="warning">Org Admin</Badge>
            </div>
          )}
          {data.permissions.length === 0 ? (
            <p style={{ color: 'var(--color-text-muted)', fontSize: '0.9em' }}>
              {t('platform.teams.noPermissions')}
            </p>
          ) : (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.4rem' }}>
              {data.permissions.map((perm) => (
                <Badge key={perm} tone={permTone(perm)}>{perm}</Badge>
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function TeamPage() {
  const t = useT();
  const { orgId, teamId } = useParams<{ orgId: string; teamId: string }>();

  const { data: team, isLoading, isError, error, refetch } = useTeam(teamId);

  if (isLoading) {
    return (
      <div className="page">
        <LoadingBlock />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="page">
        <ErrorState
          message={errorMessage(error, t, 'error.teams')}
          onRetry={() => void refetch()}
        />
      </div>
    );
  }

  const breadcrumb = (
    <span style={{ fontSize: '0.85em', color: 'var(--color-text-muted)' }}>
      <Link to="/platform/orgs" style={{ color: 'inherit' }}>
        {t('platform.orgs.title')}
      </Link>
      {' / '}
      <Link to={`/platform/orgs/${orgId}/teams`} style={{ color: 'inherit' }}>
        {orgId}
      </Link>
      {' / '}
      {team?.name ?? teamId}
    </span>
  );

  return (
    <div className="page">
      <PageHeader
        title={team?.name ?? t('platform.teams.title')}
        subtitle={team?.slug}
      />
      {breadcrumb}
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1.5rem', marginTop: '1.5rem' }}>
        {teamId && <MembersCard orgId={orgId} teamId={teamId} />}
        {teamId && <NodePoolCard teamId={teamId} />}
        {teamId && <MyPermissionsCard teamId={teamId} />}
      </div>
    </div>
  );
}
