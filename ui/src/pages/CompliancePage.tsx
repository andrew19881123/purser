// CompliancePage — AI Act + GDPR compliance surface for enterprise buyers.
//
// Three cards, top to bottom:
//   ┌─ Regulatory documentation ────────────────────────────────────────────┐
//   │  On-demand exports an auditor can consume: AI Act Art.11/Annex-IV      │
//   │  technical documentation, and the GDPR Art.30 record of processing.    │
//   │  Each row states what the document is FOR, then a Download button that │
//   │  fetches the live document and saves it as JSON.                       │
//   ├─ Right to erasure (GDPR Art.17) ───────────────────────────────────────┤
//   │  Subject identifier + reason → POST /gdpr/erasure. On success a        │
//   │  confirmation states how many records were pseudonymised.             │
//   ├─ Erasure log ──────────────────────────────────────────────────────────┤
//   │  Read-only trail of past erasures (empty until the backend ships it).  │
//   └────────────────────────────────────────────────────────────────────────┘
//
// Every compliance endpoint is enterprise-gated (402 license_required); each
// section detects that and shows the shared "locked feature" prompt rather than
// a generic error — the same pattern AuditPage/PoliciesPage use.
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  LoadingBlock,
  PageHeader,
  useFieldId,
} from '../components/ui';
import { useT } from '../i18n';
import { api } from '../api/client';
import { ApiError } from '../api/http';
import { useGdprErasure, useGdprErasureLog } from '../hooks/queries';
import type { GdprErasureResult } from '../api/types';

// ---------------------------------------------------------------------------
// Enterprise license gate detection (same idiom as AuditPage / PoliciesPage)
// ---------------------------------------------------------------------------

function isLicenseRequired(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  const body = error.body as Record<string, unknown> | null | undefined;
  if (!body || typeof body !== 'object') return false;
  const errField = body.error as Record<string, unknown> | null | undefined;
  if (!errField || typeof errField !== 'object') return false;
  return errField.type === 'license_required';
}

