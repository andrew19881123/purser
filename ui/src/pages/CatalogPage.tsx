import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  Modal,
  PageHeader,
  Spinner,
} from '../components/ui';
import {
  IconArrowRight,
  IconCheck,
  IconTrash,
  IconWarning,
} from '../components/icons';
import { useCatalog, useDeleteModel, usePreviewModelPlan } from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { billions, gb, range } from '../lib/format';
import { errorMessage } from '../lib/errors';
import { ApiError } from '../api/http';
import type { CatalogEntry, PlanPreviewResult } from '../api/types';

// ---------------------------------------------------------------------------
// FitBadge — unchanged from original
// ---------------------------------------------------------------------------

function FitBadge({ entry, t }: { entry: CatalogEntry; t: TFunc }) {
  const { fit } = entry;
  if (fit.fits && fit.estimated) {
    const tone = fit.reasonKey === 'fits_tight' ? 'warning' : 'success';
    const tokrange = range(fit.estimated.decodeTokSMin, fit.estimated.decodeTokSMax, 'tok/s');
    return (
      <div className={`fit fit--${tone}`}>
        <span className="fit__icon" aria-hidden="true">
          <IconCheck />
        </span>
        <div>
          <p className="fit__title">
            {fit.reasonKey === 'fits_tight' ? t('catalog.badge.fitsTight') : t('catalog.badge.fits')}
          </p>
          <p className="fit__detail">
            {t('catalog.fits.detail', {
              nodes: fit.nodesNeeded,
              quant: fit.quantization ?? '',
              tokrange,
            })}
          </p>
        </div>
      </div>
    );
  }

  let detail: string;
  if (fit.reasonKey === 'needs_fp4') detail = t('catalog.needFp4');
  else if (fit.reasonKey === 'no_ready_nodes') detail = t('catalog.noNodes');
  else detail = t('catalog.deficit.detail', { gb: gb(fit.deficitGb) });

  return (
    <div className="fit fit--danger">
      <span className="fit__icon" aria-hidden="true">
        <IconWarning />
      </span>
      <div>
        <p className="fit__title">{t('catalog.badge.notFit')}</p>
        <p className="fit__detail">{detail}</p>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// PlanPreviewModal — fleet-split preview dialog
// ---------------------------------------------------------------------------

function PlanPreviewModal({
  modelId,
  result,
  onClose,
  t,
}: {
  modelId: string;
  result: PlanPreviewResult;
  onClose: () => void;
  t: TFunc;
}) {
  return (
    <Modal
      title={t('catalog.preview.title')}
      onClose={onClose}
      footer={
        result.feasible ? (
          <Link
            to={`/deploy/${modelId}`}
            className="btn btn--primary btn--md link-btn"
            onClick={onClose}
          >
            <span>{t('catalog.preview.deploy')}</span>
            <IconArrowRight />
          </Link>
        ) : undefined
      }
    >
      {result.feasible && result.plan ? (
        <div className="plan-preview">
          <ul className="plan-preview__assignments" aria-label="assignments">
            {result.plan.assignments.map((a) => (
              <li key={`${a.nodeId}-${a.layerStart}`} className="plan-preview__assignment">
                <span className="plan-preview__node">{a.nodeId}</span>
                <span className="plan-preview__layers">
                  layers {a.layerStart}–{a.layerEnd}
                </span>
                <Badge tone={a.role === 'host' ? 'success' : 'info'}>
                  {a.role.toUpperCase()}
                </Badge>
              </li>
            ))}
          </ul>
          {result.plan.estimated && (
            <p className="plan-preview__toks">
              ~{range(
                result.plan.estimated.decodeTokSMin,
                result.plan.estimated.decodeTokSMax,
                'tok/s',
              )}{' '}
              decode
            </p>
          )}
          {result.plan.pipelineOrder.length > 0 && (
            <div className="plan-preview__pipeline">
              <p className="plan-preview__pipeline-label">{t('catalog.preview.pipeline')}</p>
              <ol className="plan-preview__pipeline-list">
                {result.plan.pipelineOrder.map((nodeId) => (
                  <li key={nodeId}>{nodeId}</li>
                ))}
              </ol>
            </div>
          )}
        </div>
      ) : (
        <p className="plan-preview__infeasible">
          <IconWarning />
          {t('catalog.preview.infeasible')}
          {result.reason && `: ${result.reason}`}
        </p>
      )}
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// ModelCard — per-model entry with delete + preview-split actions
// ---------------------------------------------------------------------------

interface ModelCardProps {
  entry: CatalogEntry;
  t: TFunc;
  isDeleting: boolean;
  deleteError: string | null;
  onDelete: (modelId: string) => void;
  isPreviewPending: boolean;
  onPreview: (modelId: string) => void;
}

function ModelCard({
  entry,
  t,
  isDeleting,
  deleteError,
  onDelete,
  isPreviewPending,
  onPreview,
}: ModelCardProps) {
  const { model, fit } = entry;
  return (
    <Card className="model-card">
      <div className="model-card__head">
        <div>
          <h3 className="model-card__name">{model.family}</h3>
          <p className="model-card__id">{model.modelId}</p>
        </div>
        <div className="model-card__tags">
          {model.isMoe && <Badge tone="info">{t('catalog.moe')}</Badge>}
          {model.draft.available && <Badge tone="info">{t('catalog.draft')}</Badge>}
        </div>
      </div>

      <dl className="spec-list">
        <div>
          <dt title="Total model parameters — higher = more capable but slower">Params</dt>
          <dd>{t('catalog.params', { active: billions(model.paramsActiveB), total: billions(model.paramsTotalB) })}</dd>
        </div>
        <div>
          <dt title="Transformer layers — determines how the model is split across nodes">Layers</dt>
          <dd>{model.layers}</dd>
        </div>
        <div>
          <dt title="Maximum conversation length in tokens">Context</dt>
          <dd>{(model.contextMax / 1024).toFixed(0)}K</dd>
        </div>
        <div>
          <dt title="Quantization — Q4 is smallest/fastest, Q8 is most accurate">Quant</dt>
          <dd>{model.quantizations.map((q) => q.name).join(', ')}</dd>
        </div>
      </dl>

      <FitBadge entry={entry} t={t} />

      <div className="model-card__actions">
        <Link
          to={`/deploy/${model.modelId}`}
          className={`btn btn--primary btn--md link-btn${fit.fits ? '' : ' btn--muted'}`}
          aria-disabled={fit.fits ? undefined : true}
        >
          <span>{fit.fits ? t('catalog.action.deploy') : t('catalog.action.plan')}</span>
          <IconArrowRight />
        </Link>

        {fit.fits && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onPreview(model.modelId)}
            disabled={isPreviewPending}
            aria-label={t('catalog.action.previewSplit')}
          >
            {isPreviewPending ? <Spinner /> : t('catalog.action.previewSplit')}
          </Button>
        )}

        <Button
          variant="danger"
          size="sm"
          onClick={() => onDelete(model.modelId)}
          disabled={isDeleting}
          aria-label={t('catalog.action.delete')}
        >
          {isDeleting ? <Spinner /> : <IconTrash />}
        </Button>
      </div>

      {deleteError && (
        <p role="alert" className="model-card__delete-error">
          {deleteError}
        </p>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// CatalogPage
// ---------------------------------------------------------------------------

export function CatalogPage() {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useCatalog();
  const deleteModel = useDeleteModel();
  const previewMutation = usePreviewModelPlan();

  // Track which model triggered a delete error (to show inline)
  const [deleteErrorModel, setDeleteErrorModel] = useState<{ modelId: string; msg: string } | null>(null);
  // Track which model's preview modal is open
  const [previewModelId, setPreviewModelId] = useState<string | null>(null);

  function handleDelete(modelId: string) {
    const confirmed = window.confirm(t('catalog.deleteConfirm', { model: modelId }));
    if (!confirmed) return;
    setDeleteErrorModel(null);
    deleteModel.mutate(modelId, {
      onError: (err) => {
        const msg =
          err instanceof ApiError && err.status === 409
            ? t('catalog.deleteError.inUse')
            : errorMessage(err, t, 'error.catalog');
        setDeleteErrorModel({ modelId, msg });
      },
    });
  }

  function handlePreview(modelId: string) {
    setPreviewModelId(modelId);
    previewMutation.mutate(modelId);
  }

  function closePreview() {
    setPreviewModelId(null);
    previewMutation.reset();
  }

  return (
    <div className="page">
      <PageHeader title={t('catalog.title')} subtitle={t('catalog.subtitle')} />
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState message={errorMessage(error, t, 'error.catalog')} onRetry={() => refetch()} />
      )}
      {data && data.length === 0 && (
        <EmptyState
          message="No models in catalog yet. Import one from Model Studio."
          action={<Link to="/model-studio">Go to Model Studio</Link>}
        />
      )}
      {data && data.length > 0 && (
        <div className="grid grid--cards">
          {data.map((entry) => (
            <ModelCard
              key={entry.model.modelId}
              entry={entry}
              t={t}
              isDeleting={deleteModel.isPending && !deleteErrorModel}
              deleteError={
                deleteErrorModel?.modelId === entry.model.modelId
                  ? deleteErrorModel.msg
                  : null
              }
              onDelete={handleDelete}
              isPreviewPending={
                previewMutation.isPending && previewModelId === entry.model.modelId
              }
              onPreview={handlePreview}
            />
          ))}
        </div>
      )}

      {previewModelId && previewMutation.isSuccess && (
        <PlanPreviewModal
          modelId={previewModelId}
          result={previewMutation.data}
          onClose={closePreview}
          t={t}
        />
      )}

      {previewModelId && previewMutation.isError && (
        <Modal title={t('catalog.preview.title')} onClose={closePreview}>
          <ErrorState
            message={errorMessage(previewMutation.error, t, 'error.studioPreview')}
          />
        </Modal>
      )}
    </div>
  );
}
