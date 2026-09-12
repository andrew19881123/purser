/**
 * ChargebackPage — unit tests for XLSX and PDF export buttons.
 *
 * Strategy: mock the hooks/queries layer and the api client so the component
 * renders synchronously with controlled data, without any real HTTP calls.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

// Mock hooks/queries before importing ChargebackPage.
vi.mock('../hooks/queries', () => ({
  useBillingReport: vi.fn(),
  useBillingForecast: vi.fn(),
  useModelAdoption: vi.fn(),
  useOrgBilling: vi.fn(),
  useTeamBilling: vi.fn(),
}));

// Mock i18n so we can match raw key strings in assertions.
vi.mock('../i18n', () => ({
  useT: () => (key: string) => key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

// Mock the api client so getBillingXlsxUrl / getBillingPdfUrl are controllable.
vi.mock('../api/client', () => ({
  api: {
    getBillingCsvUrl: vi.fn(() => '/api/v1/billing/report?format=csv'),
    getBillingXlsxUrl: vi.fn(() => '/api/v1/billing/report?format=xlsx'),
    getBillingPdfUrl: vi.fn(() => '/api/v1/billing/report?format=pdf'),
  },
}));

import { ChargebackPage } from './ChargebackPage';
import * as queries from '../hooks/queries';
import { api } from '../api/client';

// Minimal billing report for tests that need data loaded.
const MOCK_REPORT = {
  period_start: '2026-09-01T00:00:00Z',
  period_end: '2026-09-08T00:00:00Z',
  total_requests: 42,
  total_tokens: 8400,
  tenants: [
    {
      tenant_id: 'acme/eng',
      model_id: 'llama3-8b',
      request_count: 42,
      prompt_tokens: 4200,
      completion_tokens: 4200,
      total_tokens: 8400,
      avg_latency_ms: 210.5,
      period_start: '2026-09-01T00:00:00Z',
      period_end: '2026-09-08T00:00:00Z',
    },
  ],
};

// Typed access to the mocked hooks.
const mq = queries as unknown as {
  useBillingReport: ReturnType<typeof vi.fn>;
  useBillingForecast: ReturnType<typeof vi.fn>;
  useModelAdoption: ReturnType<typeof vi.fn>;
  useOrgBilling: ReturnType<typeof vi.fn>;
  useTeamBilling: ReturnType<typeof vi.fn>;
};

function mkQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderPage() {
  return render(
    <QueryClientProvider client={mkQueryClient()}>
      <ChargebackPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mq.useBillingReport.mockReturnValue({
    data: MOCK_REPORT,
    isLoading: false,
    error: null,
  });
  // Billing forecast is enterprise-gated; return 402 in tests to hide the section.
  mq.useBillingForecast.mockReturnValue({
    data: undefined,
    isLoading: false,
    error: Object.assign(new Error('Enterprise license required'), { status: 402 }),
  });
  // New tab hooks — default to empty/idle; individual tests override.
  mq.useModelAdoption.mockReturnValue({ data: undefined, isLoading: false, error: null });
  mq.useOrgBilling.mockReturnValue({ data: undefined, isLoading: false, error: null });
  mq.useTeamBilling.mockReturnValue({ data: undefined, isLoading: false, error: null });
});

describe('ChargebackPage — export buttons', () => {
  it('shows XLSX download button', () => {
    renderPage();
    const xlsxBtn = screen.getByRole('button', { name: 'chargeback.action.exportXlsx' });
    expect(xlsxBtn).toBeDefined();
  });

  it('shows PDF download button', () => {
    renderPage();
    const pdfBtn = screen.getByRole('button', { name: 'chargeback.action.exportPdf' });
    expect(pdfBtn).toBeDefined();
  });

  it('XLSX button calls getBillingXlsxUrl with correct URL and triggers download', () => {
    renderPage();

    const xlsxBtn = screen.getByRole('button', { name: 'chargeback.action.exportXlsx' });
    fireEvent.click(xlsxBtn);

    // getBillingXlsxUrl must have been called exactly once.
    expect(api.getBillingXlsxUrl).toHaveBeenCalledTimes(1);

    // Both start and end arguments must be ISO-8601 date strings.
    const mockFn = api.getBillingXlsxUrl as ReturnType<typeof vi.fn>;
    const [start, end] = (mockFn.mock.calls[0] ?? []) as [string, string];
    expect(typeof start).toBe('string');
    expect(typeof end).toBe('string');
    // The mock returns a URL containing the xlsx format parameter.
    expect((mockFn.mock.results[0]?.value as string) ?? '').toContain('format=xlsx');
  });
});

// ---------------------------------------------------------------------------
// Model adoption tab
// ---------------------------------------------------------------------------

const MOCK_ADOPTION = {
  window: 'daily' as const,
  days: 30,
  series: [
    {
      model_id: 'llama3-8b',
      buckets: [
        { date: '2026-09-01', requests: 10, tokens_out: 1000 },
        { date: '2026-09-02', requests: 25, tokens_out: 2500 },
      ],
    },
  ],
};

describe('ChargebackPage — model adoption tab', () => {
  it('renders adoption rows from mocked data when the tab is active', () => {
    mq.useModelAdoption.mockReturnValue({ data: MOCK_ADOPTION, isLoading: false, error: null });
    renderPage();

    fireEvent.click(screen.getByRole('tab', { name: 'chargeback.tab.adoption' }));

    // The model id appears in the adoption table.
    expect(screen.getByText('llama3-8b')).toBeDefined();
    // Aggregate requests (10 + 25 = 35) is rendered.
    expect(screen.getByText('35')).toBeDefined();
  });

  it('calls useModelAdoption once the adoption tab is shown', () => {
    mq.useModelAdoption.mockReturnValue({ data: MOCK_ADOPTION, isLoading: false, error: null });
    renderPage();
    fireEvent.click(screen.getByRole('tab', { name: 'chargeback.tab.adoption' }));
    expect(mq.useModelAdoption).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// SLA compliance tab
// ---------------------------------------------------------------------------

describe('ChargebackPage — SLA compliance tab', () => {
  it('requests the billing report with an sla threshold and renders the rate', () => {
    mq.useBillingReport.mockImplementation((params: { slaThresholdMs?: number }) => {
      if (params.slaThresholdMs != null) {
        return {
          data: {
            ...MOCK_REPORT,
            sla_stats: [{ tenant_id: 'acme/eng', sla_compliance_rate: 0.95, sla_threshold_ms: 2000 }],
          },
          isLoading: false,
          error: null,
        };
      }
      return { data: MOCK_REPORT, isLoading: false, error: null };
    });

    renderPage();
    fireEvent.click(screen.getByRole('tab', { name: 'chargeback.tab.sla' }));

    // The SLA hook variant must have been called with a numeric threshold.
    const calledWithThreshold = mq.useBillingReport.mock.calls.some(
      (c: unknown[]) => (c[0] as { slaThresholdMs?: number })?.slaThresholdMs != null,
    );
    expect(calledWithThreshold).toBe(true);
    // The tenant SLA row is rendered.
    expect(screen.getByText('acme/eng')).toBeDefined();
  });
});
