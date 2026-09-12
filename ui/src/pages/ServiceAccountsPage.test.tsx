/**
 * ServiceAccountsPage — unit tests.
 *
 * Verifies: table rendering, role badge colors, create modal, key-shown-once
 * pattern, and delete confirmation flow.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, act } from '@testing-library/react';
import { ServiceAccountsPage } from './ServiceAccountsPage';

vi.mock('../i18n', () => ({
  useT: () => (key: string) => key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

vi.mock('../hooks/queries', () => ({
  useServiceAccounts: vi.fn(),
  useCreateServiceAccount: vi.fn(),
  useRevokeServiceAccount: vi.fn(),
}));

import * as queries from '../hooks/queries';

const mq = queries as unknown as {
  useServiceAccounts: ReturnType<typeof vi.fn>;
  useCreateServiceAccount: ReturnType<typeof vi.fn>;
  useRevokeServiceAccount: ReturnType<typeof vi.fn>;
};

function success<T>(data: T) {
  return { isLoading: false, isError: false, error: null, data, refetch: vi.fn() };
}

const mutationStub = { mutate: vi.fn(), isPending: false };

function mkSa(overrides: Partial<import('../api/types').ServiceAccount> = {}): import('../api/types').ServiceAccount {
  return {
    id: 'sa-test-01',
    name: 'ci-pipeline',
    tenant: 'platform',
    description: 'Test pipeline',
    role: 'inference',
    scopes: [],
    clientId: 'sa_test_a1b2c3d4',
    enabled: true,
    lastUsedAt: new Date(Date.now() - 3600_000).toISOString(),
    createdAt: new Date().toISOString(),
    ...overrides,
  };
}

beforeEach(() => {
  mq.useServiceAccounts.mockReturnValue(success([]));
  mq.useCreateServiceAccount.mockReturnValue(mutationStub);
  mq.useRevokeServiceAccount.mockReturnValue(mutationStub);
});

// ---------------------------------------------------------------------------
// Table rendering
// ---------------------------------------------------------------------------

describe('ServiceAccountsPage — table', () => {
  it('shows empty state when no service accounts', () => {
    render(<ServiceAccountsPage />);
    expect(screen.getByText(/no service accounts yet/i)).toBeDefined();
  });

  it('renders table with name, clientId, team, role, status columns', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa()]));
    render(<ServiceAccountsPage />);
    expect(screen.getByText('ci-pipeline')).toBeDefined();
    expect(screen.getByTestId('sa-client-id-cell').textContent).toBe('sa_test_a1b2c3d4');
  });

  it('renders role badge for inference role', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa({ role: 'inference' })]));
    const { container } = render(<ServiceAccountsPage />);
    const badges = container.querySelectorAll('[data-testid="role-badge"]');
    expect(badges.length).toBeGreaterThan(0);
    expect(badges[0].textContent).toBe('inference');
  });

  it('renders role badge for admin role', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa({ role: 'admin' })]));
    const { container } = render(<ServiceAccountsPage />);
    const badges = container.querySelectorAll('[data-testid="role-badge"]');
    expect(badges[0].textContent).toBe('admin');
  });

  it('shows enabled badge for active service account', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa({ enabled: true })]));
    const { container } = render(<ServiceAccountsPage />);
    const statusBadge = container.querySelector('[data-testid="sa-status-badge"]');
    expect(statusBadge?.textContent).toBe('enabled');
  });

  it('shows disabled badge for revoked service account', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa({ enabled: false })]));
    const { container } = render(<ServiceAccountsPage />);
    const statusBadge = container.querySelector('[data-testid="sa-status-badge"]');
    expect(statusBadge?.textContent).toBe('disabled');
  });

  it('does not show revoke button for disabled accounts', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa({ enabled: false })]));
    render(<ServiceAccountsPage />);
    expect(screen.queryByTestId('revoke-sa-btn')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Create modal
// ---------------------------------------------------------------------------

describe('ServiceAccountsPage — create modal', () => {
  it('opens create modal on button click', () => {
    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('new-sa-btn'));
    // Check the modal is open by looking for the form field inside the dialog
    expect(screen.getByTestId('sa-name-input')).toBeDefined();
  });

  it('submit is disabled when name or team is empty', () => {
    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('new-sa-btn'));
    expect(screen.getByTestId('create-sa-submit')).toBeDisabled();
  });

  it('submit becomes enabled when name and team are filled', () => {
    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('new-sa-btn'));
    fireEvent.change(screen.getByTestId('sa-name-input'), { target: { value: 'my-sa' } });
    fireEvent.change(screen.getByTestId('sa-team-input'), { target: { value: 'infra' } });
    expect(screen.getByTestId('create-sa-submit')).not.toBeDisabled();
  });

  it('calls createServiceAccount with correct fields', () => {
    const mutate = vi.fn();
    mq.useCreateServiceAccount.mockReturnValue({ mutate, isPending: false });
    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('new-sa-btn'));
    fireEvent.change(screen.getByTestId('sa-name-input'), { target: { value: 'deploy-bot' } });
    fireEvent.change(screen.getByTestId('sa-team-input'), { target: { value: 'engineering' } });
    fireEvent.click(screen.getByTestId('create-sa-submit'));
    expect(mutate).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'deploy-bot', teamId: 'engineering' }),
      expect.any(Object),
    );
  });
});

// ---------------------------------------------------------------------------
// Key shown once
// ---------------------------------------------------------------------------

describe('ServiceAccountsPage — secret shown once', () => {
  it('shows secret modal with warning after creation', async () => {
    let successCb: ((sa: import('../api/types').ServiceAccountWithSecret) => void) | undefined;
    const mutate = vi.fn((_input: unknown, opts: { onSuccess?: (sa: import('../api/types').ServiceAccountWithSecret) => void }) => {
      successCb = opts.onSuccess;
    });
    mq.useCreateServiceAccount.mockReturnValue({ mutate, isPending: false });

    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('new-sa-btn'));
    fireEvent.change(screen.getByTestId('sa-name-input'), { target: { value: 'x' } });
    fireEvent.change(screen.getByTestId('sa-team-input'), { target: { value: 'y' } });
    fireEvent.click(screen.getByTestId('create-sa-submit'));

    await act(async () => {
      successCb?.({
        ...mkSa({ name: 'x', tenant: 'y' }),
        clientSecret: 'super_secret_abc123',
      });
    });

    expect(screen.getByTestId('sa-secret-warning')).toBeDefined();
    expect(screen.getByTestId('sa-client-secret').textContent).toBe('super_secret_abc123');
  });
});

// ---------------------------------------------------------------------------
// Delete confirmation
// ---------------------------------------------------------------------------

describe('ServiceAccountsPage — revoke confirmation', () => {
  it('shows confirmation modal before revoking', () => {
    mq.useServiceAccounts.mockReturnValue(success([mkSa()]));
    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('revoke-sa-btn'));
    expect(screen.getByText(/revoke service account/i)).toBeDefined();
  });

  it('calls revokeServiceAccount on confirm', () => {
    const mutate = vi.fn();
    mq.useRevokeServiceAccount.mockReturnValue({ mutate, isPending: false });
    mq.useServiceAccounts.mockReturnValue(success([mkSa()]));

    render(<ServiceAccountsPage />);
    fireEvent.click(screen.getByTestId('revoke-sa-btn'));
    fireEvent.click(screen.getByTestId('revoke-sa-confirm'));
    expect(mutate).toHaveBeenCalledWith('sa-test-01');
  });
});
