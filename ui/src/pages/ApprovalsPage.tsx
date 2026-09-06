// ApprovalsPage — deployment approval queue (AI Act Art.14 human oversight).
//
// Enterprise-gated: requires the "deployment_approvals" feature. Without it
// the control plane returns 402 and this page shows a clear "Enterprise
// license required" empty state.
//
// Workflow:
//   1. A deployer triggers POST /api/v1/models/{id}/deploy.
//   2. The control plane queues an approval record (status = "pending")
//      instead of launching the rollout.
//   3. An admin comes here, reviews the request, and Approves or Rejects.
//   4. On Approve the deployment proceeds; on Reject it is cancelled.
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
  type Tone,
} from '../components/ui';
import { IconRefresh, IconShield } from '../components/icons';
import { useApprovals, useApproveDeployment, useRejectDeployment } from '../hooks/queries';
import { useT } from '../i18n';
import type { StringKey } from '../i18n/en';
import { ApiError } from '../api/http';
import { errorMessage } from '../lib/errors';
import type { DeploymentApproval } from '../api/types';

// ---------------------------------------------------------------------------
// Status badge
// ---------------------------------------------------------------------------

function statusTone(status: DeploymentApproval['status']): Tone {
  switch (status) {
    case 'pending':
      return 'warning';
    case 'approved':
      return 'success';
    case 'rejected':
      return 'neutral';
  }
}

function StatusBadge({ status }: { status: DeploymentApproval['status'] }) {
  const t = useT();
  const label =
    status === 'approved'
      ? t('approvals.status.approved')
      : status === 'rejected'
        ? t('approvals.status.rejected')
        : t('approvals.status.pending');
  return <Badge tone={statusTone(status)}>{label}</Badge>;
}

// ---------------------------------------------------------------------------
// Approve / Reject dialog (inline)
// ---------------------------------------------------------------------------

interface ActionDialogProps {
  approval: DeploymentApproval;
  action: 'approve' | 'reject';
  onClose: () => void;
}

