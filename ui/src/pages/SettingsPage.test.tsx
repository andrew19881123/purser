/**
 * SettingsPage — unit tests for API key usage, usage summary and license status.
 *
 * Strategy: mock the hooks layer so we never touch the real API client
 * (which contains top-level await) and have full control over returned data.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

// Mock the entire hooks/queries module before importing SettingsPage.
vi.mock('../hooks/queries', () => ({
  useApiKeys: vi.fn(),
  useCreateApiKey: vi.fn(),
  useRevokeApiKey: vi.fn(),
  useKeyUsage: vi.fn(),
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
function pending() {
  return { isLoading: true, isError: false, error: null, data: undefined, refetch: vi.fn() };
}
function idle() {
  return { isLoading: false, isError: false, error: null, data: undefined, refetch: vi.fn() };
}

// Mutation stub (not under test here).
const mutationStub = { mutate: vi.fn(), isPending: false };

function mkQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderPage() {
  return render(
    <QueryClientProvider client={mkQueryClient()}>
      <SettingsPage />
    </QueryClientProvider>,
  );
}

// Typed access to mocked functions.
const mq = queries as unknown as {
  useApiKeys: ReturnType<typeof vi.fn>;
  useCreateApiKey: ReturnType<typeof vi.fn>;
  useRevokeApiKey: ReturnType<typeof vi.fn>;
  useKeyUsage: ReturnType<typeof vi.fn>;
  useUsageSummary: ReturnType<typeof vi.fn>;
  useEnterpriseStatus: ReturnType<typeof vi.fn>;
};

beforeEach(() => {
  // Default: no keys, no usage, community edition.
  mq.useApiKeys.mockReturnValue(success([]));
  mq.useCreateApiKey.mockReturnValue(mutationStub);
  mq.useRevokeApiKey.mockReturnValue(mutationStub);
  mq.useKeyUsage.mockReturnValue(idle());
  mq.useUsageSummary.mockReturnValue(success({ tenants: [] }));
  mq.useEnterpriseStatus.mockReturnValue(
    success({ edition: 'community', licensee: 'community', features: [] }),
  );
});

// ---------------------------------------------------------------------------
// Task A — per-key token usage
// ---------------------------------------------------------------------------

describe('shows_key_usage_tokens', () => {
  it('renders formatted in/out counts for an API key', () => {
    const mockKey = {
      id: 'key_abc',
      name: 'Test key',
      team: 'team-a',
      prefix: 'sk-purser-xxxx',
      role: 'admin' as const,
      createdAt: new Date().toISOString(),
      lastUsedAt: null,
      monthlyQuota: null,
      usedThisMonth: 0,
      revoked: false,
    };
    mq.useApiKeys.mockReturnValue(success([mockKey]));
    mq.useKeyUsage.mockImplementation((keyId: string | undefined) => {
      if (keyId === 'key_abc') {
        return success({
          apiKeyId: 'key_abc',
          totalRequests: 42,
          inputTokens: 1234,
          outputTokens: 567,
        });
      }
      return idle();
    });

    const { getByTestId } = renderPage();

    // formatTokenCount(1234) => "1.2K", formatTokenCount(567) => "567"
    expect(getByTestId('key-token-usage')).toHaveTextContent('1.2K in / 567 out');
  });

  it('shows loading state while key usage is fetching', () => {
    const mockKey = {
      id: 'key_loading',
      name: 'Loading key',
      team: 'team-b',
      prefix: 'sk-purser-yyyy',
      role: 'viewer' as const,
      createdAt: new Date().toISOString(),
      lastUsedAt: null,
      monthlyQuota: null,
      usedThisMonth: 0,
      revoked: false,
    };
    mq.useApiKeys.mockReturnValue(success([mockKey]));
    mq.useKeyUsage.mockReturnValue(pending());

    const { getByText } = renderPage();

    // Our mock t() returns the key itself.
    expect(getByText('settings.usage.loading')).toBeDefined();
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
