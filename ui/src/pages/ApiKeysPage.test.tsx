/**
 * ApiKeysPage — unit tests for API key table, quota bar, and CSV export.
 *
 * These tests were split from SettingsPage.test.tsx in v0.6 when API key
 * management was extracted to its own dedicated page at /api-keys.
 *
 * Strategy: mock the hooks layer and i18n so we never touch the real API
 * client and have full control over returned data and rendered strings.
 */
import { describe, it, expect, vi, beforeEach, beforeAll } from 'vitest';
import { render } from '@testing-library/react';
import { fireEvent, screen } from '@testing-library/react';
import { ApiKeysPage } from './ApiKeysPage';

// Mock i18n so t() returns the key string — makes assertions key-independent
// from real translation values, matching SettingsPage.test.tsx approach.
vi.mock('../i18n', () => ({
  useT: () => (key: string) => key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

// Mock hooks/queries before importing the page component.
vi.mock('../hooks/queries', () => ({
  useApiKeys: vi.fn(),
  useCreateApiKey: vi.fn(),
  useRevokeApiKey: vi.fn(),
  useKeyUsage: vi.fn(),
}));

import * as queries from '../hooks/queries';

// jsdom doesn't implement URL.createObjectURL — define stubs globally.
beforeAll(() => {
  if (!URL.createObjectURL) {
    URL.createObjectURL = vi.fn();
  }
  if (!URL.revokeObjectURL) {
    URL.revokeObjectURL = vi.fn();
  }
});

// Minimal query result shapes.
function success<T>(data: T) {
  return { isLoading: false, isError: false, error: null, data, refetch: vi.fn() };
}
function pending() {
  return { isLoading: true, isError: false, error: null, data: undefined, refetch: vi.fn() };
}
function idle() {
  return { isLoading: false, isError: false, error: null, data: undefined, refetch: vi.fn() };
}

const mutationStub = { mutate: vi.fn(), isPending: false };

// Typed access to mocked functions.
const mq = queries as unknown as {
  useApiKeys: ReturnType<typeof vi.fn>;
  useCreateApiKey: ReturnType<typeof vi.fn>;
  useRevokeApiKey: ReturnType<typeof vi.fn>;
  useKeyUsage: ReturnType<typeof vi.fn>;
};

function renderPage() {
  return render(<ApiKeysPage />);
}

// Helper: minimal valid API key fixture.
function mkKey(overrides: Partial<{
  id: string;
  name: string;
  team: string;
  prefix: string;
  role: 'admin' | 'viewer' | 'inference';
  createdAt: string;
  lastUsedAt: string | null;
  monthlyQuota: number | null;
  usedThisMonth: number;
  revoked: boolean;
}> = {}) {
  return {
    id: 'key_test',
    name: 'Test key',
    team: 'team-x',
    prefix: 'sk-purser-test',
    role: 'admin' as const,
    createdAt: new Date().toISOString(),
    lastUsedAt: null,
    monthlyQuota: null,
    usedThisMonth: 0,
    revoked: false,
    ...overrides,
  };
}

beforeEach(() => {
  mq.useApiKeys.mockReturnValue(success([]));
  mq.useCreateApiKey.mockReturnValue(mutationStub);
  mq.useRevokeApiKey.mockReturnValue(mutationStub);
  mq.useKeyUsage.mockReturnValue(idle());
});

// ---------------------------------------------------------------------------
// API Keys table
// ---------------------------------------------------------------------------

describe('ApiKeysPage — API Keys table', () => {
  it('renders key table with name, team, role, and status columns', () => {
    mq.useApiKeys.mockReturnValue(success([mkKey()]));
    mq.useKeyUsage.mockReturnValue(idle());

    const { getByText } = renderPage();

    // Column headers — mock t() returns the key string as-is.
    expect(getByText('settings.col.name')).toBeDefined();
    expect(getByText('settings.col.team')).toBeDefined();
    expect(getByText('settings.col.role')).toBeDefined();
    expect(getByText('settings.col.lastUsed')).toBeDefined();
    expect(getByText('settings.col.status')).toBeDefined();
  });

  it('shows quota progress bar with correct percentage', () => {
    mq.useApiKeys.mockReturnValue(
      success([mkKey({ id: 'key_quota', monthlyQuota: 1000, usedThisMonth: 800 })]),
    );
    mq.useKeyUsage.mockReturnValue(idle());

    const { container } = renderPage();

    const meter = container.querySelector('[role="meter"]');
    expect(meter).not.toBeNull();
    // 800 / 1000 = 80 %
    expect(meter!.getAttribute('aria-valuenow')).toBe('80');
  });

  it('shows "never" when last_used_at is null', () => {
    mq.useApiKeys.mockReturnValue(
      success([mkKey({ id: 'key_never', lastUsedAt: null })]),
    );
    mq.useKeyUsage.mockReturnValue(idle());

    const { getByText } = renderPage();

    expect(getByText('settings.usage.never')).toBeDefined();
  });

  it('shows revoked badge when key is disabled', () => {
    mq.useApiKeys.mockReturnValue(
      success([mkKey({ id: 'key_revoked', revoked: true })]),
    );
    mq.useKeyUsage.mockReturnValue(idle());

    const { getByText } = renderPage();

    expect(getByText('settings.status.revoked')).toBeDefined();
  });

  it('shows usage tokens after async load', () => {
    mq.useApiKeys.mockReturnValue(success([mkKey({ id: 'key_async' })]));
    mq.useKeyUsage.mockImplementation((keyId: string | undefined) => {
      if (keyId === 'key_async') {
        return success({
          apiKeyId: 'key_async',
          totalRequests: 5,
          inputTokens: 5000,
          outputTokens: 2500,
        });
      }
      return idle();
    });

    const { getByTestId } = renderPage();

    // formatTokenCount(5000) => "5.0K", formatTokenCount(2500) => "2.5K"
    expect(getByTestId('key-token-usage')).toHaveTextContent('5.0K in / 2.5K out');
  });

  it('shows loading state while key usage is fetching', () => {
    mq.useApiKeys.mockReturnValue(
      success([mkKey({ id: 'key_loading' })]),
    );
    mq.useKeyUsage.mockReturnValue(pending());

    const { getByText } = renderPage();

    // Our mock t() returns the key itself.
    expect(getByText('settings.usage.loading')).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// CSV export
// ---------------------------------------------------------------------------

describe('ApiKeysPage — CSV export', () => {
  it('download CSV button is disabled when no keys', () => {
    mq.useApiKeys.mockReturnValue(success([]));

    renderPage();

    const btn = screen.getByRole('button', { name: /apikeys\.csv/i });
    expect(btn).toBeDisabled();
  });

  it('download CSV button is enabled when keys exist', () => {
    mq.useApiKeys.mockReturnValue(success([mkKey()]));
    mq.useKeyUsage.mockReturnValue(idle());

    renderPage();

    const btn = screen.getByRole('button', { name: /apikeys\.csv/i });
    expect(btn).not.toBeDisabled();
  });

  it('clicking CSV button triggers a download without error', () => {
    mq.useApiKeys.mockReturnValue(success([mkKey({ id: 'key_csv', name: 'export-key' })]));
    mq.useKeyUsage.mockReturnValue(idle());

    const createURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock');
    const revokeURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});

    // Stub document.createElement for the anchor click.
    const mockAnchor = { href: '', download: '', click: vi.fn() };
    const origCreate = document.createElement.bind(document);
    const createSpy = vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      if (tag === 'a') return mockAnchor as unknown as HTMLElement;
      return origCreate(tag);
    });

    renderPage();

    fireEvent.click(screen.getByRole('button', { name: /apikeys\.csv/i }));

    expect(createURL).toHaveBeenCalled();
    expect(mockAnchor.click).toHaveBeenCalled();
    expect(revokeURL).toHaveBeenCalled();

    createURL.mockRestore();
    revokeURL.mockRestore();
    createSpy.mockRestore();
  });
});
