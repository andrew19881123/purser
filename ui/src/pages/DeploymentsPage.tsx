import { Link } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
  ProgressBar,
  StatusPill,
  type Tone,
} from '../components/ui';
import { IconArrowRight, IconLayers } from '../components/icons';
import { useDeployments, useModelHealth, useNodes, useUndeploy } from '../hooks/queries';
import { useT } from '../i18n';
import { useMemo } from 'react';
import { errorMessage } from '../lib/errors';
import type { Deployment, DeploymentState, ModelHealthStatus } from '../api/types';

const DEP_TONE: Record<DeploymentState, Tone> = {
  planned: 'info',
  provisioning: 'info',
  active: 'success',
  rebalancing: 'warning',
  stopping: 'warning',
  stopped: 'neutral',
  failed: 'danger',
};

const HEALTH_TONE: Record<ModelHealthStatus, Tone> = {
  healthy: 'success',
  degraded: 'warning',
  unavailable: 'danger',
};

const HEALTH_LABEL: Record<ModelHealthStatus, string> = {
  healthy: 'Healthy',
  degraded: 'Degraded',
  unavailable: 'Unavailable',
};

/** Fetches and renders a health badge for a single model. Uses its own hook
 *  so each card can call useModelHealth without violating the rules of hooks. */
export function DeploymentHealthBadge({ modelId }: { modelId: string }) {
  const { data, isLoading } = useModelHealth(modelId);

  if (isLoading || !data) {
    return <Badge tone="neutral">—</Badge>;
  }

  return <Badge tone={HEALTH_TONE[data.status]}>{HEALTH_LABEL[data.status]}</Badge>;
}

function DeploymentCard({
  dep,
  names,
}: {
  dep: Deployment;
  names: Record<string, string>;
}) {
  const t = useT();
  const undeploy = useUndeploy();
  const heading = (state: DeploymentState): string =>
    state === 'active' ? t('deploy.state.active') : t('deploy.state.provisioning');

  const onUndeploy = () => {
    if (window.confirm(t('deployments.undeployConfirm', { model: dep.plan.modelId }))) {
      undeploy.mutate(dep.id);
    }
  };

  return (
    <Card className="dep-card">
      <div className="dep-card__head">
        <div>
          <h3 className="model-card__name">{dep.plan.modelId}</h3>
          <p className="model-card__id">
            {dep.plan.quantization} · {dep.plan.assignments.length}{' '}
            {t('fleet.capacity.nodes').toLowerCase()}
          </p>
        </div>
        <div className="dep-card__badges">
          <Badge tone={DEP_TONE[dep.state]}>{heading(dep.state)}</Badge>
          <DeploymentHealthBadge modelId={dep.plan.modelId} />
        </div>
      </div>
      <ul className="dep-card__nodes">
        {dep.nodeStatus.map((s) => (
          <li key={s.nodeId}>
            <div className="rollout__head">
              <span className="muted">{names[s.nodeId] ?? s.nodeId}</span>
              <StatusPill state={s.state} />
            </div>
            {s.state !== 'running' && <ProgressBar value={s.progress} />}
          </li>
        ))}
      </ul>
      <div className="model-card__actions">
        <Link to={`/deploy/${dep.plan.modelId}`} className="btn btn--secondary btn--sm link-btn">
          <span>{t('deploy.plan.title')}</span>
          <IconArrowRight />
        </Link>
        <Button variant="danger" size="sm" disabled={undeploy.isPending} onClick={onUndeploy}>
          {t('deployments.undeploy')}
        </Button>
      </div>
    </Card>
  );
}

export function DeploymentsPage() {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useDeployments();
  const nodes = useNodes();
  const names = useMemo(() => {
    const m: Record<string, string> = {};
    (nodes.data ?? []).forEach((n) => (m[n.profile.nodeId] = n.profile.hostname));
    return m;
  }, [nodes.data]);

  return (
    <div className="page">
      <PageHeader title={t('nav.deployments')} />
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState message={errorMessage(error, t, 'error.deployments')} onRetry={() => refetch()} />
      )}
      {data && data.length === 0 && (
        <EmptyState
          icon={<IconLayers />}
          message={t('deployments.empty')}
          action={
            <Link to="/catalog" className="btn btn--primary btn--md link-btn">
              <span>{t('nav.catalog')}</span>
              <IconArrowRight />
            </Link>
          }
        />
      )}
      {data && data.length > 0 && (
        <div className="grid grid--cards">
          {data.map((dep) => (
            <DeploymentCard key={dep.id} dep={dep} names={names} />
          ))}
        </div>
      )}
    </div>
  );
}
