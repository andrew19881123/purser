import { Link } from 'react-router-dom';
import {
  Badge,
  Card,
  ErrorState,
  LoadingBlock,
  PageHeader,
} from '../components/ui';
import { IconArrowRight, IconCheck, IconWarning } from '../components/icons';
import { useCatalog } from '../hooks/queries';
import { useT, type TFunc } from '../i18n';
import { billions, gb, range } from '../lib/format';
import { errorMessage } from '../lib/errors';
import type { CatalogEntry } from '../api/types';

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

function ModelCard({ entry, t }: { entry: CatalogEntry; t: TFunc }) {
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
          <dt>Params</dt>
          <dd>{t('catalog.params', { active: billions(model.paramsActiveB), total: billions(model.paramsTotalB) })}</dd>
        </div>
        <div>
          <dt>Layers</dt>
          <dd>{model.layers}</dd>
        </div>
        <div>
          <dt>Context</dt>
          <dd>{(model.contextMax / 1024).toFixed(0)}K</dd>
        </div>
        <div>
          <dt>Quant</dt>
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
      </div>
    </Card>
  );
}

export function CatalogPage() {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useCatalog();

  return (
    <div className="page">
      <PageHeader title={t('catalog.title')} subtitle={t('catalog.subtitle')} />
      {isLoading && <LoadingBlock />}
      {isError && (
        <ErrorState message={errorMessage(error, t, 'error.catalog')} onRetry={() => refetch()} />
      )}
      {data && (
        <div className="grid grid--cards">
          {data.map((entry) => (
            <ModelCard key={entry.model.modelId} entry={entry} t={t} />
          ))}
        </div>
      )}
    </div>
  );
}