function ActionDialog({ approval, action, onClose }: ActionDialogProps) {
  const t = useT();
  const [notes, setNotes] = useState('');
  const approveMut = useApproveDeployment();
  const rejectMut = useRejectDeployment();

  const mut = action === 'approve' ? approveMut : rejectMut;
  const confirmKey = action === 'approve' ? 'approvals.confirm.approve' : 'approvals.confirm.reject';

  function handleSubmit() {
    const fn = action === 'approve' ? approveMut.mutateAsync : rejectMut.mutateAsync;
    void fn({ deploymentId: approval.deploymentId, notes }).then(onClose);
  }

  return (
    <div
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 100,
      }}
      onClick={onClose}
    >
      <div
        style={{
          background: 'var(--color-surface)', border: '1px solid var(--color-border)',
          borderRadius: 8, padding: '1.5rem', minWidth: 360, maxWidth: 480,
        }}
        onClick={(e) => e.stopPropagation()}
      >
        <p style={{ marginBottom: '1rem', fontWeight: 600 }}>
          {t(confirmKey, { model: approval.modelId })}
        </p>
        <label style={{ display: 'block', marginBottom: '0.5rem', fontSize: '0.875rem' }}>
          {t('approvals.notes.label')}
        </label>
        <textarea
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          rows={3}
          style={{
            width: '100%', padding: '0.5rem',
            border: '1px solid var(--color-border)', borderRadius: 4,
            fontSize: '0.875rem', background: 'var(--color-surface)',
            color: 'inherit', resize: 'vertical', boxSizing: 'border-box',
          }}
        />
        <div style={{ display: 'flex', gap: '0.5rem', marginTop: '1rem', justifyContent: 'flex-end' }}>
          <Button variant="secondary" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant={action === 'approve' ? 'primary' : 'danger'}
            size="sm"
            onClick={handleSubmit}
            disabled={mut.isPending}
          >
            {action === 'approve' ? t('approvals.action.approve') : t('approvals.action.reject')}
          </Button>
        </div>
        {mut.isError && (
          <p style={{ color: 'var(--color-danger)', fontSize: '0.8em', marginTop: '0.5rem' }}>
            {mut.error instanceof Error ? mut.error.message : 'Error'}
          </p>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Table row
// ---------------------------------------------------------------------------

function ApprovalRow({ approval }: { approval: DeploymentApproval }) {
  const t = useT();
  const [dialog, setDialog] = useState<'approve' | 'reject' | null>(null);

  const ts = new Date(approval.requestedAt).toLocaleString(undefined, {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  });
  const shortHash = (h: string) => (h.length > 12 ? h.slice(0, 12) + '…' : h);

  return (
    <>
      <tr>
        <td>
          <code className="inline-code" style={{ fontSize: '0.85em' }}>{approval.modelId}</code>
        </td>
        <td>
          <code className="inline-code" style={{ fontSize: '0.78em' }}>{shortHash(approval.requester)}</code>
        </td>
        <td style={{ fontSize: '0.85em', whiteSpace: 'nowrap' }}>{ts}</td>
        <td><StatusBadge status={approval.status} /></td>
        <td>
          {approval.reviewer && (
            <code className="inline-code" style={{ fontSize: '0.78em' }}>{shortHash(approval.reviewer)}</code>
          )}
        </td>
        <td style={{ fontSize: '0.8em', color: 'var(--color-text-muted)' }}>{approval.notes ?? ''}</td>
        <td>
          {approval.status === 'pending' && (
            <div style={{ display: 'flex', gap: '0.4rem' }}>
              <Button variant="primary" size="sm" onClick={() => setDialog('approve')}>
                {t('approvals.action.approve')}
              </Button>
              <Button variant="danger" size="sm" onClick={() => setDialog('reject')}>
                {t('approvals.action.reject')}
              </Button>
            </div>
          )}
        </td>
      </tr>
      {dialog && (
        <ActionDialog
          approval={approval}
          action={dialog}
          onClose={() => setDialog(null)}
        />
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Filter tabs
// ---------------------------------------------------------------------------

type StatusFilter = '' | 'pending' | 'approved' | 'rejected';

const FILTERS: { value: StatusFilter; labelKey: StringKey }[] = [
  { value: '', labelKey: 'approvals.filter.all' },
  { value: 'pending', labelKey: 'approvals.filter.pending' },
  { value: 'approved', labelKey: 'approvals.filter.approved' },
  { value: 'rejected', labelKey: 'approvals.filter.rejected' },
];

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ApprovalsPage() {
  const t = useT();
  const [filter, setFilter] = useState<StatusFilter>('');

  const { data, isLoading, isError, error, refetch, isFetching } = useApprovals(filter || undefined);

  // 402 = no enterprise license
  const is402 = isError && error instanceof ApiError && error.status === 402;

  const pageActions = (
    <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
      {FILTERS.map(({ value, labelKey }) => (
        <Button
          key={value}
          variant={filter === value ? 'primary' : 'secondary'}
          size="sm"
          onClick={() => setFilter(value)}
        >
          {t(labelKey)}
        </Button>
      ))}
      <Button
        variant="secondary"
        size="sm"
        onClick={() => void refetch()}
        disabled={isFetching}
        aria-label={t('approvals.refresh')}
        title={t('approvals.refresh')}
      >
        <IconRefresh />
      </Button>
    </div>
  );

  return (
    <div className="page">
      <PageHeader title={t('approvals.title')} subtitle={t('approvals.subtitle')} actions={pageActions} />

      <Card title={t('approvals.title')}>
        {isLoading && <LoadingBlock />}

        {is402 && (
          <EmptyState
            icon={<IconShield />}
            message={t('approvals.enterprise.required')}
            action={
              <a
                href="https://purser.dev/docs/enterprise/deployment-approvals"
                target="_blank"
                rel="noopener noreferrer"
                className="btn btn--secondary btn--sm"
              >
                {t('approvals.enterprise.docs')}
              </a>
            }
          />
        )}

        {isError && !is402 && (
          <ErrorState
            message={errorMessage(error, t, 'error.approvals')}
            onRetry={() => void refetch()}
          />
        )}

        {data && data.length === 0 && (
          <EmptyState icon={<IconShield />} message={t('approvals.empty')} />
        )}

        {data && data.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t('approvals.col.model')}</th>
                  <th scope="col">{t('approvals.col.requester')}</th>
                  <th scope="col">{t('approvals.col.requestedAt')}</th>
                  <th scope="col">{t('approvals.col.status')}</th>
                  <th scope="col">{t('approvals.col.reviewer')}</th>
                  <th scope="col">{t('approvals.col.notes')}</th>
                  <th scope="col">{t('approvals.col.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {data.map((approval) => (
                  <ApprovalRow key={approval.deploymentId} approval={approval} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
