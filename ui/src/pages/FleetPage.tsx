import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  Meter,
  PageHeader,
  StatusPill,
  type Tone,
} from '../components/ui';
import { IconRefresh, IconServer } from '../components/icons';
import { useCapacity, useMetricsStream, useNodes, useNodeAction, useReconcilerStatus } from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { gb, tokS } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { ClusterCapacity, EngineMetrics, LinkQuality, NodeView, ReconcilerStatus } from '../api/types';

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
        <div className="stat">
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
  return (
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
        <Badge tone={LINK_TONE[node.linkQuality]}>{node.linkQuality}</Badge>
      </td>
      <td>
        <div className="row-actions">
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => drain.mutate(id)}>
            {t('fleet.action.drain')}
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => restart.mutate(id)}>
            {t('fleet.action.restart')}
          </Button>
          <Button size="sm" variant="danger" disabled={busy} onClick={() => remove.mutate(id)}>
            {t('fleet.action.remove')}
          </Button>
        </div>
      </td>
    </tr>
  );
}

/** Reconciler Status card — shows active config knobs and any pending-approval
 *  events detected by the control-loop. Exported for unit testing. */
export function ReconcilerStatusCard({ status }: { status: ReconcilerStatus }) {
  const t = useT();
  const pendingEntries = Object.entries(status.tracker).filter(([, v]) => v.tracked > 0);
  const isHealthy = pendingEntries.length === 0;

  return (
    <Card
      title={
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: '0.4rem' }}>
          <IconRefresh />
          {t('fleet.reconciler.title')}
        </span>
      }
    >
      <p className="muted">
        {t('fleet.reconciler.config', {
          intervalS: status.config.intervalS,
          nodeTimeoutS: status.config.nodeTimeoutS,
          cooldownS: status.config.actionCooldownS,
        })}
      </p>
      {isHealthy ? (
        <Badge tone="success">{t('fleet.reconciler.healthy')}</Badge>
      ) : (
        <ul style={{ listStyle: 'none', padding: 0, margin: '0.5rem 0 0', display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
          {pendingEntries.map(([type, summary]) => (
            <li key={type} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <Badge tone="warning">{type}</Badge>
              <span className="muted">
                {t('fleet.reconciler.event_detail', {
                  tracked: summary.tracked,
                  oldestAgeS: summary.oldestAgeS,
                })}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function FleetPage() {
  const t = useT();
  const capacity = useCapacity();
  const nodes = useNodes();
  const reconcilerStatus = useReconcilerStatus();
  // Live hardware metrics via GET /api/v1/metrics (SSE). null until the first
  // frame arrives; each frame carries per-node engine metrics from heartbeats.
  const live = useMetricsStream();

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

      {reconcilerStatus.data && (
        <ReconcilerStatusCard status={reconcilerStatus.data} />
      )}

      <Card title={t('fleet.title')}>
        {nodes.isLoading && <LoadingBlock />}
        {nodes.isError && (
          <ErrorState
            message={errorMessage(nodes.error, t, 'error.fleet')}
            onRetry={() => nodes.refetch()}
          />
        )}
        {nodes.data && nodes.data.length === 0 && (
          <EmptyState icon={<IconServer />} message={t('fleet.empty')} />
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
