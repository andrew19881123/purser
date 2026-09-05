// ---------------------------------------------------------------------------
// Model Studio — the flagship v0.2 operator page.
//
// Four-step guided flow:
//   Step 1 — Source selector  (tabs for 6 import sources)
//   Step 2 — Model info card  (shown after "Inspect model")
//   Step 3 — Deployment preview (feasible: split diagram / infeasible: reason)
//   Step 4 — Override & deploy (node filter, quant picker, Deploy / Import-only)
//
// State is purely local (useState) — no global store. All API calls go through
// React Query mutations so loading/error states are handled uniformly.
// ---------------------------------------------------------------------------
import { useMemo, useState } from 'react';
import {
  Badge,
  Button,
  Card,
  ErrorState,
  Field,
  LoadingBlock,
  PageHeader,
  Spinner,
  Tabs,
  TabPanel,
  useFieldId,
} from '../components/ui';
import { IconBox, IconCheck, IconWarning } from '../components/icons';
import { useCatalog, useDeployModel, useImportModel, usePreviewModelPlan } from '../hooks/queries';
import { useNodes } from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { billions, gb, range } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type {
  AzureMLSource,
  DeploymentPlan,
  HuggingFaceSource,
  ImportSource,
  ImportSourceType,
  ModelSpec,
  ObjectStorageSource,
  PlanPreviewResult,
  SageMakerSource,
  VertexAISource,
} from '../api/types';

// ---------------------------------------------------------------------------
// Step 1 — source forms
// ---------------------------------------------------------------------------

const SOURCE_TABS: { id: ImportSourceType; labelKey: string }[] = [
  { id: 'huggingface',    labelKey: 'studio.source.huggingface' },
  { id: 'object_storage', labelKey: 'studio.source.object_storage' },
  { id: 'sagemaker',      labelKey: 'studio.source.sagemaker' },
  { id: 'vertexai',       labelKey: 'studio.source.vertexai' },
  { id: 'azure_ml',       labelKey: 'studio.source.azure_ml' },
  { id: 'catalog',        labelKey: 'studio.source.catalog' },
];

// --- HuggingFace form -------------------------------------------------------

function HuggingFaceForm({
  value,
  onChange,
  t,
}: {
  value: Partial<HuggingFaceSource>;
  onChange: (v: Partial<HuggingFaceSource>) => void;
  t: TFunc;
}) {
  const repoId = useFieldId('hf-repo');
  const revId  = useFieldId('hf-rev');
  const fnId   = useFieldId('hf-fn');
  return (
    <div className="form-stack">
      <Field label={t('studio.hf.repo')} htmlFor={repoId}>
        <input
          id={repoId}
          className="input"
          type="text"
          placeholder={t('studio.hf.repo.placeholder')}
          value={value.repo ?? ''}
          onChange={(e) => onChange({ ...value, repo: e.target.value })}
        />
      </Field>
      <Field label={t('studio.hf.revision')} htmlFor={revId}>
        <input
          id={revId}
          className="input"
          type="text"
          placeholder={t('studio.hf.revision.placeholder')}
          value={value.revision ?? ''}
          onChange={(e) => onChange({ ...value, revision: e.target.value })}
        />
      </Field>
      <Field label={t('studio.hf.filename')} htmlFor={fnId}>
        <input
          id={fnId}
          className="input"
          type="text"
          placeholder={t('studio.hf.filename.placeholder')}
          value={value.filenamePattern ?? ''}
          onChange={(e) => onChange({ ...value, filenamePattern: e.target.value })}
        />
      </Field>
    </div>
  );
}

// --- Object Storage form ----------------------------------------------------

function ObjectStorageForm({
  value,
  onChange,
  t,
}: {
  value: Partial<ObjectStorageSource>;
  onChange: (v: Partial<ObjectStorageSource>) => void;
  t: TFunc;
}) {
  const uriId    = useFieldId('obj-uri');
  const nameId   = useFieldId('obj-name');
  const familyId = useFieldId('obj-family');
  return (
    <div className="form-stack">
      <Field label={t('studio.obj.uri')} htmlFor={uriId}>
        <input
          id={uriId}
          className="input"
          type="text"
          placeholder={t('studio.obj.uri.placeholder')}
          value={value.uri ?? ''}
          onChange={(e) => onChange({ ...value, uri: e.target.value })}
        />
      </Field>
      <Field label={t('studio.obj.name')} htmlFor={nameId}>
        <input
          id={nameId}
          className="input"
          type="text"
          value={value.name ?? ''}
          onChange={(e) => onChange({ ...value, name: e.target.value })}
        />
      </Field>
      <Field label={t('studio.obj.family')} htmlFor={familyId}>
        <input
          id={familyId}
          className="input"
          type="text"
          value={value.family ?? ''}
          onChange={(e) => onChange({ ...value, family: e.target.value })}
        />
      </Field>
    </div>
  );
}

