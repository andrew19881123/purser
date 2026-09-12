// Route: /platform/dataplanes — add to router.tsx when ready
//
// DataPlanesPage — operator view of every registered Data Plane.
//
// A Data Plane is a named inference cluster (GPU nodes + Gateway) that
// connects back to the Control Plane. The design language is infrastructure:
// tier badges encode deployment weight (solid/outlined/ghost), active DPs
// get a teal left-border accent, and the empty state shows the CP→DP
// topology so a new operator immediately understands what to register.
import { useState, type CSSProperties } from 'react';
import {
  Button,
  Card,
  CopyButton,
  ErrorState,
  Field,
  LoadingBlock,
  Modal,
  PageHeader,
  useFieldId,
  type Tone,
} from '../components/ui';
import { IconTrash } from '../components/icons';
import {
  useDataPlanes,
  useCreateDataPlane,
  useRefreshDataPlaneConfig,
  useUpdateDataPlane,
  useDeleteDataPlane,
  useDataPlaneNodes,
  useAssignNodeToDataPlane,
  useUnassignNodeFromDataPlane,
} from '../hooks/queries';
import { useT } from '../i18n';
import { relativeTime } from '../lib/format';
import type { DataPlane, DataPlaneWithToken } from '../api/types';

// ---------------------------------------------------------------------------
// Tier badge — encodes deployment weight via shape, not just color.
// production: solid accent fill  •  staging: outlined  •  development: ghost
// ---------------------------------------------------------------------------

function TierBadge({ tier }: { tier: string }) {
  if (tier === 'production') {
    return (
      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          padding: '2px 9px',
          borderRadius: 'var(--radius-sm)',
          fontSize: '12px',
          fontWeight: 700,
          letterSpacing: '0.03em',
          background: '#0d9488',
          color: '#fff',
        }}
        data-testid="tier-badge"
      >
        production
      </span>
    );
  }
  if (tier === 'staging') {
    return (
      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          padding: '2px 9px',
          borderRadius: 'var(--radius-sm)',
          fontSize: '12px',
          fontWeight: 600,
          letterSpacing: '0.03em',
          border: '1.5px solid #0d9488',
          color: '#0d9488',
          background: 'transparent',
        }}
        data-testid="tier-badge"
      >
        staging
      </span>
    );
  }
  // development and any other tier: ghost / muted
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        padding: '2px 9px',
        borderRadius: 'var(--radius-sm)',
        fontSize: '12px',
        fontWeight: 500,
        color: 'var(--text-muted)',
        background: 'var(--surface-2)',
      }}
      data-testid="tier-badge"
    >
      {tier || 'development'}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Status pill — DP lifecycle.
// ---------------------------------------------------------------------------

const STATUS_TONE: Record<string, Tone> = {
  active: 'success',
  registering: 'info',
  degraded: 'warning',
  offline: 'neutral',
};

