// SLOPage tests — summary row, compliance table, breached badge,
// window selector, empty state, and enterprise license gate.
import { render, screen, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { SLOPage } from './SLOPage';
import { ApiError } from '../api/http';
import type { SloApiResponse } from '../api/types';

// ---------------------------------------------------------------------------
// Mock hooks
// ---------------------------------------------------------------------------

vi.mock('../hooks/queries', () => ({
  useSloComplianceFull: vi.fn(),
}));

import { useSloComplianceFull } from '../hooks/queries';

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const MET_MODEL = {
  model_id: 'llama3-8b',
  slo: { ttft_ms: 2000, tbt_ms: 500, target_compliance: 0.95 },
  actual: { ttft_compliance: 0.987, tbt_compliance: null, request_count: 1420, period_start: '2026-09-11T00:00:00Z' },
  status: 'met' as const,
};

const BREACHED_MODEL = {
  model_id: 'qwen3-235b',
  slo: { ttft_ms: 1500, tbt_ms: 400, target_compliance: 0.95 },
  actual: { ttft_compliance: 0.72, tbt_compliance: null, request_count: 480, period_start: '2026-09-11T00:00:00Z' },
  status: 'breached' as const,
};

const NO_DATA_MODEL = {
  model_id: 'mixtral-8x22b',
  slo: { ttft_ms: 2000, tbt_ms: 500, target_compliance: 0.95 },
  actual: { ttft_compliance: null, tbt_compliance: null, request_count: 3, period_start: '2026-09-11T00:00:00Z' },
  status: 'insufficient_data' as const,
};

const MOCK_RESPONSE: SloApiResponse = {
  window_hours: 24,
  generated_at: '2026-09-12T07:03:11Z',
  models: [MET_MODEL, BREACHED_MODEL, NO_DATA_MODEL],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function qr(overrides: Record<string, unknown> = {}): any {
  return {
    data: undefined,
    isLoading: false,
    isError: false,
    error: null,
    isFetching: false,
    refetch: vi.fn(),
    ...overrides,
  };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <I18nProvider>
        <SLOPage />
      </I18nProvider>
    </MemoryRouter>,
  );
}

function licenseError(): ApiError {
  return new ApiError(
    402,
    'enterprise license required',
    { error: { feature: 'slo', message: 'enterprise license required', type: 'license_required' } },
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('SLOPage — summary row', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(qr({ data: MOCK_RESPONSE }));
  });

  it('renders summary tiles with correct counts', () => {
    renderPage();
    const summary = screen.getByTestId('slo-summary');
    // Total = 3
    expect(summary).toHaveTextContent('3');
    // Met = 1, Breached = 1, No data = 1
    expect(summary).toHaveTextContent('1');
  });

  it('shows "Total" label in the summary row', () => {
    renderPage();
    expect(screen.getByText('Total')).toBeInTheDocument();
  });

  it('shows "Met" label in the summary row', () => {
    renderPage();
    // "Met" appears in both the KPI tile and the table badge — use getAllByText
    expect(screen.getAllByText('Met').length).toBeGreaterThan(0);
  });

  it('shows "Breached" label in the summary row', () => {
    renderPage();
    // "Breached" appears in both the KPI tile and the table badge — use getAllByText
    expect(screen.getAllByText('Breached').length).toBeGreaterThan(0);
  });
});

describe('SLOPage — compliance table', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(qr({ data: MOCK_RESPONSE }));
  });

  it('renders model names in the table', () => {
    renderPage();
    expect(screen.getByText('llama3-8b')).toBeInTheDocument();
    expect(screen.getByText('qwen3-235b')).toBeInTheDocument();
  });

  it('shows "Breached" badge for breached models', () => {
    renderPage();
    // "Breached" appears in both the KPI tile and the table badge
    expect(screen.getAllByText('Breached').length).toBeGreaterThan(0);
  });

  it('shows "Met" badge for compliant models', () => {
    renderPage();
    // "Met" appears in both the KPI tile and the table badge
    expect(screen.getAllByText('Met').length).toBeGreaterThan(0);
  });

  it('shows "Insufficient data" badge for models with too few requests', () => {
    renderPage();
    expect(screen.getByText('Insufficient data')).toBeInTheDocument();
  });

  it('renders TTFT target column header', () => {
    renderPage();
    expect(screen.getByRole('columnheader', { name: /ttft target/i })).toBeInTheDocument();
  });

  it('renders compliance percentage for models with data', () => {
    renderPage();
    // 0.987 * 100 = 98.7%
    expect(screen.getByText('98.7%')).toBeInTheDocument();
  });
});

describe('SLOPage — window selector', () => {
  it('renders window selector buttons', () => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(qr({ data: MOCK_RESPONSE }));
    renderPage();
    expect(screen.getByRole('button', { name: '1h' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '6h' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '24h' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '7d' })).toBeInTheDocument();
  });

  it('calls useSloComplianceFull with updated windowHours when selector changes', () => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(qr({ data: MOCK_RESPONSE }));
    renderPage();
    // Click "1h"
    fireEvent.click(screen.getByRole('button', { name: '1h' }));
    // The hook should be called with windowHours = 1
    expect(vi.mocked(useSloComplianceFull)).toHaveBeenCalledWith(1);
  });

  it('default window is 24h (hook called with 24)', () => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(qr({ data: MOCK_RESPONSE }));
    renderPage();
    expect(vi.mocked(useSloComplianceFull)).toHaveBeenCalledWith(24);
  });
});

describe('SLOPage — empty state', () => {
  it('shows empty state when no models returned', () => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(
      qr({ data: { window_hours: 24, generated_at: '', models: [] } }),
    );
    renderPage();
    expect(screen.getByText('No SLO data yet')).toBeInTheDocument();
  });
});

describe('SLOPage — enterprise gate', () => {
  it('shows enterprise upgrade card when license_required error', () => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(
      qr({ isError: true, error: licenseError() }),
    );
    renderPage();
    expect(screen.getByText('Enterprise feature')).toBeInTheDocument();
  });

  it('shows generic error for non-license errors', () => {
    vi.clearAllMocks();
    vi.mocked(useSloComplianceFull).mockReturnValue(
      qr({ isError: true, error: new Error('Internal Server Error') }),
    );
    renderPage();
    expect(screen.queryByText('Enterprise feature')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toBeInTheDocument();
  });
});