// --- SageMaker form ---------------------------------------------------------

function SageMakerForm({
  value,
  onChange,
  t,
}: {
  value: Partial<SageMakerSource>;
  onChange: (v: Partial<SageMakerSource>) => void;
  t: TFunc;
}) {
  const mgId  = useFieldId('sm-mg');
  const verId = useFieldId('sm-ver');
  return (
    <div className="form-stack">
      <Field label={t('studio.sm.modelGroup')} htmlFor={mgId}>
        <input
          id={mgId}
          className="input"
          type="text"
          value={value.modelGroup ?? ''}
          onChange={(e) => onChange({ ...value, modelGroup: e.target.value })}
        />
      </Field>
      <Field label={t('studio.sm.version')} htmlFor={verId}>
        <input
          id={verId}
          className="input"
          type="text"
          value={value.version ?? ''}
          onChange={(e) => onChange({ ...value, version: e.target.value })}
        />
      </Field>
    </div>
  );
}

// --- Vertex AI form ---------------------------------------------------------

function VertexAIForm({
  value,
  onChange,
  t,
}: {
  value: Partial<VertexAISource>;
  onChange: (v: Partial<VertexAISource>) => void;
  t: TFunc;
}) {
  const pathId = useFieldId('vertex-path');
  const verId  = useFieldId('vertex-ver');
  return (
    <div className="form-stack">
      <Field label={t('studio.vertex.modelPath')} htmlFor={pathId}>
        <input
          id={pathId}
          className="input"
          type="text"
          placeholder="projects/my-project/locations/us-central1/models/123"
          value={value.modelPath ?? ''}
          onChange={(e) => onChange({ ...value, modelPath: e.target.value })}
        />
      </Field>
      <Field label={t('studio.vertex.version')} htmlFor={verId}>
        <input
          id={verId}
          className="input"
          type="text"
          value={value.version ?? ''}
          onChange={(e) => onChange({ ...value, version: e.target.value })}
        />
      </Field>
    </div>
  );
}

// --- Azure ML form ----------------------------------------------------------

function AzureMLForm({
  value,
  onChange,
  t,
}: {
  value: Partial<AzureMLSource>;
  onChange: (v: Partial<AzureMLSource>) => void;
  t: TFunc;
}) {
  const wsId    = useFieldId('azure-ws');
  const nameId  = useFieldId('azure-name');
  const verId   = useFieldId('azure-ver');
  return (
    <div className="form-stack">
      <Field label={t('studio.azure.workspace')} htmlFor={wsId}>
        <input
          id={wsId}
          className="input"
          type="text"
          placeholder="my-workspace"
          value={value.workspace ?? ''}
          onChange={(e) => onChange({ ...value, workspace: e.target.value })}
        />
      </Field>
      <Field label={t('studio.azure.modelName')} htmlFor={nameId}>
        <input
          id={nameId}
          className="input"
          type="text"
          value={value.modelName ?? ''}
          onChange={(e) => onChange({ ...value, modelName: e.target.value })}
        />
      </Field>
      <Field label={t('studio.azure.version')} htmlFor={verId}>
        <input
          id={verId}
          className="input"
          type="text"
          value={value.version ?? ''}
          onChange={(e) => onChange({ ...value, version: e.target.value })}
        />
      </Field>
    </div>
  );
}

// --- Catalog picker ---------------------------------------------------------