function DpStatusPill({ status }: { status: string }) {
  const tone = STATUS_TONE[status] ?? 'neutral';
  // Active DPs get a subtle animated dot to signal live connectivity.
  return (
    <span className={`pill pill--${tone}`} data-testid="dp-status-pill">
      <span className="pill__dot" aria-hidden="true" />
      {status}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Config snapshot summary — shown in the expanded row.
// ---------------------------------------------------------------------------

function ConfigSnapshotSummary({ snap }: { snap: Record<string, unknown> | null | undefined }) {
  if (!snap) {
    return (
      <p style={{ fontSize: '13px', color: 'var(--text-muted)', fontStyle: 'italic' }}>
        No config snapshot yet — config is pushed automatically every 30 s or via Refresh.
      </p>
    );
  }
  const rt = snap.routingTable as Record<string, unknown> | undefined;
  const ab = snap.authBundle as Record<string, unknown> | undefined;
  const routingCount = rt ? Object.keys(rt).length : 0;
  const authCount = ab ? Object.keys(ab).length : 0;
  return (
    <dl
      style={{
        display: 'grid',
        gridTemplateColumns: 'max-content 1fr',
        columnGap: '16px',
        rowGap: '4px',
        fontSize: '13px',
      }}
    >
      <dt style={{ color: 'var(--text-muted)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em', fontSize: '11px' }}>
        Routing entries
      </dt>
      <dd style={{ margin: 0 }}>{routingCount}</dd>
      <dt style={{ color: 'var(--text-muted)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em', fontSize: '11px' }}>
        Auth keys
      </dt>
      <dd style={{ margin: 0 }}>{authCount}</dd>
    </dl>
  );
}

// ---------------------------------------------------------------------------
// Expanded detail panel — shown below each row when clicked.
// ---------------------------------------------------------------------------

const SECTION_LABEL_STYLE: CSSProperties = {
  fontSize: '12px',
  fontWeight: 700,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  color: 'var(--text-muted)',
  margin: 0,
};

// ---------------------------------------------------------------------------
// Assigned-nodes section — list, assign, and (arm→confirm) unassign.
// ---------------------------------------------------------------------------

function DpNodesSection({ dp }: { dp: DataPlane }) {
  const t = useT();
  const { data: nodes, isLoading, isError, error, refetch } = useDataPlaneNodes(dp.id);
  const assign = useAssignNodeToDataPlane();
  const unassign = useUnassignNodeFromDataPlane();
  const [newNodeId, setNewNodeId] = useState('');
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const nodeInputId = useFieldId('dp-assign-node');

  function handleAssign() {
    const id = newNodeId.trim();
    if (!id) return;
    void assign.mutateAsync({ id: dp.id, nodeId: id })
      .then(() => setNewNodeId(''))
      .catch(() => { /* surfaced below */ });
  }

  function handleUnassign(nodeId: string) {
    if (confirmingId !== nodeId) {
      setConfirmingId(nodeId);
      return;
    }
    void unassign.mutateAsync({ id: dp.id, nodeId })
      .then(() => setConfirmingId(null))
      .catch(() => setConfirmingId(null));
  }

  const rows = nodes ?? [];

  return (
    <div>
      <p style={{ ...SECTION_LABEL_STYLE, marginBottom: '8px' }}>
        {t('platform.dataplanes.nodes.title')}
      </p>
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState
          message={error instanceof Error ? error.message : t('error.dataplaneNodes')}
          onRetry={() => void refetch()}
        />
      )}
      {!isLoading && !isError && rows.length === 0 && (
        <p style={{ fontSize: '13px', color: 'var(--text-muted)', margin: '0 0 10px' }}>
          {t('platform.dataplanes.nodes.empty')}
        </p>
      )}
      {rows.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px', marginBottom: '10px' }}>
          {rows.map((node) => (
            <span
              key={node.id}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: '6px',
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderRadius: 'var(--radius-sm)',
                padding: '3px 8px',
                fontSize: '13px',
              }}
            >
              <code style={{ fontFamily: 'var(--font-mono)', fontSize: '12px' }}>{node.hostname || node.id}</code>
              {node.state && <span className="muted" style={{ fontSize: '11px' }}>{node.state}</span>}
              {confirmingId === node.id ? (
                <>
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={unassign.isPending}
                    onClick={() => handleUnassign(node.id)}
                    data-testid={`unassign-node-confirm-${node.id}`}
                  >
                    {t('platform.dataplanes.nodes.unassignConfirm')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setConfirmingId(null)}>
                    {t('action.cancel')}
                  </Button>
                </>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t('platform.dataplanes.nodes.unassign')}
                  onClick={() => handleUnassign(node.id)}
                  data-testid={`unassign-node-${node.id}`}
                  style={{ padding: '0 4px' }}
                >
                  <IconTrash />
                </Button>
              )}
            </span>
          ))}
        </div>
      )}
      <div style={{ display: 'flex', gap: '8px' }}>
        <input
          id={nodeInputId}
          className="input"
          value={newNodeId}
          placeholder={t('platform.dataplanes.nodes.placeholder')}
          onChange={(e) => setNewNodeId(e.target.value)}
          style={{ maxWidth: 220 }}
          aria-label={t('platform.dataplanes.nodes.assign')}
          data-testid="assign-node-input"
        />
        <Button
          variant="secondary"
          size="sm"
          disabled={!newNodeId.trim() || assign.isPending}
          onClick={handleAssign}
          data-testid="assign-node-btn"
        >
          {t('platform.dataplanes.nodes.assign')}
        </Button>
      </div>
      {assign.isError && (
        <p style={{ color: 'var(--color-danger)', fontSize: '12px', marginTop: '6px' }}>
          {assign.error instanceof Error ? assign.error.message : t('error.dataplaneNodes')}
        </p>
      )}
    </div>
  );
}

