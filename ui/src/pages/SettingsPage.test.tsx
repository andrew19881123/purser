/**
 * SettingsPage — unit tests for quick stats, usage summary and license status.
 *
 * Note: API key table tests moved to ApiKeysPage.test.tsx in v0.6 when API
 * key management was extracted to its own dedicated page (/api-keys).
 *
 * Strategy: mock the hooks layer so we never touch the real API client
 * (which contains top-level await) and have full control over returned data.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

// Mock the entire hooks/queries module before importing SettingsPage.
vi.mock('../hooks/queries', () => ({
  useApiKeys: vi.fn(),
  useUsageSummary: vi.fn(),
  useEnterpriseStatus: vi.fn(),
}));

// Mock the i18n module so we can render without a provider.
vi.mock('../i18n', () => ({
  useT: () => (key: string) => key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

import { SettingsPage } from './SettingsPage';
import * as queries from '../hooks/queries';

// Minimal query result shapes used in tests.
function success<T>(data: T) {
  return { isLoading: false, isError: false, error: null, data, refetch: vi.fn() };
}

function mkQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderPage() {
  return render(
    <MemoryRouter>
      <QueryClientProvider client={mkQueryClient()}>
        <SettingsPage />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

// Typed access to mocked functions.
const mq = queries as unknown as {
  useApiKeys: ReturnType<typeof vi.fn>;
  useUsageSummary: ReturnType<typeof vi.fn>;
  useEnterpriseStatus: ReturnType<typeof vi.fn>;
};

beforeEach(() => {
  // Default: no keys, no usage, community edition.
  mq.useApiKeys.mockReturnValue(success([]));
  mq.useUsageSummary.mockReturnValue(success({ tenants: [] }));
  mq.useEnterpriseStatus.mockReturnValue(
    success({ edition: 'community', licensee: 'community', features: [] }),
  );
});

// ---------------------------------------------------------------------------
// Task A — "Manage API Keys →" link card (API key table moved to ApiKeysPage)
// ---------------------------------------------------------------------------

describe('settings_page_api_keys_link', () => {
  it('shows manage api keys link pointing to /api-keys', () => {
    const { getByTestId } = renderPage();

    const link = getByTestId('manage-api-keys-link');
    expect(link).toBeDefined();
    expect(link.getAttribute('href')).toBe('/api-keys');
  });
});

// ---------------------------------------------------------------------------
// Task B — usage summary
// ---------------------------------------------------------------------------

describe('shows_usage_summary_by_tenant', () => {
  it('renders a row per tenant in the usage summary table', () => {
    mq.useUsageSummary.mockReturnValue(
      success({
        tenants: [
          {
            tenant: 'team-alpha',
            totalRequests: 5000,
            inputTokens: 1_200_000,
            outputTokens: 400_000,
          },
          {
            tenant: 'team-beta',
            totalRequests: 2500,
            inputTokens: 600_000,
            outputTokens: 200_000,
          },
        ],
      }),
    );

    const { getByTestId } = renderPage();

    const table = getByTestId('usage-summary-table');
    expect(table).toBeDefined();
    expect(table.textContent).toContain('team-alpha');
    expect(table.textContent).toContain('team-beta');
    // formatTokenCount(1_200_000) => "1.2M"
    expect(table.textContent).toContain('1.2M');
  });

  it('shows empty state when there are no tenants', () => {
    mq.useUsageSummary.mockReturnValue(success({ tenants: [] }));

    const { getByText } = renderPage();

    // EmptyState renders the message string; our mock t() returns the key itself.
    expect(getByText('settings.usage.summary.empty')).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// Task C — license status
// ---------------------------------------------------------------------------

describe('shows_community_edition_badge', () => {
  it('renders the Community badge for the community edition', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({ edition: 'community', licensee: 'community', features: [] }),
    );

    const { getByTestId, queryByTestId } = renderPage();

    expect(getByTestId('community-badge')).toBeDefined();
    expect(queryByTestId('enterprise-badge')).toBeNull();
  });

  it('renders the community description text key', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({ edition: 'community', licensee: 'community', features: [] }),
    );

    const { getByText } = renderPage();

    expect(getByText('settings.license.community.desc')).toBeDefined();
  });
});

describe('shows_enterprise_features_list', () => {
  it('renders feature badges for an enterprise license', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({
        edition: 'enterprise',
        licensee: 'Acme Corp',
        features: ['audit', 'ha', 'rbac'],
        expires: '2030-01-01T00:00:00Z',
      }),
    );

    const { getByTestId, queryByTestId } = renderPage();

    expect(getByTestId('enterprise-badge')).toBeDefined();
    expect(queryByTestId('community-badge')).toBeNull();

    const badges = getByTestId('feature-badges');
    expect(badges.textContent).toContain('audit');
    expect(badges.textContent).toContain('ha');
    expect(badges.textContent).toContain('rbac');
  });
});

describe('shows_expiry_warning_when_expired', () => {
  it('renders the expired badge when the expiry date is in the past', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({
        edition: 'enterprise',
        licensee: 'Old Corp',
        features: ['audit'],
        expires: '2020-01-01T00:00:00Z', // in the past
      }),
    );

    const { getByTestId } = renderPage();

    expect(getByTestId('expired-badge')).toBeDefined();
  });

  it('does NOT render the expired badge for a future expiry', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({
        edition: 'enterprise',
        licensee: 'New Corp',
        features: ['ha'],
        expires: '2099-12-31T00:00:00Z', // far future
      }),
    );

    const { queryByTestId } = renderPage();

    expect(queryByTestId('expired-badge')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// SettingsPage — License tile (task spec tests)
// ---------------------------------------------------------------------------

describe('SettingsPage — License tile', () => {
  it('shows MIT Core mode when no license endpoint or 404', () => {
    mq.useEnterpriseStatus.mockReturnValue({
      isLoading: false,
      isError: true,
      error: new Error('HTTP 404'),
      data: undefined,
      refetch: vi.fn(),
    });

    const { getByTestId, queryByTestId } = renderPage();

    expect(getByTestId('community-badge')).toBeDefined();
    expect(getByTestId('mit-core-desc')).toBeDefined();
    expect(queryByTestId('enterprise-badge')).toBeNull();
  });

  it('shows Enterprise plan name and expiry when license present', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({
        edition: 'enterprise',
        licensee: 'Acme Corp',
        features: ['audit'],
        expires: '2030-06-15T00:00:00Z',
      }),
    );

    const { getByTestId } = renderPage();

    expect(getByTestId('enterprise-badge')).toBeDefined();
    expect(getByTestId('license-licensee').textContent).toBe('Acme Corp');
    expect(getByTestId('license-expiry')).toBeDefined();
  });

  it('shows days remaining correctly', () => {
    // Expiry 30 days in the future.
    const futureExpiry = new Date(Date.now() + 30 * 86_400_000).toISOString();
    mq.useEnterpriseStatus.mockReturnValue(
      success({
        edition: 'enterprise',
        licensee: 'Acme Corp',
        features: [],
        expires: futureExpiry,
      }),
    );

    const { getByTestId } = renderPage();

    const daysEl = getByTestId('days-remaining');
    expect(daysEl).toBeDefined();
    // Text should contain a number (the days count).
    expect(daysEl.textContent).toMatch(/\d+/);
  });

  it('lists active feature gates', () => {
    mq.useEnterpriseStatus.mockReturnValue(
      success({
        edition: 'enterprise',
        licensee: 'FeatureCorp',
        features: ['raft_ha', 'opa_policies', 'chargeback'],
        expires: '2099-01-01T00:00:00Z',
      }),
    );

    const { getByTestId } = renderPage();

    const badges = getByTestId('feature-badges');
    expect(badges.textContent).toContain('raft_ha');
    expect(badges.textContent).toContain('opa_policies');
    expect(badges.textContent).toContain('chargeback');
  });
});
