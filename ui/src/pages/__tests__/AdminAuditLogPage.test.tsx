/**
 * AdminAuditLogPage — enterprise admin action trail.
 *
 * Covers:
 *  (c) renders rows from a mocked useAuditLog;
 *  (d) the enterprise-gated path (402 license_required) shows the gated empty
 *      state instead of crashing.
 *
 * Idiom mirrors AuditPage.test.tsx: fully mock ../../hooks/queries, render
 * under the real I18nProvider + MemoryRouter, build the 402 with the real
 * ApiError so isLicenseRequired() detection is exercised end-to-end.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../../i18n';
import { ApiError } from '../../api/http';
import type { AuditLog } from '../../api/types';
import type { ReactNode } from 'react';

vi.mock('../../hooks/queries', () => ({
  useAuditLog: vi.fn(),
}));

import { AdminAuditLogPage } from '../AdminAuditLogPage';
import { useAuditLog } from '../../hooks/queries';

const mockUseAuditLog = useAuditLog as unknown as ReturnType<typeof vi.fn>;

const MOCK_LOG: AuditLog = {
  feature: 'audit',
  licensee: 'Acme Corp',
  entries: [
    {
      seq: 2,
      actor: 'admin@acme',
      action: 'apikey.create',
      target: 'key-abc123',
      details: { team: 'eng' },
      prevHash: 'aaa',
      hash: 'bbb',
      createdAt: '2026-09-12T10:00:00Z',
    },
    {
      seq: 1,
      actor: 'admin@acme',
      action: 'login',
      target: 'session',
      prevHash: '000',
      hash: 'aaa',
      createdAt: '2026-09-12T09:00:00Z',
    },
  ],
  chain: { verified: true, length: 2 },
};

function wrapper({ children }: { children: ReactNode }) {
  return (
    <MemoryRouter>
      <I18nProvider>{children}</I18nProvider>
    </MemoryRouter>
  );
}

function renderPage() {
  return render(<AdminAuditLogPage />, { wrapper });
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('AdminAuditLogPage', () => {
  it('(c) renders admin audit entries returned by useAuditLog', () => {
    mockUseAuditLog.mockReturnValue({
      data: MOCK_LOG,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });

    renderPage();

    expect(screen.getByText('apikey.create')).toBeInTheDocument();
    expect(screen.getByText('login')).toBeInTheDocument();
    expect(screen.getAllByText('admin@acme').length).toBeGreaterThanOrEqual(2);
  });

  it('(d) shows the enterprise-gated empty state on a 402, not a crash', () => {
    mockUseAuditLog.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new ApiError(402, 'enterprise license required', {
        error: { type: 'license_required', feature: 'audit' },
      }),
      refetch: vi.fn(),
    });

    renderPage();

    // The gate renders as a role="status" banner mentioning Enterprise; there
    // must be no crash and no audit rows.
    expect(screen.getByRole('status')).toBeInTheDocument();
    expect(screen.getByText(/enterprise feature/i)).toBeInTheDocument();
    expect(screen.queryByText('apikey.create')).not.toBeInTheDocument();
  });
});
