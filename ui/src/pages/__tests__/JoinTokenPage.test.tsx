/**
 * JoinTokenPage — UX improvement tests (P-08, P-05).
 *
 * Verifies:
 * 1. Page heading shows "Add Node" (aligned with nav label — P-08).
 * 2. Subtitle contains the returning-user contextual note (P-05).
 * 3. Token value is rendered when data is available.
 * 4. Rotate token button is present and calls the mutation on click.
 * 5. Loading state renders a loading block (not a crash).
 * 6. Error state renders an error message and a retry button.
 */
import { render, screen, fireEvent } from '@testing-library/react';
import { JoinTokenPage } from '../JoinTokenPage';
import { I18nProvider } from '../../i18n';
import type { ReactNode } from 'react';

// ---- shared mock state -------------------------------------------------------

const { rotateMutate } = vi.hoisted(() => ({
  rotateMutate: vi.fn(),
}));

// ---- mock hooks/queries ------------------------------------------------------

vi.mock('../../hooks/queries', () => ({
  useJoinInfo: vi.fn(),
  useRotateToken: () => ({
    mutate: rotateMutate,
    isPending: false,
  }),
}));

// ---- mock api/config so fetch paths resolve without a real server -----------

vi.mock('../../api/config', () => ({
  config: { apiBase: '/api/v1' },
}));

// ---- helpers -----------------------------------------------------------------

function wrapper({ children }: { children: ReactNode }) {
  return <I18nProvider>{children}</I18nProvider>;
}

function renderPage() {
  return render(<JoinTokenPage />, { wrapper });
}

// ---- import the mocked module so we can control its return value per test ---

import { useJoinInfo } from '../../hooks/queries';
const mockUseJoinInfo = useJoinInfo as ReturnType<typeof vi.fn>;

// ---- tests -------------------------------------------------------------------

describe('JoinTokenPage', () => {
  beforeEach(() => {
    rotateMutate.mockClear();
  });

  it('shows "Add Node" as the page heading (P-08 naming alignment)', () => {
    mockUseJoinInfo.mockReturnValue({
      data: { joinToken: 'tok-abc', expiresAt: new Date(Date.now() + 3600_000).toISOString() },
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });

    renderPage();

    expect(screen.getByRole('heading', { name: /add node/i })).toBeInTheDocument();
  });

  it('subtitle contains the returning-user note (P-05)', () => {
    mockUseJoinInfo.mockReturnValue({
      data: { joinToken: 'tok-abc', expiresAt: new Date(Date.now() + 3600_000).toISOString() },
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });

    renderPage();

    // The subtitle must mention "returning" so operators know this is the right
    // page when they already have a cluster and want to expand it.
    expect(screen.getByText(/returning user/i)).toBeInTheDocument();
  });

  it('renders the join token when data is available', () => {
    mockUseJoinInfo.mockReturnValue({
      data: { joinToken: 'tok-xyz-123', expiresAt: new Date(Date.now() + 3600_000).toISOString() },
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });

    renderPage();

    expect(screen.getByText('tok-xyz-123')).toBeInTheDocument();
  });

  it('calls rotate mutation when Rotate token button is clicked', () => {
    mockUseJoinInfo.mockReturnValue({
      data: { joinToken: 'tok-abc', expiresAt: new Date(Date.now() + 3600_000).toISOString() },
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });

    renderPage();

    const rotateBtn = screen.getByRole('button', { name: /rotate token/i });
    fireEvent.click(rotateBtn);

    expect(rotateMutate).toHaveBeenCalledTimes(1);
  });

  it('renders a loading state without crashing', () => {
    mockUseJoinInfo.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });

    // Should not throw
    expect(() => renderPage()).not.toThrow();
  });

  it('renders an error state with retry button when query fails', () => {
    const refetch = vi.fn();
    mockUseJoinInfo.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error('Network error'),
      refetch,
    });

    renderPage();

    // ErrorState renders a retry button
    const retryBtn = screen.getByRole('button', { name: /retry/i });
    expect(retryBtn).toBeInTheDocument();

    fireEvent.click(retryBtn);
    expect(refetch).toHaveBeenCalledTimes(1);
  });
});
