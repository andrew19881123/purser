// RolesPage — org-scoped RBAC custom-role management (v0.4 platform model).
//
// Reached two ways, mirroring how the other org-scoped pages (TeamsListPage,
// TeamPage) are wired:
//   1. Drill-down:  /platform/orgs/:orgId/roles  — full management for one org.
//   2. Governance nav: /platform/roles — no org in context yet, so we show an
//      organization picker that routes into the scoped page.
//
// A role is a named bundle of permission keys drawn from the live catalog
// (GET /platform/permissions). Built-in ("system") roles are read-only; custom
// roles can be created, edited, and deleted (delete is confirm-first).
//
// These endpoints are NOT enterprise-gated, so there is no locked-panel path —
// errors surface through the shared ErrorState.
import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
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
import { IconShield, IconTrash } from '../components/icons';
import {
  useRoles,
  usePermissionCatalog,
  useCreateRole,
  useUpdateRole,
  useDeleteRole,
  useOrganizations,
} from '../hooks/queries';
import { useT } from '../i18n';
import type { TFunc } from '../i18n';
import type { StringKey } from '../i18n/en';
import { errorMessage } from '../lib/errors';
import type { CustomRole, PermissionDescriptor, PermissionScope } from '../api/types';

// Fixed scope order for display; each maps to a localized section heading.
const SCOPE_ORDER: PermissionScope[] = ['platform', 'org', 'team', 'inference'];
const SCOPE_LABEL: Record<PermissionScope, StringKey> = {
  platform: 'roles.scope.platform',
  org: 'roles.scope.org',
  team: 'roles.scope.team',
  inference: 'roles.scope.inference',
};

function scopeLabel(t: TFunc, scope: string): string {
  return scope in SCOPE_LABEL ? t(SCOPE_LABEL[scope as PermissionScope]) : scope;
}

// ---------------------------------------------------------------------------
// Create / edit role modal — name, description, and a permission multi-select
// grouped by scope, sourced from the live permission catalog.
// ---------------------------------------------------------------------------

interface RoleFormModalProps {
  orgId: string;
  role: CustomRole | null; // null = create
  onClose: () => void;
}

