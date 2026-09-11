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

// Typed access to the mocked hook.
const mq = queries as unknown as { useBillingReport: ReturnType<typeof vi.fn> };

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