function DpDetailPanel({ dp, onRefresh }: { dp: DataPlane; onRefresh: () => void }) {
  const t = useT();
  const refresh = useRefreshDataPlaneConfig();
  const del = useDeleteDataPlane();
  const [editing, setEditing] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  function handleDelete() {
    if (!confirmingDelete) {
      setConfirmingDelete(true);
      return;
    }
    void del.mutateAsync(dp.id)
      .then(() => setConfirmingDelete(false))
      .catch(() => setConfirmingDelete(false));
  }

  return (
    <div
      style={{
        background: 'var(--surface-2)',
        borderRadius: 'var(--radius)',
        padding: '14px 18px',
        display: 'grid',
        gap: '16px',
      }}
    >
      {/* Lifecycle actions: edit + delete (arm→confirm) */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
        <Button size="sm" variant="secondary" onClick={() => setEditing(true)} data-testid="edit-dp-btn">
          {t('platform.dataplanes.edit')}
        </Button>
        <Button
          size="sm"
          variant={confirmingDelete ? 'danger' : 'ghost'}
          onClick={handleDelete}
          disabled={del.isPending}
          aria-label={t('platform.dataplanes.delete')}
          data-testid={confirmingDelete ? 'delete-dp-confirm' : 'delete-dp-btn'}
        >
          {confirmingDelete
            ? t('platform.dataplanes.deleteConfirm', { name: dp.name })
            : (del.isPending ? t('platform.dataplanes.deleting') : t('platform.dataplanes.delete'))}
        </Button>
        {confirmingDelete && (
          <Button size="sm" variant="secondary" onClick={() => setConfirmingDelete(false)}>
            {t('action.cancel')}
          </Button>
        )}
      </div>
      {confirmingDelete && (
        <p style={{ fontSize: '12px', color: 'var(--text-muted)', margin: 0 }}>
          {t('platform.dataplanes.deleteHint')}
        </p>
      )}
      {del.isError && (
        <p style={{ color: 'var(--color-danger)', fontSize: '12px', margin: 0 }}>
          {del.error instanceof Error ? del.error.message : t('platform.dataplanes.delete')}
        </p>
      )}

      {/* Config snapshot */}
      <div>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '8px' }}>
          <p style={SECTION_LABEL_STYLE}>Config snapshot</p>
          <Button
            size="sm"
            variant="secondary"
            disabled={refresh.isPending}
            onClick={() => refresh.mutate(dp.id, { onSuccess: onRefresh })}
            data-testid="refresh-config-btn"
          >
            {refresh.isPending ? 'Refreshing…' : 'Refresh config'}
          </Button>
        </div>
        <ConfigSnapshotSummary snap={dp.configSnapshot as Record<string, unknown> | null | undefined} />
      </div>

      {/* Assigned nodes */}
      <DpNodesSection dp={dp} />

      {/* Data plane ID */}
      <div>
        <p style={{ ...SECTION_LABEL_STYLE, marginBottom: '4px' }}>Data Plane ID</p>
        <code style={{ fontFamily: 'var(--font-mono)', fontSize: '12px', background: 'var(--surface)', padding: '3px 8px', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)' }}>
          {dp.id}
        </code>
      </div>

      {editing && <EditDpModal dp={dp} onClose={() => setEditing(false)} />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Edit modal — mutable DP fields (name, tier, gateway URL, description, status).
// Mirrors the Register modal layout; submits via useUpdateDataPlane.
// ---------------------------------------------------------------------------

function EditDpModal({ dp, onClose }: { dp: DataPlane; onClose: () => void }) {
  const t = useT();
  const update = useUpdateDataPlane();
  const [name, setName] = useState(dp.name);
  const [tier, setTier] = useState(dp.tier || 'production');
  const [gatewayUrl, setGatewayUrl] = useState(dp.gatewayUrl ?? '');
  const [description, setDescription] = useState(dp.description ?? '');
  const nameId = useFieldId('edit-dp-name');
  const tierId = useFieldId('edit-dp-tier');
  const gwId = useFieldId('edit-dp-gw');
  const descId = useFieldId('edit-dp-desc');

  function submit() {
    if (!name.trim()) return;
    void update.mutateAsync({
      id: dp.id,
      input: {
        name: name.trim(),
        tier,
        gatewayUrl: gatewayUrl.trim(),
        description: description.trim(),
      },
    }).then(onClose).catch(() => { /* surfaced below */ });
  }

  return (
    <Modal
      title={t('platform.dataplanes.editTitle')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            variant="primary"
            onClick={submit}
            disabled={update.isPending || !name.trim()}
            data-testid="edit-dp-submit"
          >
            {update.isPending ? t('platform.dataplanes.saving') : t('platform.dataplanes.save')}
          </Button>
        </>
      }
    >
      <Field label={t('platform.dataplanes.field.name')} htmlFor={nameId}>
        <input
          id={nameId}
          className="input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          data-testid="edit-dp-name"
        />
      </Field>
      <Field label={t('platform.dataplanes.field.tier')} htmlFor={tierId}>
        <select id={tierId} className="input" value={tier} onChange={(e) => setTier(e.target.value)} data-testid="edit-dp-tier">
          <option value="production">production</option>
          <option value="staging">staging</option>
          <option value="development">development</option>
        </select>
      </Field>
      <Field label={t('platform.dataplanes.field.gatewayUrl')} htmlFor={gwId}>
        <input
          id={gwId}
          className="input"
          type="url"
          value={gatewayUrl}
          onChange={(e) => setGatewayUrl(e.target.value)}
          data-testid="edit-dp-gateway"
        />
      </Field>
      <Field label={t('platform.dataplanes.field.description')} htmlFor={descId}>
        <input
          id={descId}
          className="input"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      {update.isError && (
        <p style={{ color: 'var(--color-danger)', fontSize: '13px' }}>
          {update.error instanceof Error ? update.error.message : 'Error updating data plane'}
        </p>
      )}
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Empty state — CP→Gateway→Nodes topology diagram.
// Shown when no Data Planes are registered.
// ---------------------------------------------------------------------------

function DataPlanesEmptyState() {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        padding: '40px 20px',
        gap: '20px',
      }}
    >
      {/* ASCII-style topology diagram */}
      <svg
        width="320"
        height="120"
        viewBox="0 0 320 120"
        fill="none"
        aria-hidden="true"
        style={{ color: 'var(--text-muted)' }}
      >
        {/* Control Plane box */}
        <rect x="4" y="30" width="90" height="60" rx="8" stroke="currentColor" strokeWidth="1.5" fill="var(--surface-2)" />
        <text x="49" y="56" textAnchor="middle" fontSize="10" fontWeight="600" fill="currentColor">Control</text>
        <text x="49" y="70" textAnchor="middle" fontSize="10" fontWeight="600" fill="currentColor">Plane</text>

        {/* Arrow CP → DP */}
        <line x1="94" y1="60" x2="140" y2="60" stroke="currentColor" strokeWidth="1.5" strokeDasharray="4 3" />
        <polygon points="140,56 148,60 140,64" fill="currentColor" />
        <text x="121" y="52" textAnchor="middle" fontSize="9" fill="currentColor">mTLS</text>

        {/* Data Plane box */}
        <rect x="148" y="20" width="100" height="80" rx="8" stroke="#0d9488" strokeWidth="2" fill="var(--surface-2)" />
        <text x="198" y="50" textAnchor="middle" fontSize="10" fontWeight="700" fill="#0d9488">Data Plane</text>
        <text x="198" y="64" textAnchor="middle" fontSize="9" fill="currentColor">Gateway</text>
        <text x="198" y="78" textAnchor="middle" fontSize="9" fill="currentColor">+ GPU nodes</text>

        {/* Arrow DP → clients */}
        <line x1="248" y1="60" x2="290" y2="60" stroke="currentColor" strokeWidth="1.5" />
        <polygon points="290,56 298,60 290,64" fill="currentColor" />
        <text x="269" y="52" textAnchor="middle" fontSize="9" fill="currentColor">/v1/</text>

        {/* Clients box */}
        <rect x="298" y="42" width="18" height="36" rx="4" stroke="currentColor" strokeWidth="1.5" fill="var(--surface-2)" />
        <text x="307" y="57" textAnchor="middle" fontSize="8" fill="currentColor">CLI</text>
        <text x="307" y="69" textAnchor="middle" fontSize="8" fill="currentColor">API</text>
      </svg>

      <div style={{ textAlign: 'center', maxWidth: '360px' }}>
        <p style={{ fontWeight: 600, marginBottom: '8px' }}>No Data Planes registered</p>
        <p style={{ color: 'var(--text-muted)', fontSize: '14px', lineHeight: 1.6 }}>
          Register your first Data Plane to connect an inference cluster.
          Each DP registers with a one-time join token and receives config snapshots from this Control Plane.
        </p>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Register modal
// ---------------------------------------------------------------------------

function RegisterDpModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (result: DataPlaneWithToken) => void;
}) {
  const create = useCreateDataPlane();
  const [name, setName] = useState('');
  const [tier, setTier] = useState('production');
  const [gatewayUrl, setGatewayUrl] = useState('');
  const [description, setDescription] = useState('');
  const nameId = useFieldId('dp-name');
  const tierId = useFieldId('dp-tier');
  const gwId = useFieldId('dp-gw');
  const descId = useFieldId('dp-desc');

  const submit = () => {
    if (!name.trim()) return;
    create.mutate(
      { name: name.trim(), tier, gatewayUrl: gatewayUrl.trim(), description: description.trim() },
      { onSuccess: (result) => onCreated(result) },
    );
  };

  return (
    <Modal
      title="Register Data Plane"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            onClick={submit}
            disabled={create.isPending || !name.trim()}
            data-testid="register-dp-submit"
          >
            {create.isPending ? 'Registering…' : 'Register'}
          </Button>
        </>
      }
    >
      <Field label="Name" htmlFor={nameId}>
        <input
          id={nameId}
          className="input"
          value={name}
          placeholder="e.g. prod-cluster"
          onChange={(e) => setName(e.target.value)}
          data-testid="dp-name-input"
        />
      </Field>
      <Field label="Tier" htmlFor={tierId}>
        <select
          id={tierId}
          className="input"
          value={tier}
          onChange={(e) => setTier(e.target.value)}
          data-testid="dp-tier-select"
        >
          <option value="production">production</option>
          <option value="staging">staging</option>
          <option value="development">development</option>
        </select>
      </Field>
      <Field label="Gateway URL" htmlFor={gwId} hint="HTTPS endpoint your inference clients use">
        <input
          id={gwId}
          className="input"
          type="url"
          value={gatewayUrl}
          placeholder="https://gpu.acme.com"
          onChange={(e) => setGatewayUrl(e.target.value)}
          data-testid="dp-gateway-input"
        />
      </Field>
      <Field label="Description" htmlFor={descId}>
        <input
          id={descId}
          className="input"
          value={description}
          placeholder="Optional — e.g. Primary production cluster"
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Join token dialog — shown once after registration.
// ---------------------------------------------------------------------------

function JoinTokenModal({ result, onClose }: { result: DataPlaneWithToken; onClose: () => void }) {
  return (
    <Modal
      title="Join token — save it now"
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose} data-testid="join-token-close">
          I have saved the token
        </Button>
      }
    >
      <div className="notice notice--warning" role="alert" data-testid="join-token-warning">
        This token will not be shown again. Copy it now and paste it into your Gateway configuration.
      </div>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '10px',
          background: 'var(--surface-2)',
          borderRadius: 'var(--radius)',
          padding: '10px 14px',
          border: '1px solid var(--border)',
        }}
      >
        <code
          style={{ fontFamily: 'var(--font-mono)', fontSize: '13px', wordBreak: 'break-all', flex: 1 }}
          data-testid="join-token-value"
        >
          {result.joinToken}
        </code>
        <CopyButton value={result.joinToken} />
      </div>
      <p style={{ fontSize: '13px', color: 'var(--text-muted)' }}>
        Data Plane: <strong>{result.dataplane.name}</strong>
      </p>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function DataPlanesPage() {
  const { data, isLoading, isError, error, refetch } = useDataPlanes();
  const [showRegister, setShowRegister] = useState(false);
  const [newDp, setNewDp] = useState<DataPlaneWithToken | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  return (
    <div className="page">
      <PageHeader
        title="Data Planes"
        subtitle="Registered inference clusters connected to this Control Plane."
        actions={
          <Button
            variant="primary"
            size="sm"
            onClick={() => setShowRegister(true)}
            data-testid="register-dp-btn"
          >
            Register Data Plane
          </Button>
        }
      />

      <Card title="Data Planes">
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState
            message={error instanceof Error ? error.message : 'Failed to load data planes'}
            onRetry={() => refetch()}
          />
        )}
        {data && data.length === 0 && <DataPlanesEmptyState />}
        {data && data.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">Tier</th>
                  <th scope="col">Status</th>
                  <th scope="col">Gateway</th>
                  <th scope="col">Last heartbeat</th>
                  <th scope="col">Nodes</th>
                </tr>
              </thead>
              <tbody>
                {data.map((dp) => (
                  <>
                    <tr
                      key={dp.id}
                      onClick={() => setExpandedId(expandedId === dp.id ? null : dp.id)}
                      tabIndex={0}
                      aria-expanded={expandedId === dp.id}
                      onKeyDown={(e) => e.key === 'Enter' && setExpandedId(expandedId === dp.id ? null : dp.id)}
                      style={{ cursor: 'pointer', ...(dp.status === 'active' ? { borderLeft: '3px solid #0d9488' } : {}) }}
                    >
                      <th scope="row">
                        <span
                          aria-hidden="true"
                          style={{
                            display: 'inline-block',
                            width: '0.8em',
                            marginRight: '6px',
                            fontSize: '0.65em',
                            color: 'var(--text-muted)',
                            transition: 'transform 150ms ease',
                            transform: expandedId === dp.id ? 'rotate(90deg)' : 'rotate(0deg)',
                          }}
                        >
                          ▶
                        </span>
                        {dp.name}
                        {dp.description && (
                          <span style={{ display: 'block', fontSize: '12px', color: 'var(--text-muted)', fontWeight: 400 }}>
                            {dp.description}
                          </span>
                        )}
                      </th>
                      <td><TierBadge tier={dp.tier} /></td>
                      <td><DpStatusPill status={dp.status} /></td>
                      <td>
                        {dp.gatewayUrl ? (
                          <code style={{ fontFamily: 'var(--font-mono)', fontSize: '12px' }}>
                            {dp.gatewayUrl}
                          </code>
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                      <td>
                        {dp.lastHeartbeat ? (
                          <span style={{ fontSize: '13px' }}>{relativeTime(dp.lastHeartbeat)}</span>
                        ) : (
                          <span className="muted">never</span>
                        )}
                      </td>
                      <td>{dp.nodeCount}</td>
                    </tr>
                    {expandedId === dp.id && (
                      <tr key={`${dp.id}-detail`}>
                        <td colSpan={6} style={{ padding: '0 8px 10px 8px', borderTop: 0 }}>
                          <DpDetailPanel dp={dp} onRefresh={() => refetch()} />
                        </td>
                      </tr>
                    )}
                  </>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {showRegister && (
        <RegisterDpModal
          onClose={() => setShowRegister(false)}
          onCreated={(result) => {
            setShowRegister(false);
            setNewDp(result);
          }}
        />
      )}
      {newDp && (
        <JoinTokenModal
          result={newDp}
          onClose={() => setNewDp(null)}
        />
      )}
    </div>
  );
}
