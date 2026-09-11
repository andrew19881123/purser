import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  Meter,
  Modal,
  PageHeader,
  StatusPill,
  type Tone,
} from '../components/ui';
import { IconServer } from '../components/icons';
import {
  useCapacity,
  useMetricsStream,
  useNodes,
  useNodeAction,
  useReconcilerStatus,
  useSloCompliance,
  type ReconcilerStatus,
} from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { gb, tokS } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { ClusterCapacity, EngineMetrics, LinkQuality, NodeView, SloModelCompliance } from '../api/types';

const LINK_TONE: Record<LinkQuality, Tone> = {
  excellent: 'success',
  good: 'success',
  fair: 'warning',
  poor: 'danger',
  unknown: 'neutral',
};

function CapacityCard({
  cap,
  liveDecodeTokS,
  t,
}: {
  cap: ClusterCapacity;
  liveDecodeTokS?: number;
  t: TFunc;
}) {
  // Prefer the live SSE aggregate when a metrics stream is active.
  const decode = liveDecodeTokS ?? cap.aggregateDecodeTokS;
  return (
    <Card title={t('fleet.capacity.title')}>
      <p className="muted capacity__hint">{t('fleet.capacity.hint')}</p>
      <div className="stat-grid">
        <div className="stat">
          <span className="stat__value">
            {cap.readyNodeCount}
            <span className="stat__sub">
              {' '}
              {t('common.of')} {cap.nodeCount}
            </span>
          </span>
          <span className="stat__label">{t('fleet.capacity.nodes')}</span>
        </div>
        <div className="stat">
          <span className="stat__value">{cap.gpuCount}</span>
          <span className="stat__label">{t('fleet.capacity.gpus')}</span>
        </div>
        <div className="stat">
          <span className="stat__value" aria-live="polite">{tokS(decode)}</span>
          <span className="stat__label">{t('fleet.capacity.throughput')}</span>
        </div>
        <div
          className="stat"
          title="FP4 (4-bit floating point) quantization acceleration. Reduces memory usage and increases throughput on compatible hardware."
        >
          <span className="stat__value">
            <Badge tone={cap.fp4Capable ? 'success' : 'neutral'}>
              {cap.fp4Capable ? t('fleet.capacity.fp4.yes') : t('fleet.capacity.fp4.no')}
            </Badge>
          </span>
          <span className="stat__label">{t('fleet.capacity.fp4')}</span>
        </div>
      </div>
      <div className="capacity__meters">
        <Meter used={cap.ramTotalGb - cap.ramAvailableGb} total={cap.ramTotalGb} label={t('fleet.capacity.ram')} />
        <Meter
          used={cap.vramTotalGb - cap.vramAvailableGb}
          total={cap.vramTotalGb}
          label={t('fleet.capacity.vram')}
        />
      </div>
    </Card>
  );
}

function hardwareSummary(n: NodeView): string {
  const gpu =
    n.profile.gpus.length > 0
      ? n.profile.gpus.map((g) => `${g.count}× ${g.name} (${g.vramGb}GB)`).join(', ')
      : 'CPU only';
  return `${gpu} · ${gb(n.profile.ramTotalGb)} RAM · ${n.profile.backends.join('/')}`;
}

/**
 * Shows the control-plane reconciler health status. Handles three states:
 *   - loading  → render nothing while the first fetch is in-flight (TODO: skeleton)
 *   - error    → the endpoint is absent or returned an error; show a neutral
 *                "Status unknown" badge so the operator knows the card is present
 *                but unavailable, rather than silently disappearing.
 *   - data     → state badge + pending/error counts + active event list +
 *                collapsible configuration panel.
 *
 * P-12: the previous `{reconcilerStatus.data && <ReconcilerStatusCard .../>}`
 * guard hid the card entirely during loading and on API error, making it
 * impossible for an operator to distinguish "reconciler not supported" from
 * "dashboard bug". This version keeps the card visible in all states.
 */