function RoleFormModal({ orgId, role, onClose }: RoleFormModalProps) {
  const t = useT();
  const isEdit = role !== null;
  const nameId = useFieldId('role-name');
  const descId = useFieldId('role-desc');

  const create = useCreateRole(orgId);
  const update = useUpdateRole(orgId);
  const { data: catalog, isLoading: catLoading, isError: catError } = usePermissionCatalog();

  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [selected, setSelected] = useState<Set<string>>(new Set(role?.permissions ?? []));

  // Group the catalog by scope, preserving SCOPE_ORDER and catalog order within.
  const groups = useMemo(() => {
    const perms = catalog?.permissions ?? [];
    const byScope = new Map<string, PermissionDescriptor[]>();
    for (const p of perms) {
      const arr = byScope.get(p.scope) ?? [];
      arr.push(p);
      byScope.set(p.scope, arr);
    }
    const ordered: Array<{ scope: string; perms: PermissionDescriptor[] }> = [];
    for (const scope of SCOPE_ORDER) {
      if (byScope.has(scope)) ordered.push({ scope, perms: byScope.get(scope)! });
    }
    // Any scope the catalog adds that we don't know about — append after.
    for (const [scope, arr] of byScope) {
      if (!SCOPE_ORDER.includes(scope as PermissionScope)) ordered.push({ scope, perms: arr });
    }
    return ordered;
  }, [catalog]);

  function toggle(key: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  const pending = create.isPending || update.isPending;
  const mutErr = create.error ?? update.error;

  function handleSubmit() {
    if (!name.trim()) return;
    const permissions = [...selected];
    const trimmedDesc = description.trim() || undefined;
    if (isEdit && role) {
      update.mutate(
        { id: role.id, data: { name: name.trim(), description: trimmedDesc, permissions } },
        { onSuccess: onClose },
      );
    } else {
      create.mutate(
        { name: name.trim(), description: trimmedDesc, permissions },
        { onSuccess: onClose },
      );
    }
  }

  return (
    <Modal
      title={isEdit ? t('roles.modal.editTitle') : t('roles.modal.createTitle')}
      onClose={onClose}
      footer={
        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <Button variant="secondary" size="sm" onClick={onClose} disabled={pending}>
            {t('roles.modal.cancel')}
          </Button>
          <Button variant="primary" size="sm" onClick={handleSubmit} disabled={pending || !name.trim()}>
            {pending ? '…' : t('roles.modal.submit')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem', minWidth: '520px' }}>
        <Field label={t('roles.modal.name')} htmlFor={nameId}>
          <input
            id={nameId}
            className="input"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="ML Engineer"
            autoFocus
            aria-required="true"
          />
        </Field>

        <Field label={t('roles.modal.description')} htmlFor={descId}>
          <input
            id={descId}
            className="input"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t('roles.modal.descriptionPlaceholder')}
          />
        </Field>

        <div className="field">
          <span className="field__label">{t('roles.modal.permissions')}</span>
          <p className="field__hint" style={{ marginTop: 0 }}>
            {t('roles.modal.permissionsHint')} · {t('roles.modal.selectedCount', { count: selected.size })}
          </p>

          {catLoading && <LoadingBlock />}
          {catError && (
            <p style={{ margin: 0, color: 'var(--danger-fg)', fontSize: '0.85em' }}>
              {t('error.permissions')}
            </p>
          )}

          {!catLoading && !catError && (
            <div
              style={{
                display: 'flex',
                flexDirection: 'column',
                gap: '0.9rem',
                maxHeight: '340px',
                overflowY: 'auto',
                paddingRight: '0.25rem',
              }}
            >
              {groups.map(({ scope, perms }) => (
                <fieldset
                  key={scope}
                  style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '0.6rem 0.9rem 0.8rem', margin: 0 }}
                >
                  <legend style={{ padding: '0 0.35rem', fontSize: '0.78em', fontWeight: 600, letterSpacing: '0.02em', color: 'var(--text-muted, var(--color-text-muted))' }}>
                    {scopeLabel(t, scope)}
                  </legend>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '0.45rem' }}>
                    {perms.map((p) => (
                      <label
                        key={p.key}
                        style={{ display: 'flex', alignItems: 'flex-start', gap: '0.55rem', cursor: 'pointer', fontSize: '0.85em', lineHeight: 1.35 }}
                      >
                        <input
                          type="checkbox"
                          checked={selected.has(p.key)}
                          onChange={() => toggle(p.key)}
                          style={{ marginTop: '0.15rem' }}
                        />
                        <span>
                          <code
                            className="inline-code"
                            style={{ fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)', fontSize: '0.92em' }}
                          >
                            {p.key}
                          </code>
                          {p.description && (
                            <span className="muted" style={{ display: 'block', color: 'var(--color-text-muted)', fontSize: '0.92em' }}>
                              {p.description}
                            </span>
                          )}
                        </span>
                      </label>
                    ))}
                  </div>
                </fieldset>
              ))}
            </div>
          )}
        </div>

        {mutErr && (
          <p style={{ margin: 0, color: 'var(--danger-fg)', fontSize: '0.875em' }}>
            {mutErr instanceof Error ? mutErr.message : t('error.roles.save')}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Table row
// ---------------------------------------------------------------------------

function RoleRow({
  orgId,
  role,
  onEdit,
}: {
  orgId: string;
  role: CustomRole;
  onEdit: (role: CustomRole) => void;
}) {
  const t = useT();
  const del = useDeleteRole(orgId);
  const [confirming, setConfirming] = useState(false);

  const preview = role.permissions.slice(0, 3);
  const extra = role.permissions.length - preview.length;

  return (
    <tr>
      <td style={{ fontWeight: 600 }}>{role.name}</td>
      <td>
        <Badge tone={role.isSystem ? 'info' : 'neutral'}>
          {role.isSystem ? t('roles.type.system') : t('roles.type.custom')}
        </Badge>
      </td>
      <td>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.3rem', alignItems: 'center' }}>
          {role.permissions.length === 0 ? (
            <span className="muted" style={{ opacity: 0.5 }}>—</span>
          ) : (
            <>
              {preview.map((p) => (
                <code
                  key={p}
                  className="inline-code"
                  style={{ fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)', fontSize: '0.78em' }}
                >
                  {p}
                </code>
              ))}
              {extra > 0 && (
                <span className="muted" style={{ color: 'var(--color-text-muted)', fontSize: '0.8em' }}>
                  {t('roles.permissionMore', { count: extra })}
                </span>
              )}
            </>
          )}
        </div>
      </td>
      <td className="muted" style={{ color: 'var(--color-text-muted)', fontSize: '0.85em', maxWidth: '18rem' }}>
        {role.description || <span style={{ opacity: 0.4 }}>—</span>}
      </td>
      <td>
        {role.isSystem ? (
          <span className="muted" style={{ color: 'var(--color-text-muted)', fontSize: '0.8em' }}>
            {t('roles.readonly')}
          </span>
        ) : confirming ? (
          <div style={{ display: 'flex', gap: '0.4rem' }}>
            <Button
              variant="danger"
              size="sm"
              onClick={() => {
                del.mutate(role.id);
                setConfirming(false);
              }}
              disabled={del.isPending}
            >
              {t('roles.action.deleteConfirm')}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => setConfirming(false)}>
              {t('roles.action.cancel')}
            </Button>
          </div>
        ) : (
          <div style={{ display: 'flex', gap: '0.35rem' }}>
            <Button variant="ghost" size="sm" onClick={() => onEdit(role)}>
              {t('roles.action.edit')}
            </Button>
            <Button variant="danger" size="sm" onClick={() => setConfirming(true)} aria-label={t('roles.action.delete')}>
              <IconTrash />
              {t('roles.action.delete')}
            </Button>
          </div>
        )}
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Manager (org id in context)
// ---------------------------------------------------------------------------

function RolesManager({ orgId }: { orgId: string }) {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useRoles(orgId);
  const [showCreate, setShowCreate] = useState(false);
  const [editRole, setEditRole] = useState<CustomRole | null>(null);

  const roles = data?.roles ?? [];

  const breadcrumb = (
    <span style={{ fontSize: '0.85em', color: 'var(--color-text-muted)' }}>
      <Link to="/platform/orgs" style={{ color: 'inherit' }}>
        {t('platform.orgs.title')}
      </Link>
      {' / '}
      {orgId}
      {' / '}
      {t('roles.title')}
    </span>
  );

  return (
    <div className="page">
      <PageHeader
        title={t('roles.title')}
        subtitle={t('roles.subtitle')}
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowCreate(true)}>
            {t('roles.create')}
          </Button>
        }
      />
      {breadcrumb}

      <div style={{ marginTop: '1.5rem' }}>
        <Card title={t('roles.title')}>
          {isLoading && <LoadingBlock />}

          {isError && (
            <ErrorState message={errorMessage(error, t, 'error.roles')} onRetry={() => void refetch()} />
          )}

          {!isLoading && !isError && roles.length === 0 && (
            <EmptyState icon={<IconShield />} title={t('roles.empty.title')} message={t('roles.empty.msg')} />
          )}

          {roles.length > 0 && (
            <div className="table-wrap" style={{ overflowX: 'auto' }}>
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">{t('roles.col.name')}</th>
                    <th scope="col">{t('roles.col.type')}</th>
                    <th scope="col">{t('roles.col.permissions')}</th>
                    <th scope="col">{t('roles.col.description')}</th>
                    <th scope="col">{t('roles.col.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {roles.map((role) => (
                    <RoleRow key={role.id} orgId={orgId} role={role} onEdit={setEditRole} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>

      {showCreate && <RoleFormModal orgId={orgId} role={null} onClose={() => setShowCreate(false)} />}
      {editRole && <RoleFormModal orgId={orgId} role={editRole} onClose={() => setEditRole(null)} />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Organization picker (no org id in context — reached from the Governance nav)
// ---------------------------------------------------------------------------

function RolesOrgPicker() {
  const t = useT();
  const navigate = useNavigate();
  const { data, isLoading, isError, error, refetch } = useOrganizations();
  const orgs = data?.organizations ?? [];
  const selectId = useFieldId('roles-org');

  return (
    <div className="page">
      <PageHeader title={t('roles.title')} subtitle={t('roles.subtitle')} />
      <Card title={t('roles.picker.title')}>
        {isLoading && <LoadingBlock />}

        {isError && (
          <ErrorState message={errorMessage(error, t, 'error.orgs')} onRetry={() => void refetch()} />
        )}

        {!isLoading && !isError && orgs.length === 0 && (
          <EmptyState icon={<IconShield />} message={t('roles.picker.noOrgs')} />
        )}

        {orgs.length > 0 && (
          <Field label={t('roles.picker.orgLabel')} htmlFor={selectId} hint={t('roles.picker.hint')}>
            <select
              id={selectId}
              className="select"
              defaultValue=""
              onChange={(e) => {
                if (e.target.value) navigate(`/platform/orgs/${e.target.value}/roles`);
              }}
            >
              <option value="" disabled>
                {t('roles.picker.placeholder')}
              </option>
              {orgs.map((org) => (
                <option key={org.id} value={org.id}>
                  {org.name}
                </option>
              ))}
            </select>
          </Field>
        )}
      </Card>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page shell — org-scoped when :orgId is present, picker otherwise.
// ---------------------------------------------------------------------------

export function RolesPage() {
  const { orgId } = useParams<{ orgId: string }>();
  return orgId ? <RolesManager orgId={orgId} /> : <RolesOrgPicker />;
}
