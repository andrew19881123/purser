import { useState } from 'react';
import {
  Button,
  Card,
  CopyButton,
  ErrorState,
  LoadingBlock,
  PageHeader,
} from '../components/ui';
import { IconRefresh } from '../components/icons';
import { useJoinInfo, useRotateToken } from '../hooks/queries';
import { useT } from '../i18n';
import { config } from '../api/config';
import { relativeTime } from '../lib/format';
import { errorMessage } from '../lib/errors';

// TTL choices for the enrollment bundle. Seconds are forwarded verbatim to
// GET /api/v1/enrollment-bundle?ttl_seconds=<value>.
interface TtlOption {
  labelKey: 'join.ttl.1h' | 'join.ttl.24h' | 'join.ttl.7d' | 'join.ttl.30d';
  seconds: number;
}

const TTL_OPTIONS: TtlOption[] = [
  { labelKey: 'join.ttl.1h', seconds: 3600 },
  { labelKey: 'join.ttl.24h', seconds: 86400 },
  { labelKey: 'join.ttl.7d', seconds: 604800 },
  { labelKey: 'join.ttl.30d', seconds: 2592000 },
];

type BundleStatus = 'idle' | 'loading' | 'error';

export function JoinTokenPage() {
  const t = useT();
  const { data: join, isLoading, isError, error, refetch } = useJoinInfo();
  const rotate = useRotateToken();
  const [ttl, setTtl] = useState<number>(86400);
  const [bundleStatus, setBundleStatus] = useState<BundleStatus>('idle');

  const downloadBundle = async () => {
    setBundleStatus('loading');
    try {
      const res = await fetch(
        `${config.apiBase}/enrollment-bundle?ttl_seconds=${ttl}`,
        { credentials: 'same-origin' },
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const text = await res.text();
      const blob = new Blob([text], { type: 'text/plain' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'purser-enrollment.env';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      setBundleStatus('idle');
    } catch {
      setBundleStatus('error');
    }
  };

  const expired = join ? new Date(join.expiresAt).getTime() < Date.now() : false;

  return (
    <div className="page">
      <PageHeader title={t('join.title')} subtitle={t('join.subtitle')} />

      {/* --- Join token card ------------------------------------------------ */}
      <Card
        title={t('join.token.label')}
        action={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => rotate.mutate()}
            disabled={rotate.isPending}
          >
            <IconRefresh />
            <span>{t('onboarding.token.rotate')}</span>
          </Button>
        }
      >
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState
            message={errorMessage(error, t, 'error.join')}
            onRetry={() => refetch()}
          />
        )}
        {join && (
          <>
            <div className="token-row">
              <code className="token" aria-label={t('join.token.label')}>
                {join.joinToken}
              </code>
              <CopyButton value={join.joinToken} />
            </div>
            <p className={`token-expiry${expired ? ' token-expiry--danger' : ''}`}>
              {expired
                ? t('onboarding.token.expired')
                : t('onboarding.token.expires', { when: relativeTime(join.expiresAt) })}
            </p>
          </>
        )}
      </Card>

      {/* --- Enrollment bundle card ----------------------------------------- */}
      <Card title={t('join.bundle.title')}>
        <p className="prose">{t('join.bundle.desc')}</p>

        <div className="bundle-row">
          <label className="field__label" htmlFor="bundle-ttl">
            {t('join.bundle.ttl')}
          </label>
          <select
            id="bundle-ttl"
            className="select"
            value={ttl}
            onChange={(e) => setTtl(Number(e.target.value))}
            disabled={bundleStatus === 'loading'}
          >
            {TTL_OPTIONS.map((o) => (
              <option key={o.seconds} value={o.seconds}>
                {t(o.labelKey)}
              </option>
            ))}
          </select>

          <div
            className="bundle-btn-wrap"
            title={t('join.bundle.tooltip')}
            aria-label={t('join.bundle.tooltip')}
          >
            <Button
              variant="primary"
              onClick={() => void downloadBundle()}
              disabled={bundleStatus === 'loading'}
            >
              {bundleStatus === 'loading'
                ? t('join.bundle.downloading')
                : t('join.bundle.download')}
            </Button>
          </div>
        </div>

        {bundleStatus === 'error' && (
          <ErrorState
            message={t('join.bundle.error')}
            onRetry={() => setBundleStatus('idle')}
          />
        )}
      </Card>
    </div>
  );
}
