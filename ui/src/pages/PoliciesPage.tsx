// Route: /platform/policies — add to router.tsx
//
// PoliciesPage — OPA/Rego policy management.
//
// Visual identity: code-centric, developer-tool aesthetic. The Rego source
// panel is the signature element — dark background with line numbers, looking
// like a GitHub-dark code view. Enterprise-gated: shows upgrade prompt when
// the policy_engine feature is not licensed.
//
// Layout:
//   ┌─ PageHeader ──────────────────────────────────────────────────┐
//   │  "Policies" / "OPA/Rego enforcement rules"    [Upload policy] │
//   └───────────────────────────────────────────────────────────────┘
//   ┌─ Policy table ────────────────────────────────────────────────┐
//   │  Name | Status | Description | Created | Actions             │
//   └───────────────────────────────────────────────────────────────┘
//   ┌─ Source panel (when a policy is selected) ────────────────────┐
//   │  Title bar + line-numbered Rego code (dark background)        │
//   └───────────────────────────────────────────────────────────────┘
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  LoadingBlock,
  Modal,
  PageHeader,
  useFieldId,
} from '../components/ui';
import { useT } from '../i18n';
import { errorMessage } from '../lib/errors';
import { ApiError } from '../api/http';
import { usePolicies, useUpsertPolicy, useDeletePolicy } from '../hooks/queries';
import type { Policy } from '../api/types';

// ---------------------------------------------------------------------------
// Enterprise license gate detection (reused from AuditPage pattern)
// ---------------------------------------------------------------------------

function isLicenseRequired(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  const body = error.body as Record<string, unknown> | null | undefined;
  if (!body || typeof body !== 'object') return false;
  const errField = body.error as Record<string, unknown> | null | undefined;
  if (!errField || typeof errField !== 'object') return false;
  return errField.type === 'license_required';
}

function EnterpriseGate() {
  const t = useT();
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'flex-start',
        gap: '10px',
        background: 'var(--info-bg)',
        border: '1px solid color-mix(in srgb, var(--info-fg) 25%, transparent)',
        borderRadius: 'var(--radius)',
        padding: '20px 24px',
      }}
      role="status"
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
        <span aria-hidden="true" style={{ fontSize: '1.25em', lineHeight: 1, color: 'var(--info-fg)' }}>
          🔒
        </span>
        <strong style={{ color: 'var(--info-fg)', fontSize: '1em', fontWeight: 600 }}>
          {t('policies.enterprise.title')}
        </strong>
      </div>
      <p style={{ margin: 0, color: 'var(--text)', fontSize: '0.9em', lineHeight: 1.5 }}>
        {t('policies.enterprise.desc')}
      </p>
      <a
        href="https://andrew19881123.github.io/purser/enterprise/licensing/"
        target="_blank"
        rel="noreferrer"
        style={{
          color: 'var(--info-fg)',
          fontWeight: 600,
          fontSize: '0.875em',
          textDecoration: 'none',
          borderBottom: '1px solid color-mix(in srgb, var(--info-fg) 40%, transparent)',
          paddingBottom: '1px',
        }}
      >
        {t('policies.enterprise.link')}
      </a>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Rego source panel — the signature element of this page.
// Dark editor background, monospace font, line numbers in muted color.
// ---------------------------------------------------------------------------