function EnterpriseGate({ desc }: { desc?: string }) {
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
          {t('compliance.enterprise.title')}
        </strong>
      </div>
      <p style={{ margin: 0, color: 'var(--text)', fontSize: '0.9em', lineHeight: 1.5 }}>
        {desc ?? t('compliance.enterprise.desc')}
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
        {t('compliance.enterprise.link')}
      </a>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Download helper — save raw response text as a JSON file.
// ---------------------------------------------------------------------------

function downloadJson(text: string, filename: string) {
  const blob = new Blob([text], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

const today = () => new Date().toISOString().slice(0, 10);

// ---------------------------------------------------------------------------
// Regulatory documentation exports
// ---------------------------------------------------------------------------

type ExportKind = 'aiAct' | 'gdpr';

function ExportsCard() {
  const t = useT();
  const [busy, setBusy] = useState<ExportKind | null>(null);
  const [error, setError] = useState<unknown>(null);

  async function run(kind: ExportKind) {
    setBusy(kind);
    setError(null);
    try {
      if (kind === 'aiAct') {
        const text = await api.getAiActTechnicalDoc();
        downloadJson(text, `purser-ai-act-technical-doc-${today()}.json`);
      } else {
        const text = await api.getGdprRecordOfProcessing();
        downloadJson(text, `purser-gdpr-record-of-processing-${today()}.json`);
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card title={t('compliance.exports.title')}>
      <p className="muted" style={{ margin: '0 0 1rem', fontSize: '0.9em', lineHeight: 1.5 }}>
        {t('compliance.exports.desc')}
      </p>

      {isLicenseRequired(error) && <EnterpriseGate />}
      {error != null && !isLicenseRequired(error) && (
        <div style={{ marginBottom: '1rem' }}>
          <ErrorState message={t('compliance.error.export')} onRetry={() => setError(null)} />
        </div>
      )}

      <ExportRow
        title={t('compliance.aiAct.title')}
        desc={t('compliance.aiAct.desc')}
        buttonLabel={t('compliance.aiAct.download')}
        busy={busy === 'aiAct'}
        onDownload={() => void run('aiAct')}
      />
      <ExportRow
        title={t('compliance.gdpr.title')}
        desc={t('compliance.gdpr.desc')}
        buttonLabel={t('compliance.gdpr.download')}
        busy={busy === 'gdpr'}
        onDownload={() => void run('gdpr')}
      />
    </Card>
  );
}

function ExportRow({
  title,
  desc,
  buttonLabel,
  busy,
  onDownload,
}: {
  title: string;
  desc: string;
  buttonLabel: string;
  busy: boolean;
  onDownload: () => void;
}) {
  const t = useT();
  return (
    <div
      style={{
        display: 'flex',
        gap: '1rem',
        alignItems: 'flex-start',
        justifyContent: 'space-between',
        padding: '0.85rem 0',
        borderTop: '1px solid var(--border)',
      }}
    >
      <div style={{ maxWidth: '42rem' }}>
        <h3 style={{ margin: '0 0 0.25rem', fontSize: '0.95em', fontWeight: 600 }}>{title}</h3>
        <p className="muted" style={{ margin: 0, fontSize: '0.85em', lineHeight: 1.5 }}>
          {desc}
        </p>
      </div>
      <Button
        variant="primary"
        size="sm"
        onClick={onDownload}
        disabled={busy}
        aria-label={buttonLabel}
        style={{ whiteSpace: 'nowrap' }}
      >
        {busy ? t('compliance.export.downloading') : t('compliance.export.download')}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// GDPR Art.17 right-to-erasure
// ---------------------------------------------------------------------------

function ErasureCard() {
  const t = useT();
  const subjectId = useFieldId('erasure-subject');
  const reasonId = useFieldId('erasure-reason');
  const [subject, setSubject] = useState('');
  const [reason, setReason] = useState('');
  const [result, setResult] = useState<GdprErasureResult | null>(null);

  const erasure = useGdprErasure();

  function submit() {
    const id = subject.trim();
    if (!id) return;
    setResult(null);
    erasure.mutate(
      { subjectType: 'api_key', subjectIdentifier: id, reason: reason.trim() },
      { onSuccess: (res) => setResult(res) },
    );
  }

  return (
    <Card title={t('compliance.erasure.title')}>
      <p className="muted" style={{ margin: '0 0 1rem', fontSize: '0.9em', lineHeight: 1.5 }}>
        {t('compliance.erasure.desc')}
      </p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
        style={{ display: 'flex', flexDirection: 'column', gap: '1rem', maxWidth: '44rem' }}
      >
        <Field
          label={t('compliance.erasure.subject.label')}
          htmlFor={subjectId}
          hint={t('compliance.erasure.subject.hint')}
        >
          <input
            id={subjectId}
            type="text"
            className="input"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            placeholder="e3b0c44298fc1c149afbf4c8996fb924…"
            spellCheck={false}
            aria-required="true"
            style={{ fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)' }}
          />
        </Field>

        <Field
          label={t('compliance.erasure.reason.label')}
          htmlFor={reasonId}
          hint={t('compliance.erasure.reason.hint')}
        >
          <input
            id={reasonId}
            type="text"
            className="input"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t('compliance.erasure.reason.placeholder')}
          />
        </Field>

        <div>
          <Button
            type="submit"
            variant="danger"
            size="sm"
            disabled={!subject.trim() || erasure.isPending}
          >
            {erasure.isPending ? t('compliance.erasure.submitting') : t('compliance.erasure.submit')}
          </Button>
        </div>
      </form>

      {result && (
        <div
          data-testid="erasure-confirmation"
          role="status"
          style={{
            marginTop: '1rem',
            display: 'flex',
            flexDirection: 'column',
            gap: '6px',
            background: 'var(--success-bg, var(--info-bg))',
            border: '1px solid color-mix(in srgb, var(--success-fg, var(--info-fg)) 30%, transparent)',
            borderRadius: 'var(--radius)',
            padding: '14px 18px',
          }}
        >
          <strong style={{ fontSize: '0.95em' }}>{t('compliance.erasure.confirm.title')}</strong>
          <p style={{ margin: 0, fontSize: '0.88em', lineHeight: 1.5 }}>
            {t('compliance.erasure.confirm.body', {
              count: String(result.erasedEvents),
              subject: result.subjectPrefix,
              time: new Date(result.completedAt).toLocaleString(),
            })}
          </p>
        </div>
      )}

      {erasure.isError && isLicenseRequired(erasure.error) && (
        <div style={{ marginTop: '1rem' }}>
          <EnterpriseGate />
        </div>
      )}
      {erasure.isError && !isLicenseRequired(erasure.error) && (
        <p style={{ marginTop: '1rem', color: 'var(--danger-fg)', fontSize: '0.875em' }} role="alert">
          {erasure.error instanceof ApiError && erasure.error.status === 403
            ? t('compliance.erasure.forbidden')
            : t('compliance.error.erasure')}
        </p>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Erasure log (read-only)
// ---------------------------------------------------------------------------

function ErasureLogCard() {
  const t = useT();
  const { data, isLoading, isError, error, refetch } = useGdprErasureLog();
  const entries = data ?? [];

  return (
    <Card title={t('compliance.erasureLog.title')}>
      <p className="muted" style={{ margin: '0 0 1rem', fontSize: '0.9em', lineHeight: 1.5 }}>
        {t('compliance.erasureLog.desc')}
      </p>

      {isLoading && <LoadingBlock />}

      {isError && isLicenseRequired(error) && <EnterpriseGate />}
      {isError && !isLicenseRequired(error) && (
        <ErrorState message={t('compliance.error.erasureLog')} onRetry={() => void refetch()} />
      )}

      {!isLoading && !isError && entries.length === 0 && (
        <EmptyState message={t('compliance.erasureLog.empty')} />
      )}

      {entries.length > 0 && (
        <div className="table-wrap" style={{ overflowX: 'auto' }}>
          <table className="table">
            <thead>
              <tr>
                <th scope="col">{t('compliance.erasureLog.col.subject')}</th>
                <th scope="col">{t('compliance.erasureLog.col.erasedBy')}</th>
                <th scope="col">{t('compliance.erasureLog.col.reason')}</th>
                <th scope="col">{t('compliance.erasureLog.col.events')}</th>
                <th scope="col">{t('compliance.erasureLog.col.type')}</th>
                <th scope="col">{t('compliance.erasureLog.col.time')}</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((row) => (
                <tr key={row.id || row.subjectHash}>
                  <td>
                    <code className="inline-code" style={{ fontSize: '0.82em' }}>
                      {row.subjectHash.slice(0, 12)}…
                    </code>
                  </td>
                  <td className="muted">{row.erasedBy}</td>
                  <td className="muted" style={{ maxWidth: '20rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {row.reason || <span style={{ opacity: 0.4 }}>—</span>}
                  </td>
                  <td>
                    <Badge tone="neutral">{row.eventsErased}</Badge>
                  </td>
                  <td className="muted" style={{ fontSize: '0.82em' }}>{row.erasureType}</td>
                  <td className="muted" style={{ fontSize: '0.8em', whiteSpace: 'nowrap' }}>
                    {row.erasedAt ? new Date(row.erasedAt).toLocaleString() : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

export function CompliancePage() {
  const t = useT();
  return (
    <div className="page">
      <PageHeader title={t('compliance.title')} subtitle={t('compliance.subtitle')} />
      <ExportsCard />
      <ErasureCard />
      <ErasureLogCard />
    </div>
  );
}
