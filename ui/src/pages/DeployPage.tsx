import { useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  ErrorState,
  Field,
  LoadingBlock,
  PageHeader,
  ProgressBar,
  StatusPill,
  useFieldId,
} from '../components/ui';
import { IconArrowRight } from '../components/icons';
import {
  useCapacity,
  useCatalog,
  useCreateDeployment,
  useDeployment,
  useModel,
  useNodes,
  usePlan,
  usePlanPreview,
} from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { gb, range } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type {
  DeployOverrides,
  Deployment,
  DeploymentPlan,
  FitVerdict,
} from '../api/types';

const PREFERENCES: DeployOverrides['preference'][] = ['quality', 'balanced', 'speed'];

function useNodeNames(): Record<string, string> {
  const { data } = useNodes();
  return useMemo(() => {
    const map: Record<string, string> = {};
    (data ?? []).forEach((n) => (map[n.profile.nodeId] = n.profile.hostname));
    return map;
  }, [data]);
}

function SplitMap({ plan, names, t }: { plan: DeploymentPlan; names: Record<string, string>; t: TFunc }) {
  const totalLayers = plan.assignments.reduce((s, a) => Math.max(s, a.layerEnd), 0);
  return (
    <div className="split-map">
      {plan.assignments.map((a) => {
        const width = ((a.layerEnd - a.layerStart) / totalLayers) * 100;
        return (
          <div key={a.nodeId} className="split-map__row">
            <div className="split-map__label">
              <span className="split-map__host">{names[a.nodeId] ?? a.nodeId}</span>
              <Badge tone={a.role === 'host' ? 'info' : 'neutral'}>
                {a.role === 'host' ? t('fleet.role.host') : t('fleet.role.worker')}
              </Badge>
            </div>
            <div className="split-map__bar">
              <div
                className={`split-map__fill split-map__fill--${a.role}`}
                style={{ width: `${width}%` }}
              >
                {t('deploy.rollout.layers', { start: a.layerStart, end: a.layerEnd })}
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function WhyPlanCard({ explanation, t }: { explanation: string[]; t: TFunc }) {
  return (
    <Card title={t('deploy.plan.why')}>
      <ul className="reasons">
        {explanation.map((line, i) => (
          <li key={i}>{line}</li>
        ))}
      </ul>
    </Card>
  );
}

function PlanView({
  plan,
  names,
  t,
}: {
  plan: DeploymentPlan;
  names: Record<string, string>;
  t: TFunc;
}) {
  const e = plan.estimated;
  return (
    <>
      <WhyPlanCard explanation={plan.explanation} t={t} />

      <div className="grid grid--2">
        <Card title={t('deploy.plan.split')}>
          <SplitMap plan={plan} names={names} t={t} />
          <p className="muted pipeline-order">
            {t('deploy.plan.pipeline')}: {plan.pipelineOrder.map((id) => names[id] ?? id).join(' → ')}
          </p>
        </Card>

        <Card title={t('deploy.plan.perf')}>
          <dl className="perf">
            <div>
              <dt>{t('deploy.plan.decode')}</dt>
              <dd>{range(e.decodeTokSMin, e.decodeTokSMax, 'tok/s')}</dd>
            </div>
            <div>
              <dt>{t('deploy.plan.prefill')}</dt>
              <dd>{range(e.prefillTokSMin, e.prefillTokSMax, 'tok/s')}</dd>
            </div>
            <div>
              <dt>{t('deploy.plan.headroom')}</dt>
              <dd>{gb(e.headroomGb)}</dd>
            </div>
          </dl>
          <p className="muted">{t('deploy.perf.rangeNote')}</p>
        </Card>
      </div>
    </>
  );
}

function RolloutView({ id, t }: { id: string; t: TFunc }) {
  const { data, isLoading, isError, error, refetch } = useDeployment(id);
  const names = useNodeNames();
  // Authoritative plan + explanation from GET /api/v1/plans/{id}.
  const planQ = usePlan(data?.plan.planId);
  if (isLoading) return <LoadingBlock />;
  if (isError || !data)
    return <ErrorState message={errorMessage(error, t, 'error.rollout')} onRetry={() => refetch()} />;

  const dep: Deployment = data;
  const active = dep.state === 'active';
  // Prefer the plan fetched from /plans/{id}; fall back to the embedded copy.
  const explanation = planQ.data?.explanation ?? dep.plan.explanation;
  return (
    <>
    {explanation.length > 0 && <WhyPlanCard explanation={explanation} t={t} />}
    <Card
      title={t('deploy.rollout.title')}
      action={
        active ? (
          <Link to="/playground" className="btn btn--primary btn--sm link-btn">
            <span>{t('deploy.openPlayground')}</span>
            <IconArrowRight />
          </Link>
        ) : undefined
      }
    >
      <p className={`rollout-state${active ? ' rollout-state--ok' : ''}`} aria-live="polite">
        {active ? t('deploy.state.active') : t('deploy.state.provisioning')}
      </p>
      <ul className="rollout">
        {dep.nodeStatus.map((s) => (
          <li key={s.nodeId} className="rollout__item">
            <div className="rollout__head">
              <span className="rollout__host">{names[s.nodeId] ?? s.nodeId}</span>
              <StatusPill state={s.state} />
            </div>
            <ProgressBar value={s.progress} label={`${names[s.nodeId] ?? s.nodeId} load progress`} />
            <span className="muted rollout__detail">{s.detail}</span>
          </li>
        ))}
      </ul>
    </Card>
    </>
  );
}

function CantFit({ fit, t }: { fit: FitVerdict; t: TFunc }) {
  let detail: string;
  if (fit.reasonKey === 'needs_fp4') detail = t('catalog.needFp4');
  else if (fit.reasonKey === 'no_ready_nodes') detail = t('catalog.noNodes');
  else detail = t('catalog.deficit.detail', { gb: gb(fit.deficitGb) });
  return (
    <ErrorState
      title={t('deploy.cantFit.title')}
      message={`${detail}. ${t('deploy.cantFit.body')}`}
      action={
        <Link to="/" className="btn btn--secondary btn--sm">
          {t('onboarding.goToFleet')}
        </Link>
      }
    />
  );
}

export function DeployPage() {
  const t = useT();
  const { modelId } = useParams();
  const model = useModel(modelId);
  const catalog = useCatalog();
  const capacity = useCapacity();
  const names = useNodeNames();

  const [preference, setPreference] = useState<DeployOverrides['preference']>('balanced');
  const [forceNodeCount, setForceNodeCount] = useState<number | null>(null);
  const overrides: DeployOverrides = { preference, forceNodeCount };

  const fit: FitVerdict | undefined = catalog.data?.find((c) => c.model.modelId === modelId)?.fit;
  const fits = fit?.fits ?? false;

  const preview = usePlanPreview(fits ? modelId : undefined, overrides);
  const create = useCreateDeployment();
  const [launchedId, setLaunchedId] = useState<string | null>(null);

  const nodeCountId = useFieldId('nodecount');
  const maxNodes = capacity.data?.readyNodeCount ?? 1;

  if (model.isLoading || catalog.isLoading) return <div className="page"><LoadingBlock /></div>;
  if (model.isError || !model.data)
    return (
      <div className="page">
        <ErrorState
          message={errorMessage(model.error, t, 'error.model')}
          onRetry={() => model.refetch()}
        />
      </div>
    );

  return (
    <div className="page">
      <PageHeader
        title={t('deploy.title', { model: model.data.family })}
        subtitle={model.data.modelId}
      />

      {!fits && fit && <CantFit fit={fit} t={t} />}

      {fits && (
        <>
          <Card title={t('deploy.overrides.title')}>
            <div className="overrides">
              <Field label={t('deploy.overrides.nodes')} htmlFor={nodeCountId}>
                <select
                  id={nodeCountId}
                  className="select"
                  value={forceNodeCount ?? 'auto'}
                  onChange={(e) =>
                    setForceNodeCount(e.target.value === 'auto' ? null : Number(e.target.value))
                  }
                >
                  <option value="auto">{t('deploy.overrides.auto', { n: fit?.nodesNeeded ?? 1 })}</option>
                  {Array.from({ length: maxNodes }, (_, i) => i + 1).map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </select>
              </Field>

              <div className="field">
                <span className="field__label" id="pref-label">
                  {t('deploy.overrides.pref')}
                </span>
                <div className="segmented" role="group" aria-labelledby="pref-label">
                  {PREFERENCES.map((p) => (
                    <button
                      key={p}
                      type="button"
                      className={`segmented__btn${preference === p ? ' segmented__btn--active' : ''}`}
                      aria-pressed={preference === p}
                      onClick={() => setPreference(p)}
                    >
                      {t(`deploy.pref.${p}`)}
                    </button>
                  ))}
                </div>
              </div>

              <div className="overrides__launch">
                <Button
                  variant="primary"
                  disabled={create.isPending || !preview.data}
                  onClick={() =>
                    create.mutate(
                      { modelId: model.data!.modelId, overrides },
                      { onSuccess: (dep) => setLaunchedId(dep.id) },
                    )
                  }
                >
                  {launchedId ? t('deploy.action.relaunch') : t('deploy.action.launch')}
                </Button>
              </div>
            </div>
          </Card>

          {create.isError && (
            <ErrorState
              title={t('deploy.cantFit.title')}
              message={errorMessage(create.error, t, 'error.deployFailed')}
            />
          )}

          {/* Pre-launch: show the previewed plan. Post-launch: the rollout view
              carries the authoritative plan from GET /api/v1/plans/{id}. */}
          {!launchedId && preview.isLoading && <LoadingBlock />}
          {!launchedId && preview.isError && (
            <ErrorState
              message={errorMessage(preview.error, t, 'error.plan')}
              onRetry={() => preview.refetch()}
            />
          )}
          {!launchedId && preview.data && <PlanView plan={preview.data} names={names} t={t} />}

          {launchedId && <RolloutView id={launchedId} t={t} />}
        </>
      )}
    </div>
  );
}
