// Route: /config — add to router.tsx (Administration section in the sidebar).
//
// ConfigCodePage — config-as-code (purser.yaml desired state) surface.
//
// Three operations, backed by the control-plane config endpoints:
//   - GET  /api/v1/config/export → the CURRENT cluster config, shown read-only
//     in a dark, line-numbered code panel (same identity as the Rego panel on
//     PoliciesPage).
//   - POST /api/v1/config/diff   → a SAFE dry-run: the operator pastes/uploads a
//     candidate purser.yaml and sees exactly what an apply would change. No
//     mutation.
//   - POST /api/v1/config/apply  → MUTATING and cluster-wide. Gated behind an
//     arm → confirm interaction (a modal that spells out the consequence) so it
//     can never fire from a single stray click.
//
// Visual identity: reuses the developer-tool / dark code-panel aesthetic of
// PoliciesPage and the Card + stat-grid patterns used across the dashboard.
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  ErrorState,
  Field,
  LoadingBlock,
  Modal,
  PageHeader,
  useFieldId,
} from '../components/ui';
import { useT, type TFunc } from '../i18n';
import { errorMessage } from '../lib/errors';
import { useConfigApply, useConfigDiff, useConfigExport } from '../hooks/queries';
import type { ConfigApplyResult, ConfigDiff } from '../api/types';

// ---------------------------------------------------------------------------
// Dark, line-numbered code panel — mirrors PoliciesPage's RegoSourcePanel so
// the two config surfaces read as one design system.
// ---------------------------------------------------------------------------

function CodePanel({ title, code, ariaLabel }: { title: string; code: string; ariaLabel: string }) {
  const lines = code.split('\n');
  return (
    <div style={{ borderRadius: 'var(--radius)', overflow: 'hidden', border: '1px solid #30363d' }}>
      <div
        style={{
          background: '#161b22',
          borderBottom: '1px solid #30363d',
          padding: '0.6rem 1rem',
          display: 'flex',
          alignItems: 'center',
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
          {title}
        </span>
      </div>
      <div
        role="region"
        aria-label={ariaLabel}
        style={{ background: '#0d1117', overflowX: 'auto', maxHeight: '520px', overflowY: 'auto' }}
      >
        <table
          style={{
            borderCollapse: 'collapse',
            width: '100%',
            fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)',
            fontSize: '0.82em',
            lineHeight: '1.6',
          }}
          aria-label={ariaLabel}
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
                    whiteSpace: 'nowrap',
                    borderRight: '1px solid #21262d',
                  }}
                >
                  {idx + 1}
                </td>
                <td style={{ paddingLeft: '1rem', paddingRight: '1rem', color: '#e6edf3', whiteSpace: 'pre' }}>
                  {line || ' '}
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
// Diff result — a structured summary of what an apply would change. Read-only.
// ---------------------------------------------------------------------------

/** Best-effort human label for an opaque model/deployment/quota spec object. */
function itemLabel(x: unknown): string {
  if (typeof x === 'string') return x;
  if (x && typeof x === 'object') {
    const o = x as Record<string, unknown>;
    const id = o.id ?? o.model ?? o.name ?? o.team ?? o.teamId;
    if (typeof id === 'string' && id) return id;
  }
  return JSON.stringify(x);
}

function ChipList({ items }: { items: unknown[] }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px', marginTop: '4px' }}>
      {items.map((it, i) => (
        <code
          key={i}
          className="inline-code"
          style={{
            fontSize: '0.8em',
            background: 'var(--surface-2)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-sm)',
            padding: '2px 8px',
          }}
        >
          {itemLabel(it)}
        </code>
      ))}
    </div>
  );
}

