// NodePoolsPage — manage node pools (v0.4 platform model).
//
// Platform admins can create pools, assign nodes, and configure per-team
// quotas for shared pools.
import { useState } from 'react';
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
import { IconServer, IconTrash } from '../components/icons';
import {
  useNodePools,
  useCreateNodePool,
  usePoolNodes,
  usePoolQuotas,
  useAssignNodeToPool,
  useRemoveNodeFromPool,
  useUpdateNodePool,
  useDeleteNodePool,
} from '../hooks/queries';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import type { NodePool, PoolTeamQuota } from '../api/types';

// ---------------------------------------------------------------------------
// Policy badge
// ---------------------------------------------------------------------------

function PolicyBadge({ policy }: { policy: NodePool['policy'] }) {
  const t = useT();
  const tone: Tone = policy === 'exclusive' ? 'success' : 'info';
  return (
    <Badge tone={tone}>
      {policy === 'exclusive' ? t('platform.pools.exclusive') : t('platform.pools.shared')}
    </Badge>
  );
}

// ---------------------------------------------------------------------------
// Create Pool modal
// ---------------------------------------------------------------------------

interface CreatePoolModalProps {
  onClose: () => void;
}

function CreatePoolModal({ onClose }: CreatePoolModalProps) {
  const t = useT();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [ownerType, setOwnerType] = useState<NodePool['owner_type']>('platform');
  const [ownerId, setOwnerId] = useState('platform');
  const [policy, setPolicy] = useState<NodePool['policy']>('shared');
  const nameId = useFieldId('pool-name');
  const descId = useFieldId('pool-desc');
  const ownerTypeId = useFieldId('pool-owner-type');
  const ownerIdId = useFieldId('pool-owner-id');
  const policyId = useFieldId('pool-policy');
  const createPool = useCreateNodePool();

  function handleSubmit() {
    if (!name.trim()) return;
    void createPool.mutateAsync({
      name: name.trim(),
      description: description.trim() || undefined,
      owner_type: ownerType,
      owner_id: ownerId.trim() || 'platform',
      policy,
    }).then(onClose);
  }

  return (
    <Modal
      title={t('platform.pools.createPool')}
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
            disabled={!name.trim() || createPool.isPending}
          >
            {t('platform.pools.createPool')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        <Field label={t('platform.pools.name')} htmlFor={nameId}>
          <input
            id={nameId}
            className="input"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="gpu-pool-a"
            autoFocus
          />
        </Field>
        <Field label={t('platform.pools.description')} htmlFor={descId}>
          <input
            id={descId}
            className="input"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Optional description"
          />
        </Field>
        <Field label={t('platform.pools.ownerType')} htmlFor={ownerTypeId}>
          <select
            id={ownerTypeId}
            className="select"
            value={ownerType}
            onChange={(e) => setOwnerType(e.target.value as NodePool['owner_type'])}
          >
            <option value="platform">platform</option>
            <option value="org">org</option>
            <option value="team">team</option>
          </select>
        </Field>
        <Field label={t('platform.pools.ownerId')} htmlFor={ownerIdId}>
          <input
            id={ownerIdId}
            className="input"
            type="text"
            value={ownerId}
            onChange={(e) => setOwnerId(e.target.value)}
            placeholder="platform"
          />
        </Field>
        <Field label={t('platform.pools.policy')} htmlFor={policyId}>
          <select
            id={policyId}
            className="select"
            value={policy}
            onChange={(e) => setPolicy(e.target.value as NodePool['policy'])}
          >
            <option value="shared">{t('platform.pools.shared')}</option>
            <option value="exclusive">{t('platform.pools.exclusive')}</option>
          </select>
        </Field>
        {createPool.isError && (
          <p style={{ color: 'var(--color-danger)', fontSize: '0.85em' }}>
            {createPool.error instanceof Error ? createPool.error.message : 'Error creating pool'}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Edit Pool modal — reuses the same Field/Modal idiom as Create.
// Only name / description / policy are mutable (matches PUT /pools/{id}).
// ---------------------------------------------------------------------------

interface EditPoolModalProps {
  pool: NodePool;
  onClose: () => void;
}

function EditPoolModal({ pool, onClose }: EditPoolModalProps) {
  const t = useT();
  const [name, setName] = useState(pool.name);
  const [description, setDescription] = useState(pool.description ?? '');
  const [policy, setPolicy] = useState<NodePool['policy']>(pool.policy);
  const nameId = useFieldId('edit-pool-name');
  const descId = useFieldId('edit-pool-desc');
  const policyId = useFieldId('edit-pool-policy');
  const updatePool = useUpdateNodePool();

  function handleSubmit() {
    if (!name.trim()) return;
    void updatePool.mutateAsync({
      id: pool.id,
      input: {
        name: name.trim(),
        description: description.trim(),
        policy,
      },
    }).then(onClose);
  }

  return (
    <Modal
      title={t('platform.pools.editPool')}
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
            disabled={!name.trim() || updatePool.isPending}
          >
            {t('platform.pools.save')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        <Field label={t('platform.pools.name')} htmlFor={nameId}>
          <input
            id={nameId}
            className="input"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
          />
        </Field>
        <Field label={t('platform.pools.description')} htmlFor={descId}>
          <input
            id={descId}
            className="input"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Optional description"
          />
        </Field>
        <Field label={t('platform.pools.policy')} htmlFor={policyId}>
          <select
            id={policyId}
            className="select"
            value={policy}
            onChange={(e) => setPolicy(e.target.value as NodePool['policy'])}
          >
            <option value="shared">{t('platform.pools.shared')}</option>
            <option value="exclusive">{t('platform.pools.exclusive')}</option>
          </select>
        </Field>
        {updatePool.isError && (
          <p style={{ color: 'var(--color-danger)', fontSize: '0.85em' }}>
            {updatePool.error instanceof Error ? updatePool.error.message : 'Error updating pool'}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Pool detail panel (inline expansion)
// ---------------------------------------------------------------------------

interface PoolDetailProps {
  pool: NodePool;
}

function PoolDetail({ pool }: PoolDetailProps) {
  const t = useT();
  const [newNodeId, setNewNodeId] = useState('');
  const { data: nodesData, isLoading: nodesLoading } = usePoolNodes(pool.id);
  const { data: quotasData } = usePoolQuotas(pool.policy === 'shared' ? pool.id : undefined);
  const assignNode = useAssignNodeToPool();
  const removeNode = useRemoveNodeFromPool();

  const nodeIds = nodesData?.node_ids ?? [];
  const quotas = quotasData?.quotas ?? [];

  function handleAssign() {
    if (!newNodeId.trim()) return;
    void assignNode.mutateAsync({ poolId: pool.id, nodeId: newNodeId.trim() })
      .then(() => setNewNodeId(''));
  }

  return (
    <div style={{ padding: '1rem', background: 'var(--color-bg-subtle)', borderTop: '1px solid var(--color-border)' }}>
      {/* Nodes section */}
      <p style={{ fontWeight: 600, marginBottom: '0.5rem', fontSize: '0.9em' }}>
        {t('platform.pools.assignNode')}
      </p>
      {nodesLoading && <LoadingBlock />}
      {!nodesLoading && nodeIds.length === 0 && (
        <p style={{ color: 'var(--color-text-muted)', fontSize: '0.85em', marginBottom: '0.75rem' }}>
          {t('platform.pools.noNodes')}
        </p>
      )}
      {nodeIds.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', marginBottom: '0.75rem' }}>
          {nodeIds.map((nid) => (
            <span key={nid} style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
              <Badge tone="neutral">{nid}</Badge>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void removeNode.mutateAsync({ poolId: pool.id, nodeId: nid })}
                disabled={removeNode.isPending}
                aria-label={t('platform.pools.removeNode')}
                style={{ padding: '0 0.25rem' }}
              >
                <IconTrash />
              </Button>
            </span>
          ))}
        </div>
      )}
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
        <input
          className="input"
          type="text"
          value={newNodeId}
          onChange={(e) => setNewNodeId(e.target.value)}
          placeholder="node-id"
          style={{ maxWidth: 200 }}
        />
        <Button
          variant="secondary"
          size="sm"
          onClick={handleAssign}
          disabled={!newNodeId.trim() || assignNode.isPending}
        >
          {t('platform.pools.assignNode')}
        </Button>
      </div>

      {/* Quotas section (shared pools only) */}
      {pool.policy === 'shared' && (
        <>
          <p style={{ fontWeight: 600, marginBottom: '0.5rem', fontSize: '0.9em' }}>
            {t('platform.pools.quotas')}
          </p>
          {quotas.length === 0 ? (
            <p style={{ color: 'var(--color-text-muted)', fontSize: '0.85em' }}>
              {t('platform.pools.noQuotas')}
            </p>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">{t('platform.pools.quota.team')}</th>
                    <th scope="col">{t('platform.pools.quota.maxDeployments')}</th>
                    <th scope="col">{t('platform.pools.quota.maxGpuNodes')}</th>
                    <th scope="col">{t('platform.pools.quota.priority')}</th>
                  </tr>
                </thead>
                <tbody>
                  {quotas.map((q: PoolTeamQuota) => (
                    <tr key={q.team_id}>
                      <td><code className="inline-code">{q.team_id}</code></td>
                      <td>{q.max_deployments}</td>
                      <td>{q.max_gpu_nodes}</td>
                      <td>{q.priority}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Pool row
// ---------------------------------------------------------------------------

interface PoolRowProps {
  pool: NodePool;
}

function PoolRow({ pool }: PoolRowProps) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const [editing, setEditing] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const deletePool = useDeleteNodePool();
  const nodeCount = pool.node_ids?.length ?? '—';
  const hasNodes = (pool.node_ids?.length ?? 0) > 0;

  function handleDelete() {
    if (!confirmingDelete) {
      setConfirmingDelete(true);
      return;
    }
    void deletePool.mutateAsync(pool.id)
      .then(() => setConfirmingDelete(false))
      .catch(() => setConfirmingDelete(false));
  }

  return (
    <>
      <tr>
        <td style={{ fontWeight: 600 }}>{pool.name}</td>
        <td>
          <span style={{ fontSize: '0.85em', color: 'var(--color-text-muted)' }}>
            {pool.owner_type}/{pool.owner_id}
          </span>
        </td>
        <td><PolicyBadge policy={pool.policy} /></td>
        <td>{nodeCount}</td>
        <td>
          <div style={{ display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? t('action.close') : t('platform.pools.assignNode')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>
              {t('platform.pools.edit')}
            </Button>
            <Button
              variant={confirmingDelete ? 'danger' : 'ghost'}
              size="sm"
              onClick={handleDelete}
              disabled={deletePool.isPending}
              aria-label={t('platform.pools.delete')}
              title={hasNodes ? t('platform.pools.deleteHasNodes') : undefined}
            >
              <IconTrash />
              {confirmingDelete
                ? t('platform.pools.deleteConfirm', { name: pool.name })
                : t('platform.pools.delete')}
            </Button>
            {confirmingDelete && (
              <Button variant="secondary" size="sm" onClick={() => setConfirmingDelete(false)}>
                {t('action.cancel')}
              </Button>
            )}
          </div>
          {deletePool.isError && (
            <p style={{ color: 'var(--color-danger)', fontSize: '0.8em', marginTop: '0.4rem' }}>
              {deletePool.error instanceof Error ? deletePool.error.message : t('platform.pools.deleteHasNodes')}
            </p>
          )}
        </td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={5} style={{ padding: 0 }}>
            <PoolDetail pool={pool} />
          </td>
        </tr>
      )}
      {editing && <EditPoolModal pool={pool} onClose={() => setEditing(false)} />}
    </>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function NodePoolsPage() {
  const t = useT();
  const [showCreate, setShowCreate] = useState(false);

  const { data, isLoading, isError, error, refetch } = useNodePools();
  const pools = data?.pools ?? [];

  const pageActions = (
    <Button variant="primary" size="sm" onClick={() => setShowCreate(true)}>
      {t('platform.pools.createPool')}
    </Button>
  );

  return (
    <div className="page">
      <PageHeader
        title={t('platform.pools.title')}
        actions={pageActions}
      />

      <Card title={t('platform.pools.title')}>
        {isLoading && <LoadingBlock />}

        {isError && (
          <ErrorState
            message={errorMessage(error, t, 'error.pools')}
            onRetry={() => void refetch()}
          />
        )}

        {!isLoading && !isError && pools.length === 0 && (
          <EmptyState icon={<IconServer />} message={t('platform.pools.noPools')} />
        )}

        {pools.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('platform.pools.col.name')}</th>
                  <th scope="col">{t('platform.pools.col.owner')}</th>
                  <th scope="col">{t('platform.pools.col.policy')}</th>
                  <th scope="col">{t('platform.pools.col.nodes')}</th>
                  <th scope="col">{t('platform.pools.col.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {pools.map((pool) => (
                  <PoolRow key={pool.id} pool={pool} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {showCreate && <CreatePoolModal onClose={() => setShowCreate(false)} />}
    </div>
  );
}
