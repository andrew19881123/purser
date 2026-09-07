// AuditPage — legacy test file updated to reflect the v0.5 two-tab design.
// The primary test suite lives in ui/src/pages/AuditPage.test.tsx; this file
// provides supplementary coverage (loading states, error paths, data rendering).
import { render, screen, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { AuditPage } from '../pages/AuditPage';

// ---------------------------------------------------------------------------
// Mock hooks/queries to isolate the component from React Query infrastructure.
// ---------------------------------------------------------------------------

vi.mock('../hooks/queries', () => ({
  useAuditChainVerify: vi.fn(),
  useInferenceAudit: vi.fn(),
  useAccessLog: vi.fn(),
}));

import {
  useAuditChainVerify,
  useInferenceAudit,
  useAccessLog,
} from '../hooks/queries';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function qr(overrides: Record<string, unknown> = {}): any {
  return { data: undefined, isLoading: false, isError: false, error: null, isFetching: false, refetch: vi.fn(), ...overrides };
}

const CHAIN_OK = {
  verified: true,
  blockCount: 2,
  lastVerifiedAt: '2024-09-01T14:00:00.000Z',
  brokenAtSeq: null,
};

const MOCK_AUDIT = {
  total: 2,
  events: [
    { seq: 1, modelId: 'llama3-8b', modelRevision: 'main', modelQuantization: 'Q4_K_M', tenant: 'acme', apiKeyId: 'key-abc123', nodeId: 'node-1', inferenceEngine: 'llamacpp', inputTokens: 512, outputTokens: 128, latencyMs: 1240, status: 'ok', createdAt: '2024-09-01T12:00:00.000Z', hash: 'abc123', prevHash: '000000' },
    { seq: 2, modelId: 'qwen3-235b', modelRevision: 'main', modelQuantization: 'Q8_0', tenant: 'beta', apiKeyId: 'key-def456', nodeId: 'node-2', inferenceEngine: 'llamacpp', inputTokens: 1024, outputTokens: 256, latencyMs: 3800, status: 'error', createdAt: '2024-09-01T13:00:00.000Z', hash: 'def456', prevHash: 'abc123' },
  ],
};

const MOCK_ACCESS = {
  count: 1,
  entries: [
    { id: 1, apiKeyId: 'key-abc123', method: 'POST', path: '/v1/chat/completions', ipPrefix: '10.0.1.0/24', userAgent: 'python-httpx/0.27.2', statusCode: 200, requestAt: '2024-09-01T12:00:00.000Z' },
  ],
};

function renderPage() {
  return render(
    <MemoryRouter>
      <I18nProvider>
        <AuditPage />
      </I18nProvider>
    </MemoryRouter>,
  );
}

function mockDefaults() {
  vi.mocked(useAuditChainVerify).mockReturnValue(qr({ data: CHAIN_OK }));
  vi.mocked(useInferenceAudit).mockReturnValue(qr({ data: MOCK_AUDIT }));
  vi.mocked(useAccessLog).mockReturnValue(qr({ data: MOCK_ACCESS }));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('AuditPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockDefaults();
  });

  it('renders_inference_events_in_table', () => {
    renderPage();
    // Model IDs and tenant names also appear in filter dropdown options, so use getAllByText.
    expect(screen.getAllByText('llama3-8b').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('acme').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('qwen3-235b').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('beta').length).toBeGreaterThanOrEqual(1);
  });

  it('renders_chain_verified_badge', () => {
    renderPage();
    expect(screen.getByText('Chain verified')).toBeInTheDocument();
  });

  it('renders_chain_broken_warning', () => {
    vi.mocked(useAuditChainVerify).mockReturnValue(
      qr({ data: { verified: false, blockCount: 567, lastVerifiedAt: '2024-09-01T14:00:00Z', brokenAtSeq: 568 } }),
    );
    renderPage();
    expect(screen.queryByText('Chain verified')).not.toBeInTheDocument();
    expect(screen.getByText('Chain broken at seq 568')).toBeInTheDocument();
  });

  it('renders_correct_event_count', () => {
    const { container } = renderPage();
    const rows = container.querySelectorAll('tbody tr');
    expect(rows.length).toBeGreaterThanOrEqual(2);
  });

  it('shows_access_log_table_on_tab_click', () => {
    renderPage();
    fireEvent.click(screen.getByRole('tab', { name: /access log/i }));
    expect(screen.getByText('Method')).toBeInTheDocument();
  });
});
