// OrganizationsPage — CRUD for platform organizations (v0.4 platform model).
//
// Platform admins can create, view, and delete organizations. Each org row
// links to its team listing at /platform/orgs/{id}/teams.
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
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
import { IconBuildingOffice, IconTrash } from '../components/icons';
import {
  useOrganizations,
  useCreateOrganization,
  useDeleteOrganization,
} from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import type { Organization } from '../api/types';

// ---------------------------------------------------------------------------
// Create Organization modal
// ---------------------------------------------------------------------------

interface CreateOrgModalProps {
  onClose: () => void;
}

function CreateOrgModal({ onClose }: CreateOrgModalProps) {
  const t = useT();
  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  const [description, setDescription] = useState('');
  const nameId = useFieldId('org-name');
  const slugId = useFieldId('org-slug');
  const descId = useFieldId('org-desc');
  const createOrg = useCreateOrganization();

  // Auto-derive slug from name
  function handleNameChange(v: string) {
    setName(v);
    setSlug(v.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''));
  }

  function handleSubmit() {
    if (!name.trim() || !slug.trim()) return;
    void createOrg.mutateAsync({ name: name.trim(), slug: slug.trim(), description: description.trim() || undefined })
      .then(onClose);
  }

  return (
    <Modal
      title={t('platform.orgs.createOrg')}
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
            disabled={!name.trim() || !slug.trim() || createOrg.isPending}
          >
            {t('platform.orgs.createOrg')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        <Field label={t('platform.orgs.name')} htmlFor={nameId}>
          <input
            id={nameId}
            className="input"
            type="text"
            value={name}
            onChange={(e) => handleNameChange(e.target.value)}
            placeholder="Acme Corp"
            autoFocus
          />
        </Field>
        <Field label={t('platform.orgs.slug')} htmlFor={slugId}>
          <input
            id={slugId}
            className="input"
            type="text"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="acme-corp"
          />
        </Field>
        <Field label={t('platform.orgs.description')} htmlFor={descId}>
          <input
            id={descId}
            className="input"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Optional description"
          />
        </Field>
        {createOrg.isError && (
          <p style={{ color: 'var(--color-danger)', fontSize: '0.85em' }}>
            {createOrg.error instanceof Error ? createOrg.error.message : 'Error creating organization'}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Table row
// ---------------------------------------------------------------------------

interface OrgRowProps {
  org: Organization;
}

function OrgRow({ org }: OrgRowProps) {
  const t = useT();
  const navigate = useNavigate();
  const deleteOrg = useDeleteOrganization();
  const [confirming, setConfirming] = useState(false);

  function handleDelete() {
    if (!confirming) {
      setConfirming(true);
      return;
    }
    void deleteOrg.mutateAsync(org.id).then(() => setConfirming(false));
  }

  return (
    <tr>
      <td>
        <button
          className="btn btn--ghost btn--sm"
          style={{ fontWeight: 600, padding: 0, textDecoration: 'underline' }}
          onClick={() => navigate(`/platform/orgs/${org.id}/teams`)}
        >
          {org.name}
        </button>
      </td>
      <td>
        <Badge tone="neutral">{org.slug}</Badge>
      </td>
      <td style={{ color: 'var(--color-text-muted)', fontSize: '0.85em' }}>
        {org.description ?? '—'}
      </td>
      <td>
        <div style={{ display: 'flex', gap: '0.4rem' }}>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => navigate(`/platform/orgs/${org.id}/teams`)}
          >
            {t('platform.orgs.viewTeams')}
          </Button>
          <Button
            variant={confirming ? 'danger' : 'ghost'}
            size="sm"
            onClick={handleDelete}
            disabled={deleteOrg.isPending}
            aria-label={t('platform.orgs.delete')}
          >
            <IconTrash />
            {confirming ? t('platform.orgs.deleteConfirm', { name: org.name }) : t('platform.orgs.delete')}
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

export function OrganizationsPage() {
  const t = useT();
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, isError, error, refetch } = useOrganizations();
  const orgs = data?.organizations ?? [];

  const pageActions = (
    <Button variant="primary" size="sm" onClick={() => setShowCreate(true)}>
      {t('platform.orgs.createOrg')}
    </Button>
  );

  return (
    <div className="page">
      <PageHeader
        title={t('platform.orgs.title')}
        actions={pageActions}
      />

      <Card title={t('platform.orgs.title')}>
        {isLoading && <LoadingBlock />}

        {isError && (
          <ErrorState
            message={errorMessage(error, t, 'error.orgs')}
            onRetry={() => void refetch()}
          />
        )}

        {!isLoading && !isError && orgs.length === 0 && (
          <EmptyState
            icon={<IconBuildingOffice />}
            message={t('platform.orgs.noOrgs')}
          />
        )}

        {orgs.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('platform.orgs.col.name')}</th>
                  <th scope="col">{t('platform.orgs.col.slug')}</th>
                  <th scope="col">{t('platform.orgs.description')}</th>
                  <th scope="col">{t('platform.orgs.col.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {orgs.map((org) => (
                  <OrgRow key={org.id} org={org} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {showCreate && <CreateOrgModal onClose={() => setShowCreate(false)} />}
    </div>
  );
}
