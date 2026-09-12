/**
 * CompliancePage — AI Act / GDPR compliance surface.
 *
 * Covers:
 *  (a) the page renders and the two regulatory-export buttons call the right
 *      API client methods (getAiActTechnicalDoc / getGdprRecordOfProcessing);
 *  (b) the GDPR erasure form submits the entered subject identifier via the
 *      erasure mutation and renders a confirmation;
 *  (c) the erasure-log read-only view renders its empty state gracefully.
 *
 * Idiom mirrors AuditPage/ChargebackPage tests: mock ../../hooks/queries and
 * ../../api/client, render under the real I18nProvider + MemoryRouter.
 */
import { describe, it, expect, vi, beforeEach, beforeAll } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../../i18n';
import type { ReactNode } from 'react';

// --- api client: the compliance downloads call these directly ----------------
vi.mock('../../api/client', () => ({
  api: {
    getAiActTechnicalDoc: vi.fn(async () => '{"system_name":"Purser AI Inference Gateway"}'),
    getGdprRecordOfProcessing: vi.fn(async () => '{"controller":"Acme Corp"}'),
  },
}));

// --- hooks: erasure mutation + erasure-log query -----------------------------
const { erasureMutate } = vi.hoisted(() => ({ erasureMutate: vi.fn() }));

vi.mock('../../hooks/queries', () => ({
  useGdprErasure: vi.fn(),
  useGdprErasureLog: vi.fn(),
}));

import { CompliancePage } from '../CompliancePage';
import { api } from '../../api/client';
import { useGdprErasure, useGdprErasureLog } from '../../hooks/queries';

const mockErasure = useGdprErasure as unknown as ReturnType<typeof vi.fn>;
const mockErasureLog = useGdprErasureLog as unknown as ReturnType<typeof vi.fn>;

function wrapper({ children }: { children: ReactNode }) {
  return (
    <MemoryRouter>
      <I18nProvider>{children}</I18nProvider>
    </MemoryRouter>
  );
}

function renderPage() {
  return render(<CompliancePage />, { wrapper });
}

beforeAll(() => {
  // jsdom implements neither URL.createObjectURL nor the anchor navigation the
  // blob download relies on; stub both so the download handler cannot throw.
  (URL as unknown as { createObjectURL: () => string }).createObjectURL = vi.fn(() => 'blob:mock');
  (URL as unknown as { revokeObjectURL: () => void }).revokeObjectURL = vi.fn();
  HTMLAnchorElement.prototype.click = vi.fn();
});

beforeEach(() => {
  vi.clearAllMocks();
  mockErasure.mockReturnValue({
    mutate: erasureMutate,
    isPending: false,
    isError: false,
    error: null,
  });
  mockErasureLog.mockReturnValue({
    data: [],
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  });
});

describe('CompliancePage', () => {
  it('(a) renders and the two export buttons call the right client methods', async () => {
    renderPage();

    expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /AI Act/i }));
    await waitFor(() => expect(api.getAiActTechnicalDoc).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole('button', { name: /record of processing/i }));
    await waitFor(() => expect(api.getGdprRecordOfProcessing).toHaveBeenCalledTimes(1));
  });

  it('(b) erasure form submits the entered subject id and shows a confirmation', () => {
    const result = {
      erasedEvents: 3,
      erasureType: 'inference_audit',
      completedAt: '2026-09-12T00:00:00Z',
      subjectPrefix: 'a1b2c3d4...',
    };
    // Make mutate invoke its onSuccess so the confirmation renders in one step.
    mockErasure.mockReturnValue({
      mutate: (vars: unknown, opts?: { onSuccess?: (r: typeof result) => void }) => {
        erasureMutate(vars);
        opts?.onSuccess?.(result);
      },
      isPending: false,
      isError: false,
      error: null,
    });

    renderPage();

    fireEvent.change(screen.getByLabelText(/subject identifier/i), {
      target: { value: 'a1b2c3d4e5f6' },
    });
    fireEvent.click(screen.getByRole('button', { name: /erase records/i }));

    expect(erasureMutate).toHaveBeenCalledTimes(1);
    expect(erasureMutate).toHaveBeenCalledWith(
      expect.objectContaining({ subjectType: 'api_key', subjectIdentifier: 'a1b2c3d4e5f6' }),
    );

    const confirmation = screen.getByTestId('erasure-confirmation');
    expect(confirmation).toBeInTheDocument();
    expect(within(confirmation).getByText(/3/)).toBeInTheDocument();
  });

  it('(c) renders the erasure-log empty state without crashing', () => {
    renderPage();
    expect(screen.getByText(/no erasure operations/i)).toBeInTheDocument();
  });
});