function CatalogPicker({
  onSelect,
  t,
}: {
  onSelect: (model: ModelSpec) => void;
  t: TFunc;
}) {
  const { data: catalog, isLoading } = useCatalog();
  const selectId = useFieldId('catalog-pick');
  const [modelId, setModelId] = useState('');

  if (isLoading) return <LoadingBlock />;
  if (!catalog || catalog.length === 0)
    return <p className="muted">{t('studio.catalog.empty')}</p>;

  return (
    <div className="form-stack">
      <Field label={t('studio.catalog.select')} htmlFor={selectId}>
        <select
          id={selectId}
          className="select"
          value={modelId}
          onChange={(e) => setModelId(e.target.value)}
        >
          <option value="">— select —</option>
          {catalog.map((e) => (
            <option key={e.model.modelId} value={e.model.modelId}>
              {e.model.family} ({e.model.modelId})
            </option>
          ))}
        </select>
      </Field>
      <Button
        variant="primary"
        disabled={!modelId}
        onClick={() => {
          const entry = catalog.find((e) => e.model.modelId === modelId);
          if (entry) onSelect(entry.model);
        }}
      >
        {t('studio.action.inspect')}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 2 — Model info card
// ---------------------------------------------------------------------------

const SOURCE_LABELS: Record<ImportSourceType, string> = {
  huggingface: 'HuggingFace Hub',
  object_storage: 'Object Storage',
  sagemaker: 'SageMaker',
  vertexai: 'Vertex AI',
  azure_ml: 'Azure ML',
  catalog: 'Purser Catalog',
};

function ModelInfoCard({
  model,
  sourceType,
  t,
}: {
  model: ModelSpec;
  sourceType: ImportSourceType;
  t: TFunc;
}) {
  const smallest = model.quantizations.reduce(
    (a, b) => (a.sizeGb < b.sizeGb ? a : b),
    model.quantizations[0],
  );
  return (
    <Card title={t('studio.model.title')}>
      <div className="studio-model">
        <div className="studio-model__head">
          <h3 className="studio-model__name">{model.family}</h3>
          <code className="studio-model__id">{model.modelId}</code>
        </div>

        <dl className="spec-list">
          <div>
            <dt>{t('studio.model.source')}</dt>
            <dd><Badge tone="info">{SOURCE_LABELS[sourceType]}</Badge></dd>
          </div>
          <div>
            <dt>{t('studio.model.params')}</dt>
            <dd>{billions(model.paramsTotalB)} total / {billions(model.paramsActiveB)} active</dd>
          </div>
          <div>
            <dt>{t('studio.model.layers')}</dt>
            <dd>{model.layers}</dd>
          </div>
          {smallest && (
            <div>
              <dt>{t('studio.model.size')}</dt>
              <dd>{gb(smallest.sizeGb)}</dd>
            </div>
          )}
          <div>
            <dt>{t('studio.model.quantizations')}</dt>
            <dd>
              {model.quantizations.map((q) => (
                <Badge key={q.name} tone="neutral" >{q.name}</Badge>
              ))}
            </dd>
          </div>
          {model.isMoe && (
            <div>
              <dt>Architecture</dt>
              <dd><Badge tone="info">MoE</Badge></dd>
            </div>
          )}
        </dl>
      </div>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Step 3 — Deployment preview (split diagram)
// ---------------------------------------------------------------------------

function SplitDiagram({
  plan,
  nodeNames,
  t,
}: {
  plan: DeploymentPlan;
  nodeNames: Record<string, string>;
  t: TFunc;
}) {
  const totalLayers = plan.assignments.reduce((s, a) => Math.max(s, a.layerEnd), 0);
  return (
    <div className="split-map">
      {plan.assignments.map((a) => {
        const pct = totalLayers > 0
          ? ((a.layerEnd - a.layerStart) / totalLayers) * 100
          : 0;
        return (
          <div key={a.nodeId} className="split-map__row">
            <div className="split-map__label">
              <span className="split-map__host">{nodeNames[a.nodeId] ?? a.nodeId}</span>
              <Badge tone={a.role === 'host' ? 'info' : 'neutral'}>
                {a.role === 'host' ? 'Host' : 'Worker'}
              </Badge>
            </div>
            <div className="split-map__bar">
              <div
                className={`split-map__fill split-map__fill--${a.role}`}
                style={{ width: `${pct}%` }}
              >
                {t('studio.preview.layers', { start: a.layerStart, end: a.layerEnd })}
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function PreviewFeasible({
  plan,
  nodeNames,
  t,
}: {
  plan: DeploymentPlan;
  nodeNames: Record<string, string>;
  t: TFunc;
}) {
  const e = plan.estimated;
  return (
    <div className="grid grid--2">
      <Card title={t('studio.preview.fleet')}>
        <SplitDiagram plan={plan} nodeNames={nodeNames} t={t} />
        <p className="muted pipeline-order">
          {t('studio.preview.pipeline')}:{' '}
          {plan.pipelineOrder.map((id) => nodeNames[id] ?? id).join(' → ')}
        </p>
      </Card>
      <Card title={t('studio.preview.throughput')}>
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
  );
}

function PreviewCard({
  result,
  nodeNames,
  t,
}: {
  result: PlanPreviewResult;
  nodeNames: Record<string, string>;
  t: TFunc;
}) {
  if (!result.feasible) {
    return (
      <div className="fit fit--danger">
        <span className="fit__icon" aria-hidden="true">
          <IconWarning />
        </span>
        <div>
          <p className="fit__title">{t('studio.preview.infeasible')}</p>
          <p className="fit__detail">{result.reason}</p>
        </div>
      </div>
    );
  }
  if (!result.plan) return null;
  return <PreviewFeasible plan={result.plan} nodeNames={nodeNames} t={t} />;
}

// ---------------------------------------------------------------------------
// Step 4 — Override & deploy
// ---------------------------------------------------------------------------

type ToastKind = 'deployed' | 'imported';

function Toast({
  kind,
  value,
  onClose,
  t,
}: {
  kind: ToastKind;
  value: string;
  onClose: () => void;
  t: TFunc;
}) {
  const title = kind === 'deployed' ? t('studio.deployed.title') : t('studio.imported.title');
  const body  = kind === 'deployed'
    ? t('studio.deployed.body', { id: value })
    : t('studio.imported.body', { model: value });
  return (
    <div className="fit fit--success" role="status" aria-live="polite">
      <span className="fit__icon" aria-hidden="true">
        <IconCheck />
      </span>
      <div>
        <p className="fit__title">{title}</p>
        <p className="fit__detail">{body}</p>
      </div>
      <Button variant="ghost" size="sm" onClick={onClose}>
        {t('action.close')}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type SourceState =
  | { type: 'huggingface';    data: Partial<HuggingFaceSource> }
  | { type: 'object_storage'; data: Partial<ObjectStorageSource> }
  | { type: 'sagemaker';      data: Partial<SageMakerSource> }
  | { type: 'vertexai';       data: Partial<VertexAISource> }
  | { type: 'azure_ml';       data: Partial<AzureMLSource> }
  | { type: 'catalog' };

function initialSource(type: ImportSourceType): SourceState {
  switch (type) {
    case 'huggingface':    return { type, data: {} };
    case 'object_storage': return { type, data: {} };
    case 'sagemaker':      return { type, data: {} };
    case 'vertexai':       return { type, data: {} };
    case 'azure_ml':       return { type, data: {} };
    case 'catalog':        return { type };
  }
}

/** Convert the partial source state into a complete ImportSource, or null if
 *  required fields are missing. */
function toImportSource(s: SourceState): ImportSource | null {
  switch (s.type) {
    case 'catalog': return null;
    case 'huggingface':
      if (!s.data.repo?.trim()) return null;
      return { type: 'huggingface', repo: s.data.repo, revision: s.data.revision, filenamePattern: s.data.filenamePattern };
    case 'object_storage':
      if (!s.data.uri?.trim() || !s.data.name?.trim()) return null;
      return { type: 'object_storage', uri: s.data.uri, name: s.data.name ?? '', family: s.data.family ?? s.data.name ?? '' };
    case 'sagemaker':
      if (!s.data.modelGroup?.trim()) return null;
      return { type: 'sagemaker', modelGroup: s.data.modelGroup, version: s.data.version };
    case 'vertexai':
      if (!s.data.modelPath?.trim()) return null;
      return { type: 'vertexai', modelPath: s.data.modelPath, version: s.data.version };
    case 'azure_ml':
      if (!s.data.workspace?.trim() || !s.data.modelName?.trim()) return null;
      return { type: 'azure_ml', workspace: s.data.workspace, modelName: s.data.modelName, version: s.data.version };
  }
}

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export function ModelStudioPage() {
  const t = useT();

  // Source selection
  const [activeSource, setActiveSource] = useState<ImportSourceType>('huggingface');
  const [sourceState, setSourceState] = useState<SourceState>(initialSource('huggingface'));

  // Derived model after import/catalog selection
  const [model, setModel] = useState<ModelSpec | null>(null);
  const [sourceTypeForModel, setSourceTypeForModel] = useState<ImportSourceType>('huggingface');

  // Plan preview result
  const [previewResult, setPreviewResult] = useState<PlanPreviewResult | null>(null);

  // Quantization override (only when the model has multiple quantizations)
  const [selectedQuant, setSelectedQuant] = useState<string>('');

  // Toast state
  const [toast, setToast] = useState<{ kind: ToastKind; value: string } | null>(null);

  // Node names for the split diagram
  const nodesQ = useNodes();
  const nodeNames = useMemo(() => {
    const map: Record<string, string> = {};
    (nodesQ.data ?? []).forEach((n) => (map[n.profile.nodeId] = n.profile.hostname));
    return map;
  }, [nodesQ.data]);

  // Mutations
  const importMutation   = useImportModel();
  const previewMutation  = usePreviewModelPlan();
  const deployMutation   = useDeployModel();

  // --- switch source tab ----------------------------------------------------
  function switchSource(type: ImportSourceType) {
    setActiveSource(type);
    setSourceState(initialSource(type));
    setModel(null);
    setPreviewResult(null);
    setToast(null);
  }

  // --- inspect (import from external source) --------------------------------
  function handleInspect() {
    const src = toImportSource(sourceState);
    if (!src) return;
    importMutation.mutate(src, {
      onSuccess: (m) => {
        setModel(m);
        setSourceTypeForModel(activeSource);
        setPreviewResult(null);
        setSelectedQuant(m.quantizations[0]?.name ?? '');
        setToast(null);
      },
    });
  }

  // --- catalog direct select ------------------------------------------------
  function handleCatalogSelect(m: ModelSpec) {
    setModel(m);
    setSourceTypeForModel('catalog');
    setPreviewResult(null);
    setSelectedQuant(m.quantizations[0]?.name ?? '');
    setToast(null);
  }

  // --- preview plan ---------------------------------------------------------
  function handlePreview() {
    if (!model) return;
    previewMutation.mutate(model.modelId, {
      onSuccess: (result) => {
        setPreviewResult(result);
        setToast(null);
      },
    });
  }

  // --- deploy ---------------------------------------------------------------
  function handleDeploy() {
    if (!model) return;
    deployMutation.mutate(
      { modelId: model.modelId },
      {
        onSuccess: (dep) => {
          setToast({ kind: 'deployed', value: dep.id });
        },
      },
    );
  }

  // --- import only ----------------------------------------------------------
  function handleImportOnly() {
    if (!model) return;
    setToast({ kind: 'imported', value: model.family });
  }

  // --- build the source form ------------------------------------------------
  function renderSourceForm() {
    switch (sourceState.type) {
      case 'huggingface':
        return (
          <HuggingFaceForm
            value={sourceState.data}
            onChange={(d) => setSourceState({ type: 'huggingface', data: d })}
            t={t}
          />
        );
      case 'object_storage':
        return (
          <ObjectStorageForm
            value={sourceState.data}
            onChange={(d) => setSourceState({ type: 'object_storage', data: d })}
            t={t}
          />
        );
      case 'sagemaker':
        return (
          <SageMakerForm
            value={sourceState.data}
            onChange={(d) => setSourceState({ type: 'sagemaker', data: d })}
            t={t}
          />
        );
      case 'vertexai':
        return (
          <VertexAIForm
            value={sourceState.data}
            onChange={(d) => setSourceState({ type: 'vertexai', data: d })}
            t={t}
          />
        );
      case 'azure_ml':
        return (
          <AzureMLForm
            value={sourceState.data}
            onChange={(d) => setSourceState({ type: 'azure_ml', data: d })}
            t={t}
          />
        );
      case 'catalog':
        return <CatalogPicker onSelect={handleCatalogSelect} t={t} />;
    }
  }

  const canInspect = activeSource === 'catalog'
    ? false // catalog picker handles its own button
    : toImportSource(sourceState) !== null;

  const quantSelectId = useFieldId('quant-select');

  return (
    <div className="page">
      <PageHeader
        title={t('studio.title')}
        subtitle={t('studio.subtitle')}
        actions={
          <span className="page-header__icon" aria-hidden="true">
            <IconBox />
          </span>
        }
      />

      {/* ── Step 1: Source selector ───────────────────────────────────────── */}
      <Card title={t('studio.source.label')}>
        <Tabs
          tabs={SOURCE_TABS.map((s) => ({ id: s.id, label: t(s.labelKey as Parameters<TFunc>[0]) }))}
          active={activeSource}
          onChange={(id) => switchSource(id as ImportSourceType)}
          ariaLabel={t('studio.source.label')}
        />

        <div className="studio-form">
          {SOURCE_TABS.map((s) => (
            <TabPanel key={s.id} id={s.id}>
              {activeSource === s.id && renderSourceForm()}
            </TabPanel>
          ))}

          {/* Inspect button — only shown for external sources */}
          {activeSource !== 'catalog' && (
            <div className="studio-form__actions">
              <Button
                variant="primary"
                disabled={!canInspect || importMutation.isPending}
                onClick={handleInspect}
              >
                {importMutation.isPending ? (
                  <>
                    <Spinner />
                    {t('common.loading')}
                  </>
                ) : model ? (
                  t('studio.action.reimport')
                ) : (
                  t('studio.action.inspect')
                )}
              </Button>
              {importMutation.isError && (
                <p className="field__hint field__hint--error">
                  {errorMessage(importMutation.error, t, 'error.import')}
                </p>
              )}
            </div>
          )}
        </div>
      </Card>

      {/* ── Step 2: Model info card ───────────────────────────────────────── */}
      {model && (
        <ModelInfoCard model={model} sourceType={sourceTypeForModel} t={t} />
      )}

      {/* ── Step 3: Deployment preview ────────────────────────────────────── */}
      {model && (
        <Card title={t('studio.preview.title')}>
          {!previewResult && !previewMutation.isPending && (
            <div className="studio-preview-cta">
              <Button variant="primary" onClick={handlePreview}>
                {t('studio.preview.compute')}
              </Button>
            </div>
          )}

          {previewMutation.isPending && <LoadingBlock />}

          {previewMutation.isError && (
            <ErrorState
              message={errorMessage(previewMutation.error, t, 'error.studioPreview')}
              onRetry={handlePreview}
            />
          )}

          {previewResult && (
            <>
              <PreviewCard result={previewResult} nodeNames={nodeNames} t={t} />
              <div className="studio-preview-cta">
                <Button variant="ghost" size="sm" onClick={handlePreview} disabled={previewMutation.isPending}>
                  {t('studio.preview.recompute')}
                </Button>
              </div>
            </>
          )}
        </Card>
      )}

      {/* ── Step 4: Override & deploy ─────────────────────────────────────── */}
      {model && previewResult?.feasible && previewResult.plan && (
        <Card title={t('deploy.overrides.title')}>
          <div className="studio-deploy">
            {/* Quantization selector */}
            {model.quantizations.length > 1 && (
              <Field label={t('studio.quant.label')} htmlFor={quantSelectId}>
                <select
                  id={quantSelectId}
                  className="select"
                  value={selectedQuant}
                  onChange={(e) => setSelectedQuant(e.target.value)}
                >
                  {model.quantizations.map((q) => (
                    <option key={q.name} value={q.name}>
                      {q.name} — {gb(q.sizeGb)}
                      {q.requiresFp4 ? ' (FP4)' : ''}
                    </option>
                  ))}
                </select>
              </Field>
            )}

            {/* Deploy / Import-only */}
            <div className="studio-deploy__actions">
              <Button
                variant="primary"
                disabled={deployMutation.isPending}
                onClick={handleDeploy}
              >
                {deployMutation.isPending ? <Spinner /> : null}
                {t('studio.action.deploy')}
              </Button>
              <Button
                variant="secondary"
                disabled={deployMutation.isPending}
                onClick={handleImportOnly}
              >
                {t('studio.action.importOnly')}
              </Button>
            </div>

            {deployMutation.isError && (
              <ErrorState
                message={errorMessage(deployMutation.error, t, 'error.deployFailed')}
              />
            )}
          </div>
        </Card>
      )}

      {/* ── Toast ─────────────────────────────────────────────────────────── */}
      {toast && (
        <Toast
          kind={toast.kind}
          value={toast.value}
          onClose={() => setToast(null)}
          t={t}
        />
      )}
    </div>
  );
}
