import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { AuditPage } from '../pages/AuditPage';
import type { AuditLog } from '../api/types';
import { ApiError } from '../api/http';

// ---------------------------------------------------------------------------
// Module mock — replace the entire hooks/queries module so tests never reach
// the real fetch layer or React Query infrastructure.
// ---------------------------------------------------------------------------

vi.mock('../hooks/queries', () => ({
  useAuditLog: vi.fn(),
}));

// Import AFTER the mock declaration so we get the vi-replaced version.
import { useAuditLog } from '../hooks/queries';

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const MOCK_LOG: AuditLog = {
  feature: 'audit',
  licensee: 'Acme Corp',
  entries: [
    {
      seq: 1,
      actor: 'api',
      action: 'model.created',
      target: 'llama-8b',
      createdAt: '2024-09-01T12:00:00.000Z',
      prevHash: '0'.repeat(64),
      hash: 'abc123',
    },
    {
      seq: 2,
      actor: 'api',
      action: 'apikey.deleted',
      target: 'old-key',
      createdAt: '2024-09-01T13:00:00.000Z',
      prevHash: 'abc123',
      hash: 'def456',
    },
  ],
  chain: { verified: true, length: 2 },
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function renderPage() {
  return render(
    <MemoryRouter>
      <I18nProvider>
        <AuditPage />
      </I18nProvider>
    </MemoryRouter>,
  );
}

function mockQuery(overrides: Partial<ReturnType<typeof useAuditLog>>) {
  vi.mocked(useAuditLog).mockReturnValue({
    data: undefined,
    isLoading: false,
    isError: false,
    error: null,
    isFetching: false,
    refetch: vi.fn(),
    // react-query shape — just the fields AuditPage actually reads
    ...overrides,
  } as ReturnType<typeof useAuditLog>);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('AuditPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders_audit_entries_table', () => {
    mockQuery({ data: MOCK_LOG });
    renderPage();
    expect(screen.getByText('model.created')).toBeInTheDocument();
    expect(screen.getByText('llama-8b')).toBeInTheDocument();
    expect(screen.getByText('apikey.deleted')).toBeInTheDocument();
    expect(screen.getByText('old-key')).toBeInTheDocument();
  });

  it('renders_chain_verified_badge', () => {
    mockQuery({ data: MOCK_LOG });
    renderPage();
    expect(screen.getByText('Chain verified')).toBeInTheDocument();
  });

  it('renders_chain_broken_warning', () => {
    const brokenLog: AuditLog = {
      ...MOCK_LOG,
      chain: {
        verified: false,
        length: 2,
        break: { index: 1, seq: 2, kind: 'hash', msg: 'stored hash mismatch' },
      },
    };
    mockQuery({ data: brokenLog });
    renderPage();
    expect(screen.queryByText('Chain verified')).not.toBeInTheDocument();
    expect(
      screen.getByText('Chain integrity broken at seq 2'),
    ).toBeInTheDocument();
  });

  it('renders_402_empty_state_without_license', () => {
    const err = new ApiError(402, 'Payment Required');
    mockQuery({ isError: true, error: err, data: undefined });
    renderPage();
    expect(
      screen.getByText('Enterprise license required to view audit log'),
    ).toBeInTheDocument();
    expect(screen.getByText('Learn about enterprise audit log')).toBeInTheDocument();
  });

  it('renders_correct_entry_count', () => {
    mockQuery({ data: MOCK_LOG });
    const { container } = renderPage();
    const rows = container.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(2);
  });
});