function DiffResultView({ diff, t }: { diff: ConfigDiff; t: TFunc }) {
  const totalChanges =
    diff.modelsToAdd.length +
    diff.modelsToRemove.length +
    diff.deploymentsToAdd.length +
    diff.deploymentsToRemove.length +
    diff.quotasToUpsert.length;

  const groups: { key: string; label: string; items: unknown[]; tone: 'success' | 'danger' | 'info' }[] = [
    { key: 'ma', label: t('configcode.diff.modelsToAdd'), items: diff.modelsToAdd, tone: 'success' },
    { key: 'mr', label: t('configcode.diff.modelsToRemove'), items: diff.modelsToRemove, tone: 'danger' },
    { key: 'da', label: t('configcode.diff.deploymentsToAdd'), items: diff.deploymentsToAdd, tone: 'success' },
    { key: 'dr', label: t('configcode.diff.deploymentsToRemove'), items: diff.deploymentsToRemove, tone: 'danger' },
    { key: 'qu', label: t('configcode.diff.quotasToUpsert'), items: diff.quotasToUpsert, tone: 'info' },
  ];

  return (
    <div data-testid="config-diff-result" style={{ marginTop: '1rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '0.75rem' }}>
        <strong>{t('configcode.diff.title')}</strong>
        <Badge tone={totalChanges === 0 ? 'neutral' : 'info'}>
          {totalChanges === 0
            ? t('configcode.diff.noChanges')
            : t('configcode.diff.changeCount', { count: totalChanges })}
        </Badge>
      </div>
      {totalChanges > 0 && (
        <dl style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', columnGap: '20px', rowGap: '10px', margin: 0 }}>
          {groups
            .filter((g) => g.items.length > 0)
            .map((g) => (
              <div key={g.key} style={{ display: 'contents' }}>
                <dt style={{ alignSelf: 'center' }}>
                  <Badge tone={g.tone}>{g.items.length}</Badge> <span className="muted">{g.label}</span>
                </dt>
                <dd style={{ margin: 0 }}>
                  <ChipList items={g.items} />
                </dd>
              </div>
            ))}
        </dl>
      )}
    </div>
  );
}

function ApplyResultView({ result, t }: { result: ConfigApplyResult; t: TFunc }) {
  const stats: { key: string; label: string; value: number }[] = [
    { key: 'models', label: t('configcode.apply.modelsAdded'), value: result.modelsAdded },
    { key: 'deployments', label: t('configcode.apply.deploymentsAdded'), value: result.deploymentsAdded },
    { key: 'orgs', label: t('configcode.apply.orgsAdded'), value: result.orgsAdded },
    { key: 'pools', label: t('configcode.apply.nodePoolsAdded'), value: result.nodePoolsAdded },
    { key: 'quotas', label: t('configcode.apply.quotasUpserted'), value: result.quotasUpserted },
    { key: 'slos', label: t('configcode.apply.slosUpserted'), value: result.slosUpserted },
  ];
  return (
    <div data-testid="config-apply-result" style={{ marginTop: '1rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '0.75rem' }}>
        <strong>{t('configcode.apply.resultTitle')}</strong>
        <Badge tone="success">{t('configcode.apply.applied')}</Badge>
      </div>
      <div className="stat-grid">
        {stats.map((s) => (
          <div className="stat" key={s.key}>
            <span className="stat__value">{s.value}</span>
            <span className="stat__label">{s.label}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ConfigCodePage() {
  const t = useT();
  const exportQuery = useConfigExport();
  const diff = useConfigDiff();
  const apply = useConfigApply();

  const [editor, setEditor] = useState('');
  const [armApply, setArmApply] = useState(false);
  const editorId = useFieldId('config-editor');

  const canSubmit = editor.trim().length > 0;

  function handleDiff() {
    if (!canSubmit) return;
    diff.mutate(editor);
  }

  function handleLoadCurrent() {
    if (exportQuery.data) setEditor(exportQuery.data);
  }

  function handleUpload(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => setEditor(String(reader.result ?? ''));
    reader.readAsText(file);
    // Reset the input so re-uploading the same file fires change again.
    e.target.value = '';
  }

  function handleConfirmApply() {
    setArmApply(false);
    apply.mutate(editor);
  }

  return (
    <div className="page">
      <PageHeader title={t('configcode.title')} subtitle={t('configcode.subtitle')} />

      {/* Current cluster configuration (read-only) */}
      <Card
        title={t('configcode.current.title')}
        action={
          <Button
            variant="secondary"
            size="sm"
            onClick={handleLoadCurrent}
            disabled={!exportQuery.data}
          >
            {t('configcode.action.loadCurrent')}
          </Button>
        }
      >
        <p className="muted" style={{ marginTop: 0 }}>{t('configcode.current.hint')}</p>
        {exportQuery.isLoading && <LoadingBlock />}
        {exportQuery.isError && (
          <ErrorState
            message={errorMessage(exportQuery.error, t, 'configcode.error.export')}
            onRetry={() => void exportQuery.refetch()}
          />
        )}
        {exportQuery.data !== undefined && !exportQuery.isError && (
          <CodePanel
            title={t('configcode.current.panelTitle')}
            code={exportQuery.data}
            ariaLabel={t('configcode.current.panelTitle')}
          />
        )}
      </Card>

      {/* Check & apply changes */}
      <Card title={t('configcode.check.title')}>
        <p className="muted" style={{ marginTop: 0 }}>{t('configcode.check.hint')}</p>

        <Field label={t('configcode.editor.fieldLabel')} htmlFor={editorId} hint={t('configcode.editor.hint')}>
          <textarea
            id={editorId}
            className="input"
            aria-label={t('configcode.editor.label')}
            value={editor}
            onChange={(e) => setEditor(e.target.value)}
            rows={14}
            spellCheck={false}
            placeholder={'apiVersion: purser/v1\nkind: ClusterConfig\n…'}
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
          />
        </Field>

        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'center' }}>
          <label className="btn btn--secondary btn--sm" style={{ cursor: 'pointer', margin: 0 }}>
            {t('configcode.action.upload')}
            <input
              type="file"
              accept=".yaml,.yml,text/yaml,application/x-yaml"
              onChange={handleUpload}
              style={{ display: 'none' }}
              aria-label={t('configcode.action.upload')}
            />
          </label>
          <Button variant="secondary" size="sm" onClick={handleDiff} disabled={!canSubmit || diff.isPending}>
            {diff.isPending ? '…' : t('configcode.action.diff')}
          </Button>
          <Button
            variant="danger"
            size="sm"
            onClick={() => setArmApply(true)}
            disabled={!canSubmit || apply.isPending}
          >
            {t('configcode.action.apply')}
          </Button>
        </div>

        {diff.isError && (
          <ErrorState
            title={t('configcode.error.diffTitle')}
            message={errorMessage(diff.error, t, 'configcode.error.diff')}
          />
        )}
        {diff.data && <DiffResultView diff={diff.data} t={t} />}

        {apply.isError && (
          <ErrorState
            title={t('configcode.error.applyTitle')}
            message={errorMessage(apply.error, t, 'configcode.error.apply')}
          />
        )}
        {apply.data && <ApplyResultView result={apply.data} t={t} />}
      </Card>

      {/* Arm → confirm modal for the mutating, cluster-wide apply */}
      {armApply && (
        <Modal
          title={t('configcode.confirm.title')}
          onClose={() => setArmApply(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setArmApply(false)}>
                {t('action.cancel')}
              </Button>
              <Button variant="danger" onClick={handleConfirmApply}>
                {t('configcode.confirm.apply')}
              </Button>
            </>
          }
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem', maxWidth: '520px' }}>
            <p style={{ margin: 0 }}>{t('configcode.confirm.body')}</p>
            <p style={{ margin: 0, color: 'var(--danger-fg)', fontWeight: 600 }}>
              {t('configcode.confirm.warning')}
            </p>
            <p className="muted" style={{ margin: 0 }}>{t('configcode.confirm.tip')}</p>
          </div>
        </Modal>
      )}
    </div>
  );
}