function RegoSourcePanel({ policy, onClose }: { policy: Policy; onClose: () => void }) {
  const t = useT();
  const lines = policy.source.split('\n');

  return (
    <div
      style={{
        marginTop: '1rem',
        borderRadius: 'var(--radius)',
        overflow: 'hidden',
        border: '1px solid #30363d',
      }}
    >
      {/* Panel header — mimics a terminal/editor title bar */}
      <div
        style={{
          background: '#161b22',
          borderBottom: '1px solid #30363d',
          padding: '0.6rem 1rem',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: '0.75rem',
        }}
      >
        <span
          style={{
            fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)',
            fontSize: '0.8em',
            color: '#8b949e',
            letterSpacing: '0.02em',
          }}
        >
          {t('policies.source.title', { name: policy.name })}
        </span>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close source panel">
          ✕
        </Button>
      </div>

      {/* Line-numbered code body */}
      <div
        role="region"
        aria-label={t('policies.source.title', { name: policy.name })}
        style={{
          background: '#0d1117',
          overflowX: 'auto',
          maxHeight: '480px',
          overflowY: 'auto',
        }}
      >
        <table
          style={{
            borderCollapse: 'collapse',
            width: '100%',
            fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)',
            fontSize: '0.82em',
            lineHeight: '1.6',
          }}
          aria-label="Rego source"
        >
          <tbody>
            {lines.map((line, idx) => (
              <tr key={idx}>
                <td
                  aria-hidden="true"
                  style={{
                    paddingLeft: '1rem',
                    paddingRight: '1.2rem',
                    textAlign: 'right',
                    color: '#586069',
                    userSelect: 'none',
                    minWidth: '3.5rem',
                    verticalAlign: 'top',
                    paddingTop: '0',
                    paddingBottom: '0',
                    whiteSpace: 'nowrap',
                    borderRight: '1px solid #21262d',
                  }}
                >
                  {idx + 1}
                </td>
                <td
                  style={{
                    paddingLeft: '1rem',
                    paddingRight: '1rem',
                    color: '#e6edf3',
                    whiteSpace: 'pre',
                    paddingTop: '0',
                    paddingBottom: '0',
                  }}
                >
                  {line || ' '}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Upload policy modal
// ---------------------------------------------------------------------------

const EXAMPLE_REGO = `package purser

# Allow only approved models
default allow = false

allow if {
    input.action == "deploy"
    input.model_id == "qwen3-235b"
}`.trim();

function UploadPolicyModal({ onClose }: { onClose: () => void }) {
  const t = useT();
  const { mutate: upsert, isPending, isError, error } = useUpsertPolicy();
  const nameId = useFieldId('policy-name');
  const sourceId = useFieldId('policy-source');

  const [name, setName] = useState('');
  const [source, setSource] = useState(EXAMPLE_REGO);
  const [nameError, setNameError] = useState('');

  function handleSubmit() {
    if (!name.trim()) {
      setNameError(t('policies.modal.name.label') + ' is required.');
      return;
    }
    setNameError('');
    upsert(
      { name: name.trim(), rego: source },
      { onSuccess: () => onClose() },
    );
  }

  return (
    <Modal
      title={t('policies.modal.title')}
      onClose={onClose}
      footer={
        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <Button variant="secondary" size="sm" onClick={onClose} disabled={isPending}>
            {t('policies.modal.cancel')}
          </Button>
          <Button variant="primary" size="sm" onClick={handleSubmit} disabled={isPending || !name.trim()}>
            {isPending ? '…' : t('policies.modal.submit')}
          </Button>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem', minWidth: '520px' }}>
        <Field label={t('policies.modal.name.label')} htmlFor={nameId} hint={t('policies.modal.name.hint')}>
          <input
            id={nameId}
            type="text"
            className="input"
            value={name}
            onChange={(e) => { setName(e.target.value); setNameError(''); }}
            placeholder="rate-limit-tier1"
            aria-required="true"
            aria-invalid={nameError ? 'true' : undefined}
          />
          {nameError && (
            <p style={{ margin: '0.25rem 0 0', color: 'var(--danger-fg)', fontSize: '0.85em' }}>
              {nameError}
            </p>
          )}
        </Field>

        <Field label={t('policies.modal.source.label')} htmlFor={sourceId}>
          {/* Textarea styled like a code editor */}
          <textarea
            id={sourceId}
            className="input"
            value={source}
            onChange={(e) => setSource(e.target.value)}
            rows={14}
            spellCheck={false}
            style={{
              fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)',
              fontSize: '0.82em',
              lineHeight: '1.6',
              background: '#0d1117',
              color: '#e6edf3',
              border: '1px solid #30363d',
              borderRadius: 'var(--radius)',
              padding: '0.75rem 1rem',
              resize: 'vertical',
            }}
            aria-label={t('policies.modal.source.label')}
          />
        </Field>

        {isError && !isLicenseRequired(error) && (
          <p style={{ margin: 0, color: 'var(--danger-fg)', fontSize: '0.875em' }}>
            {error instanceof Error ? error.message : t('error.policies.upsert')}
          </p>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Policy row
// ---------------------------------------------------------------------------

function PolicyRow({
  policy,
  isViewingSource,
  onViewSource,
  onToggle,
  onDelete,
}: {
  policy: Policy;
  isViewingSource: boolean;
  onViewSource: () => void;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const t = useT();

  return (
    <tr>
      <td>
        <code
          className="inline-code"
          style={{ fontSize: '0.875em', fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)' }}
        >
          {policy.name}
        </code>
      </td>
      <td>
        <Badge tone={policy.enabled ? 'success' : 'neutral'}>
          {policy.enabled ? t('policies.status.enabled') : t('policies.status.disabled')}
        </Badge>
      </td>
      <td className="muted" style={{ fontSize: '0.875em', maxWidth: '20rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {policy.description || <span style={{ opacity: 0.4 }}>—</span>}
      </td>
      <td className="muted" style={{ fontSize: '0.8em', whiteSpace: 'nowrap' }}>
        {new Date(policy.createdAt).toLocaleDateString()}
      </td>
      <td>
        <div style={{ display: 'flex', gap: '0.35rem', flexWrap: 'nowrap' }}>
          <Button
            variant={isViewingSource ? 'primary' : 'ghost'}
            size="sm"
            onClick={onViewSource}
            aria-pressed={isViewingSource}
          >
            {t('policies.action.viewSource')}
          </Button>
          <Button variant="ghost" size="sm" onClick={onToggle}>
            {policy.enabled ? t('policies.action.disable') : t('policies.action.enable')}
          </Button>
          <Button variant="danger" size="sm" onClick={onDelete}>
            {t('policies.action.delete')}
          </Button>
        </div>
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

export function PoliciesPage() {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = usePolicies();
  const { mutate: upsert } = useUpsertPolicy();
  const { mutate: deletePolicy } = useDeletePolicy();

  const [showUpload, setShowUpload] = useState(false);
  const [viewingSourceName, setViewingSourceName] = useState<string | null>(null);

  const policies = data?.policies ?? [];
  const viewingPolicy = policies.find((p) => p.name === viewingSourceName) ?? null;

  function handleToggle(policy: Policy) {
    upsert({ name: policy.name, rego: policy.source, enabled: !policy.enabled });
  }

  function handleDelete(policy: Policy) {
    if (window.confirm(`Delete policy "${policy.name}"?`)) {
      deletePolicy(policy.name);
      if (viewingSourceName === policy.name) setViewingSourceName(null);
    }
  }

  function handleViewSource(name: string) {
    setViewingSourceName((prev) => (prev === name ? null : name));
  }

  return (
    <div className="page">
      <PageHeader
        title={t('policies.title')}
        subtitle={t('policies.subtitle')}
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowUpload(true)}>
            {t('policies.upload')}
          </Button>
        }
      />

      {showUpload && <UploadPolicyModal onClose={() => setShowUpload(false)} />}

      <Card>
        {isLoading && <LoadingBlock />}

        {isError && isLicenseRequired(error) && <EnterpriseGate />}

        {isError && !isLicenseRequired(error) && (
          <ErrorState
            message={errorMessage(error, t, 'error.policies')}
            onRetry={() => void refetch()}
          />
        )}

        {!isLoading && !isError && policies.length === 0 && (
          <EmptyState
            title={t('policies.empty.title')}
            message={t('policies.empty.msg')}
            action={
              <a
                href="https://andrew19881123.github.io/purser/enterprise/policy-as-code/"
                target="_blank"
                rel="noreferrer"
                style={{ color: 'var(--accent)', fontWeight: 600, fontSize: '0.875em', textDecoration: 'none' }}
              >
                {t('policies.empty.docsLink')}
              </a>
            }
          />
        )}

        {policies.length > 0 && (
          <div className="table-wrap" style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('policies.col.name')}</th>
                  <th scope="col">{t('policies.col.status')}</th>
                  <th scope="col">{t('policies.col.description')}</th>
                  <th scope="col">{t('policies.col.created')}</th>
                  <th scope="col" />
                </tr>
              </thead>
              <tbody>
                {policies.map((p) => (
                  <PolicyRow
                    key={p.name}
                    policy={p}
                    isViewingSource={viewingSourceName === p.name}
                    onViewSource={() => handleViewSource(p.name)}
                    onToggle={() => handleToggle(p)}
                    onDelete={() => handleDelete(p)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Inline source panel below the table */}
        {viewingPolicy && (
          <RegoSourcePanel
            policy={viewingPolicy}
            onClose={() => setViewingSourceName(null)}
          />
        )}
      </Card>
    </div>
  );
}
