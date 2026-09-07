// AuditPage tests — chain integrity panel, inference audit tab, access log tab.
import { render, screen, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { AuditPage } from './AuditPage';
import type { ChainVerifyResponse, InferenceAuditResponse, AccessLogResponse } from '../api/types';

// ---------------------------------------------------------------------------
// Mock the entire hooks/queries module so no React Query infrastructure is
// needed and no real network calls are made.
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
// Fixtures
// ---------------------------------------------------------------------------

const MOCK_CHAIN_OK: ChainVerifyResponse = {
  verified: true,
  blockCount: 1420,
  lastVerifiedAt: '2026-09-08T14:00:00Z',
  brokenAtSeq: null,
};

const MOCK_CHAIN_BROKEN: ChainVerifyResponse = {
  verified: false,
  blockCount: 567,
  lastVerifiedAt: '2026-09-08T14:00:00Z',
  brokenAtSeq: 568,
};

const MOCK_AUDIT: InferenceAuditResponse = {
  total: 2,
  events: [
    {
      seq: 1,
      modelId: 'llama3-8b',
      modelRevision: 'main',
      modelQuantization: 'Q4_K_M',
      tenant: 'acme',
      apiKeyId: 'key-abc123',
      nodeId: 'node-1',
      inferenceEngine: 'llamacpp',
      inputTokens: 512,
      outputTokens: 128,
      latencyMs: 1240,
      status: 'ok',
      createdAt: '2026-09-08T14:23:07Z',
      hash: 'abc123',
      prevHash: '000000',
    },
    {
      seq: 2,
      modelId: 'qwen3-235b',
      modelRevision: 'main',
      modelQuantization: 'Q8_0',
      tenant: 'beta',
      apiKeyId: 'key-def456',
      nodeId: 'node-2',
      inferenceEngine: 'llamacpp',
      inputTokens: 1024,
      outputTokens: 256,
      latencyMs: 3800,
      status: 'error',
      createdAt: '2026-09-08T15:00:00Z',
      hash: 'def456',
      prevHash: 'abc123',
    },
  ],
};

const MOCK_ACCESS_LOG: AccessLogResponse = {
  count: 1,
  entries: [
    {
      id: 4812,
      apiKeyId: 'key-abc123',
      method: 'POST',
      path: '/v1/chat/completions',
      ipPrefix: '10.0.1.0/24',
      userAgent: 'python-httpx/0.27.2',
      statusCode: 200,
      requestAt: '2026-09-08T14:23:07Z',
    },
  ],
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

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function qr(overrides: Record<string, unknown> = {}): any {
  return { data: undefined, isLoading: false, isError: false, error: null, isFetching: false, refetch: vi.fn(), ...overrides };
}

function mockChain(overrides: Record<string, unknown> = {}) {
  vi.mocked(useAuditChainVerify).mockReturnValue(qr({ data: MOCK_CHAIN_OK, ...overrides }));
}

function mockInference(overrides: Record<string, unknown> = {}) {
  vi.mocked(useInferenceAudit).mockReturnValue(qr({ data: MOCK_AUDIT, ...overrides }));
}

function mockAccess(overrides: Record<string, unknown> = {}) {
  vi.mocked(useAccessLog).mockReturnValue(qr({ data: MOCK_ACCESS_LOG, ...overrides }));
}

function mockAll() {
  mockChain();
  mockInference();
  mockAccess();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('AuditPage — Chain Integrity panel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAll();
  });

  it('shows verified badge when chain is intact', () => {
    renderPage();
    expect(screen.getByText('Chain verified')).toBeInTheDocument();
  });

  it('shows broken badge with sequence number when chain broken', () => {
    vi.mocked(useAuditChainVerify).mockReturnValue(qr({ data: MOCK_CHAIN_BROKEN }));
    renderPage();
    expect(screen.queryByText('Chain verified')).not.toBeInTheDocument();
    expect(screen.getByText('Chain broken at seq 568')).toBeInTheDocument();
  });

  it('calls /verify endpoint on mount', () => {
    renderPage();
    expect(useAuditChainVerify).toHaveBeenCalled();
  });

  it('shows "Verify Now" button that triggers a fresh call', () => {
    const refetch = vi.fn();
    vi.mocked(useAuditChainVerify).mockReturnValue(qr({ data: MOCK_CHAIN_OK, refetch }));
    renderPage();
    const verifyBtn = screen.getByRole('button', { name: /verify now/i });
    fireEvent.click(verifyBtn);
    expect(refetch).toHaveBeenCalled();
  });
});

describe('AuditPage — Inference Audit tab', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAll();
  });

  it('renders event table with correct columns', () => {
    renderPage();
    // Column headers are rendered as <th> elements; use role=columnheader to be specific.
    expect(screen.getByRole('columnheader', { name: 'Model' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Tenant' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'API Key' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Latency' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Tokens (in/out)' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Status' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Time' })).toBeInTheDocument();
  });

  it('shows model filter dropdown', () => {
    renderPage();
    const modelSelect = screen.getByRole('combobox', { name: /model/i });
    expect(modelSelect).toBeInTheDocument();
  });

  it('renders pagination controls', () => {
    renderPage();
    expect(screen.getByRole('button', { name: /prev/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /next/i })).toBeInTheDocument();
  });

  it('CSV export button exists', () => {
    renderPage();
    expect(screen.getByRole('button', { name: /export csv/i })).toBeInTheDocument();
  });
});

describe('AuditPage — Access Log tab', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAll();
  });

  it('renders access log table', () => {
    renderPage();
    // Switch to Access Log tab
    fireEvent.click(screen.getByRole('tab', { name: /access log/i }));
    // The access log table should be visible
    expect(screen.getByText('Method')).toBeInTheDocument();
    expect(screen.getByText('Path')).toBeInTheDocument();
    expect(screen.getByText('IP Prefix')).toBeInTheDocument();
    expect(screen.getByText('User Agent')).toBeInTheDocument();
  });

  it('shows api_key_id filter', () => {
    renderPage();
    fireEvent.click(screen.getByRole('tab', { name: /access log/i }));
    const filterInput = screen.getByRole('textbox', { name: /filter by api key/i });
    expect(filterInput).toBeInTheDocument();
  });
});