export function ReconcilerStatusCard({
  status,
}: {
  status: ReconcilerStatus | undefined;
}) {
  const STATE_TONE: Record<ReconcilerStatus['state'], Tone> = {
    idle: 'success',
    syncing: 'info',
    error: 'danger',
  };

  if (!status) {
    return (
      <Card title="Reconciler">
        <p>
          <Badge tone="neutral">Status unknown</Badge>
          {' '}
          Reconciler status unknown
        </p>
      </Card>
    );
  }

  // Active tracker events: only event types that currently have tracked > 0.
  const activeEvents = Object.entries(status.tracker).filter(([, v]) => v.tracked > 0);

  return (
    <Card title="Reconciler">
      <div className="stat-grid">
        <div className="stat">
          <span className="stat__value">
            <Badge tone={STATE_TONE[status.state]}>{status.state}</Badge>
          </span>
          <span className="stat__label">State</span>
        </div>
        <div className="stat">
          <span className="stat__value">{status.pendingCount}</span>
          <span className="stat__label">Pending</span>
        </div>
        <div className="stat">
          <span className="stat__value">{status.errorCount}</span>
          <span className="stat__label">Errors</span>
        </div>
      </div>

      {activeEvents.length > 0 && (
        <div className="reconciler__events">
          <p className="muted">Active events</p>
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Event type</th>
                  <th scope="col">Tracked</th>
                  <th scope="col">Age (s)</th>
                </tr>
              </thead>
              <tbody>
                {activeEvents.map(([type, v]) => (
                  <tr key={type}>
                    <td><code>{type}</code></td>
                    <td>{v.tracked}</td>
                    <td>{v.oldestAgeS.toFixed(0)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <details className="reconciler__config">
        <summary className="muted">Configuration</summary>
        <dl className="reconciler__config-grid">
          <dt>Interval</dt>
          <dd data-testid="cfg-interval">{status.config.intervalS}s</dd>
          <dt>Node timeout</dt>
          <dd data-testid="cfg-node-timeout">{status.config.nodeTimeoutS}s</dd>
          <dt>Hysteresis</dt>
          <dd>{status.config.hysteresisS}s</dd>
          <dt>Action cooldown</dt>
          <dd>{status.config.actionCooldownS}s</dd>
        </dl>
      </details>

      {status.lastSyncAt && (
        <p className="muted">
          Last sync:{' '}
          <time dateTime={status.lastSyncAt}>
            {new Date(status.lastSyncAt).toLocaleString()}
          </time>
        </p>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// SLO Status card
// ---------------------------------------------------------------------------

const SLO_TONE: Record<SloModelCompliance['status'], Tone> = {
  met: 'success',
  breached: 'danger',
  insufficient_data: 'neutral',
};

function SloStatusCard({ t }: { t: TFunc }) {
  const { data, isLoading, isError, error } = useSloCompliance(24);

  return (
    <Card title={t('slo.title')}>
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState message={errorMessage(error, t, 'error.slo')} />
      )}
      {data && data.models.length === 0 && (
        <EmptyState message={t('slo.empty')} />
      )}
      {data && data.models.length > 0 && (
        <div className="table-wrap">
          <table className="table" data-testid="slo-table">
            <thead>
              <tr>
                <th scope="col">{t('slo.col.model')}</th>
                <th scope="col">{t('slo.col.ttftTarget')}</th>
                <th scope="col">{t('slo.col.compliance')}</th>
                <th scope="col">{t('slo.col.status')}</th>
              </tr>
            </thead>
            <tbody>
              {data.models.map((m) => (
                <tr key={m.model_id}>
                  <td>{m.model_id}</td>
                  <td>{m.ttft_target_ms}</td>
                  <td>{m.ttft_actual_compliance_pct.toFixed(1)}%</td>
                  <td>
                    <Badge tone={SLO_TONE[m.status]}>
                      {m.status === 'met'
                        ? t('slo.status.met')
                        : m.status === 'breached'
                          ? t('slo.status.breached')
                          : t('slo.status.insufficientData')}
                    </Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function NodeRow({
  node,
  liveMetrics,
  t,
}: {
  node: NodeView;
  /** Live hardware metrics from the SSE stream; null if the node has not yet reported. */
  liveMetrics: EngineMetrics | null;
  t: TFunc;
}) {
  const { drain, restart, remove } = useNodeAction();
  const id = node.profile.nodeId;
  const busy = drain.isPending || restart.isPending || remove.isPending;
  // Prefer SSE live data; fall back to REST snapshot metrics.
  const metrics = liveMetrics ?? node.metrics;
  const [showRemoveModal, setShowRemoveModal] = useState(false);
  return (
    <>
      <tr>
        <th scope="row" className="node-cell">
          <span className="node-cell__host">{node.profile.hostname}</span>
          <span className="node-cell__meta">
            {node.profile.os}/{node.profile.arch}
            {node.role && (
              <>
                {' · '}
                <Badge tone="info">{node.role === 'host' ? t('fleet.role.host') : t('fleet.role.worker')}</Badge>
              </>
            )}
            {node.profile.gpus.some((g) => g.fp4Native) && <Badge tone="success">FP4</Badge>}
          </span>
        </th>
        <td>
          <StatusPill state={node.profile.state} />
        </td>
        <td className="hw-cell">{hardwareSummary(node)}</td>
        <td>
          {metrics ? (
            <div className="load-cell">
              <span>{tokS(metrics.decodeTokS)}</span>
              <span className="muted">queue {metrics.queueDepth}</span>
            </div>
          ) : (
            <span className="muted">{t('common.na')}</span>
          )}
        </td>
        <td>
          <span title="Network link quality measured as round-trip latency to the control plane">
            <Badge tone={LINK_TONE[node.linkQuality]}>{node.linkQuality}</Badge>
          </span>
        </td>
        <td>
          <div className="row-actions">
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => {
                if (window.confirm(t('fleet.confirm.drain', { node: id }))) {
                  drain.mutate(id);
                }
              }}
            >
              {t('fleet.action.drain')}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => restart.mutate(id)}>
              {t('fleet.action.restart')}
            </Button>
            <Button size="sm" variant="danger" disabled={busy} onClick={() => setShowRemoveModal(true)}>
              {t('fleet.action.remove')}
            </Button>
          </div>
        </td>
      </tr>
      {showRemoveModal && (
        <Modal
          title={t('fleet.confirm.removeTitle')}
          onClose={() => setShowRemoveModal(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setShowRemoveModal(false)}>
                {t('action.cancel')}
              </Button>
              <Button
                variant="danger"
                onClick={() => {
                  remove.mutate(id);
                  setShowRemoveModal(false);
                }}
              >
                {t('fleet.action.remove')}
              </Button>
            </>
          }
        >
          {t('fleet.confirm.removeBody', { node: id })}
        </Modal>
      )}
    </>
  );
}

export function FleetPage() {
  const t = useT();
  const capacity = useCapacity();
  const nodes = useNodes();
  const reconcilerStatus = useReconcilerStatus();
  // Live hardware metrics via GET /api/v1/metrics (SSE). null until the first
  // frame arrives; each frame carries per-node engine metrics from heartbeats.
  const { snapshot: live, streamError } = useMetricsStream();

  // Build a fast lookup: nodeId → live EngineMetrics from the SSE stream.
  // When a node has not yet reported, its entry is absent and NodeRow falls
  // back to the REST snapshot metrics (or shows n/a).
  const liveByNode: Record<string, EngineMetrics> = {};
  if (live?.nodes) {
    for (const sample of live.nodes) {
      // Zero-metric nodes (not yet reported) produce all-zero metrics objects.
      // Only expose them as live data when at least one metric is non-zero,
      // so the fallback to REST data is used for truly silent nodes.
      const m = sample.metrics;
      if (m.decodeTokS > 0 || m.prefillTokS > 0 || m.ramUsedGb > 0 || m.vramUsedGb > 0) {
        liveByNode[sample.nodeId] = m;
      }
    }
  }

  return (
    <div className="page">
      <PageHeader title={t('fleet.title')} subtitle={t('fleet.subtitle')} />
      {streamError && (
        <Badge tone="warning">{t('fleet.metrics.stale')}</Badge>
      )}

      {capacity.isLoading && <LoadingBlock />}
      {capacity.isError && (
        <ErrorState
          message={errorMessage(capacity.error, t, 'error.capacity')}
          onRetry={() => capacity.refetch()}
        />
      )}
      {capacity.data && (
        <CapacityCard cap={capacity.data} liveDecodeTokS={live?.aggregateDecodeTokS} t={t} />
      )}

      {/* P-12: Reconciler card is always rendered (never conditionally hidden).
          - isLoading: render nothing until the first response (TODO: skeleton)
          - isError / no data: "Status unknown" badge — endpoint absent or unreachable
          - data: full status card */}
      {!reconcilerStatus.isLoading && (
        <ReconcilerStatusCard status={reconcilerStatus.data} />
      )}

      <SloStatusCard t={t} />

      <Card title={t('fleet.title')}>
        {nodes.isLoading && <LoadingBlock />}
        {nodes.isError && (
          <ErrorState
            message={errorMessage(nodes.error, t, 'error.fleet')}
            onRetry={() => nodes.refetch()}
          />
        )}
        {nodes.data && nodes.data.length === 0 && (
          <EmptyState
            icon={<IconServer />}
            message={t('fleet.empty')}
            action={
              <Link to="/" className="btn btn--primary btn--sm link-btn">
                {t('nav.onboarding')}
              </Link>
            }
          />
        )}
        {nodes.data && nodes.data.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('fleet.col.node')}</th>
                  <th scope="col">{t('fleet.col.state')}</th>
                  <th scope="col">{t('fleet.col.hardware')}</th>
                  <th scope="col">{t('fleet.col.load')}</th>
                  <th scope="col">{t('fleet.col.link')}</th>
                  <th scope="col">{t('fleet.col.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {nodes.data.map((n) => (
                  <NodeRow
                    key={n.profile.nodeId}
                    node={n}
                    liveMetrics={liveByNode[n.profile.nodeId] ?? null}
                    t={t}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
