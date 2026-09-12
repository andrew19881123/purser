// TeamsListPage — teams within a single organization (v0.4 platform model).
//
// Reached from OrganizationsPage's "View Teams" action at
// /platform/orgs/:orgId/teams. Platform admins can create, view, and delete
// teams here; each team row links to its detail view (TeamPage) at
// /platform/orgs/:orgId/teams/:teamId.
//
// This page only wires the EXISTING useTeams / useCreateTeam / useDeleteTeam
// hooks into a reachable list — the per-team detail (members, pools,
// permissions) already lives in TeamPage.
import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
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
} from '../components/ui';
import { IconUsers, IconTrash } from '../components/icons';
import { useTeams, useCreateTeam, useDeleteTeam } from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import type { Team } from '../api/types';

// ---------------------------------------------------------------------------
// Create Team modal
// ---------------------------------------------------------------------------

interface CreateTeamModalProps {
  orgId: string;
  onClose: () => void;
}

function CreateTeamModal({ orgId, onClose }: CreateTeamModalProps) {
  const t = useT();
  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  const [description, setDescription] = useState('');
  const nameId = useFieldId('team-name');
  const slugId = useFieldId('team-slug');
  const descId = useFieldId('team-desc');
  const createTeam = useCreateTeam(orgId);

  // Auto-derive slug from name (same idiom as CreateOrgModal).
  function handleNameChange(v: string) {
    setName(v);
    setSlug(v.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''));
  }

  function handleSubmit() {
    if (!name.trim() || !slug.trim()) return;
    void createTeam
      .mutateAsync({ name: name.trim(), slug: slug.trim(), description: description.trim() || undefined })
      .then(onClose);
  }

  return (
    <Modal
      title={t('platform.teams.createTeam')}
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
            disabled={!name.trim() || !slug.trim() || createTeam.isPending}
          >
            {t('platform.teams.createTeam')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        <Field label={t('platform.teams.name')} htmlFor={nameId}>
          <input
            id={nameId}
            className="input"
            type="text"
            value={name}
            onChange={(e) => handleNameChange(e.target.value)}
            placeholder="Platform Engineering"
            autoFocus
          />
        </Field>
        <Field label={t('platform.teams.slug')} htmlFor={slugId}>
          <input
            id={slugId}
            className="input"
            type="text"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="platform-engineering"
          />
        </Field>
        <Field label={t('platform.teams.description')} htmlFor={descId}>
          <input
            id={descId}
            className="input"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Optional description"
          />
        </Field>
        {createTeam.isError && (
          <p style={{ color: 'var(--color-danger)', fontSize: '0.85em' }}>
            {createTeam.error instanceof Error ? createTeam.error.message : 'Error creating team'}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Table row
// ---------------------------------------------------------------------------

interface TeamRowProps {
  orgId: string;
  team: Team;
}

function TeamRow({ orgId, team }: TeamRowProps) {
  const t = useT();
  const deleteTeam = useDeleteTeam(orgId);
  const [confirming, setConfirming] = useState(false);

  function handleDelete() {
    if (!confirming) {
      setConfirming(true);
      return;
    }
    void deleteTeam.mutateAsync(team.id).then(() => setConfirming(false));
  }

  return (
    <tr>
      <td>
        <Link
          to={`/platform/orgs/${orgId}/teams/${team.id}`}
          className="btn btn--ghost btn--sm"
          style={{ fontWeight: 600, padding: 0, textDecoration: 'underline' }}
        >
          {team.name}
        </Link>
      </td>
      <td>
        <Badge tone="neutral">{team.slug}</Badge>
      </td>
      <td style={{ color: 'var(--color-text-muted)', fontSize: '0.85em' }}>
        {team.description ?? '—'}
      </td>
      <td>
        <div style={{ display: 'flex', gap: '0.4rem' }}>
          <Button
            variant={confirming ? 'danger' : 'ghost'}
            size="sm"
            onClick={handleDelete}
            disabled={deleteTeam.isPending}
            aria-label={t('platform.teams.delete')}
          >
            <IconTrash />
            {confirming ? t('platform.teams.deleteConfirm', { name: team.name }) : t('platform.teams.delete')}
          </Button>
          {confirming && (
            <Button variant="secondary" size="sm" onClick={() => setConfirming(false)}>
              {t('action.cancel')}
            </Button>
          )}
        </div>
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function TeamsListPage() {
  const t = useT();
  const { orgId } = useParams<{ orgId: string }>();
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, isError, error, refetch } = useTeams(orgId);
  const teams = data?.teams ?? [];

  const breadcrumb = (
    <span style={{ fontSize: '0.85em', color: 'var(--color-text-muted)' }}>
      <Link to="/platform/orgs" style={{ color: 'inherit' }}>
        {t('platform.orgs.title')}
      </Link>
      {' / '}
      {orgId}
    </span>
  );

  const pageActions = orgId ? (
    <Button variant="primary" size="sm" onClick={() => setShowCreate(true)}>
      {t('platform.teams.createTeam')}
    </Button>
  ) : undefined;

  return (
    <div className="page">
      <PageHeader
        title={t('platform.teams.listTitle')}
        subtitle={t('platform.teams.listSubtitle')}
        actions={pageActions}
      />
      {breadcrumb}

      <Card title={t('platform.teams.listTitle')}>
        {isLoading && <LoadingBlock />}

        {isError && (
          <ErrorState
            message={errorMessage(error, t, 'error.teams')}
            onRetry={() => void refetch()}
          />
        )}

        {!isLoading && !isError && teams.length === 0 && (
          <EmptyState icon={<IconUsers />} message={t('platform.teams.noTeams')} />
        )}

        {teams.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('platform.teams.col.name')}</th>
                  <th scope="col">{t('platform.teams.col.slug')}</th>
                  <th scope="col">{t('platform.teams.col.description')}</th>
                  <th scope="col">{t('platform.teams.col.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {teams.map((team) => (
                  <TeamRow key={team.id} orgId={orgId as string} team={team} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {showCreate && orgId && <CreateTeamModal orgId={orgId} onClose={() => setShowCreate(false)} />}
    </div>
  );
}
