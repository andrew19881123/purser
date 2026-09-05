import { useState } from 'react';
import {
  Button,
  Card,
  CodeBlock,
  CopyButton,
  ErrorState,
  Field,
  PageHeader,
  useFieldId,
} from '../components/ui';
import { useCreateJoinToken } from '../hooks/queries';
import { useT } from '../i18n';
import { timeUntil } from '../lib/format';
import { errorMessage } from '../lib/errors';
import { config } from '../api/config';
import type { JoinTokenResult } from '../api/types';

// ---------------------------------------------------------------------------
// Infer the control-plane address that the agent should connect to.
// When the API base is an absolute URL (e.g. set via PURSER_API_BASE_URL),
// extract its origin; otherwise fall back to the current window origin so
// the UI works from any same-origin deployment without extra configuration.
// ---------------------------------------------------------------------------
function inferControlPlaneAddr(): string {
  const base = config.apiBase;
  if (base.startsWith('http')) {
    try {
      return new URL(base).origin;
    } catch {
      // fall through to window.location
    }
  }
  return typeof window !== 'undefined' ? window.location.origin : '';
}

function buildEnvBlock(result: JoinTokenResult): string {
  const addr = inferControlPlaneAddr();
  return [
    `PURSER_CONTROL_PLANE_ADDR=${addr}`,
    `PURSER_JOIN_TOKEN=${result.token}`,
    `PURSER_CLUSTER_ID=${result.clusterId}`,
  ].join('\n');
}

const INSTALL_SNIPPET = `\
# Debian / Ubuntu — replace VERSION with your release (e.g. 0.2.0)
sudo apt install ./purser-agent_VERSION_amd64.deb

# Populate the agent configuration with the values above
sudo tee /etc/purser/agent.env > /dev/null <<'EOF'
PURSER_CONTROL_PLANE_ADDR=<paste from above>
PURSER_JOIN_TOKEN=<paste from above>
PURSER_CLUSTER_ID=<paste from above>
EOF

# Enable and start the agent service
sudo systemctl enable --now purser-agent`;

export function JoinTokenPage() {
  const t = useT();
  const createToken = useCreateJoinToken();
  const [ttl, setTtl] = useState(86400); // 24 h default
  const [result, setResult] = useState<JoinTokenResult | null>(null);
  const ttlId = useFieldId('ttl');

  const ttlOptions = [
    { value: 3600, label: t('jointoken.ttl.1h') },
    { value: 28800, label: t('jointoken.ttl.8h') },
    { value: 86400, label: t('jointoken.ttl.24h') },
    { value: 604800, label: t('jointoken.ttl.7d') },
  ];

  const generate = () => {
    createToken.mutate(ttl, {
      onSuccess: (data) => {
        setResult(data);
        // Scroll the result card into view on small viewports.
        window.setTimeout(
          () => document.getElementById('join-token-result')?.scrollIntoView({ behavior: 'smooth' }),
          50,
        );
      },
    });
  };

  const envBlock = result ? buildEnvBlock(result) : '';

  return (
    <div className="page">
      <PageHeader title={t('jointoken.title')} subtitle={t('jointoken.subtitle')} />

      <Card title={t('jointoken.form.title')}>
        <Field label={t('jointoken.ttl.label')} htmlFor={ttlId}>
          <select
            id={ttlId}
            className="select"
            value={ttl}
            onChange={(e) => setTtl(Number(e.target.value))}
          >
            {ttlOptions.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
        </Field>
        <div style={{ marginTop: 'var(--space-4)' }}>
          <Button variant="primary" onClick={generate} disabled={createToken.isPending}>
            {t('jointoken.action.generate')}
          </Button>
        </div>
        {createToken.isError && (
          <div style={{ marginTop: 'var(--space-3)' }}>
            <ErrorState message={errorMessage(createToken.error, t, 'error.jointoken')} />
          </div>
        )}
      </Card>

      {result && (
        <div id="join-token-result">
        <Card title={t('jointoken.result.title')}>
          <div className="notice notice--warning" role="alert">
            {t('jointoken.result.warning')}
          </div>

          <p className="field__label" style={{ marginTop: 'var(--space-4)' }}>
            {t('jointoken.result.token')}
          </p>
          <div className="token-row">
            <code className="token" aria-label={t('jointoken.result.token')}>
              {result.token}
            </code>
            <CopyButton value={result.token} />
          </div>
          <p className="token-expiry">
            {t('jointoken.result.expires', { when: timeUntil(result.expiresAt) })}
          </p>

          <p className="field__label" style={{ marginTop: 'var(--space-4)' }}>
            {t('jointoken.result.env.label')}
          </p>
          <p className="muted" style={{ marginBottom: 'var(--space-2)' }}>
            {t('jointoken.result.env.hint')}
          </p>
          <CodeBlock code={envBlock} ariaLabel={t('jointoken.result.env.label')} />

          <details style={{ marginTop: 'var(--space-4)' }}>
            <summary
              style={{ cursor: 'pointer', userSelect: 'none', color: 'var(--accent)', fontWeight: 500 }}
            >
              {t('jointoken.result.instructions.title')}
            </summary>
            <div style={{ marginTop: 'var(--space-3)' }}>
              <CodeBlock
                code={INSTALL_SNIPPET}
                ariaLabel={t('jointoken.result.instructions.title')}
              />
            </div>
          </details>
        </Card>
        </div>
      )}
    </div>
  );
}
