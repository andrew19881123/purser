/**
 * SettingsPage — revoke-confirm modal hardening tests (H11).
 *
 * Verifies:
 * 1. Clicking "Revoke" opens a Modal instead of calling the mutation immediately.
 * 2. Clicking Cancel closes the modal without calling the mutation.
 * 3. Clicking Revoke in the modal calls revoke.mutate with the correct key id.
 */
import { render, screen, fireEvent, within } from '@testing-library/react';
import { SettingsPage } from '../SettingsPage';
import { I18nProvider } from '../../i18n';
import type { ReactNode } from 'react';
import type { ApiKey } from '../../api/types';

// ---- shared mock state ------------------------------------------------------

const { revokeMutate } = vi.hoisted(() => ({
  revokeMutate: vi.fn(),
}));

// ---- mock the entire hooks/queries module -----------------------------------

vi.mock('../../hooks/queries', () => ({
  useApiKeys: () => ({
    data: [mockApiKey()],
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  }),
  useCreateApiKey: () => ({
    mutate: vi.fn(),
    isPending: false,
  }),
  useRevokeApiKey: () => ({
    mutate: revokeMutate,
    isPending: false,
  }),
}));

// ---- helpers ----------------------------------------------------------------

function mockApiKey(): ApiKey {
  return {
    id: 'key-1',
    name: 'my-test-key',
    team: 'engineering',
    prefix: 'psk_abc',
    role: 'admin',
    createdAt: '2026-01-01T00:00:00Z',
    lastUsedAt: null,
    monthlyQuota: null,
    usedThisMonth: 0,
    revoked: false,
  };
}

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nProvider>{children}</I18nProvider>;
}

// ---- setup ------------------------------------------------------------------

beforeEach(() => {
  revokeMutate.mockClear();
});

// ---- tests ------------------------------------------------------------------

describe('SettingsPage — revoke API key modal', () => {
  it('revoke_apikey_opens_confirm_modal_without_mutating', () => {
    render(<SettingsPage />, { wrapper: Wrapper });

    fireEvent.click(screen.getByText('Revoke'));

    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('Revoke API key?')).toBeInTheDocument();
    expect(revokeMutate).not.toHaveBeenCalled();
  });

  it('revoke_apikey_cancel_closes_modal_without_mutating', () => {
    render(<SettingsPage />, { wrapper: Wrapper });

    fireEvent.click(screen.getByText('Revoke'));
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Cancel'));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(revokeMutate).not.toHaveBeenCalled();
  });

  it('revoke_apikey_confirm_calls_mutate_with_key_id', () => {
    render(<SettingsPage />, { wrapper: Wrapper });

    fireEvent.click(screen.getByText('Revoke'));

    const modal = screen.getByRole('dialog');
    // The modal body shows the key name
    expect(modal).toHaveTextContent('my-test-key');
    // Confirm inside the modal
    fireEvent.click(within(modal).getByText('Revoke'));

    expect(revokeMutate).toHaveBeenCalledWith('key-1');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
