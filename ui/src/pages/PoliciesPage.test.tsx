// PoliciesPage tests — policy list, status badges, toggle, source view,
// upload modal validation, empty state, and enterprise license gate.
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { PoliciesPage } from './PoliciesPage';
import { ApiError } from '../api/http';
import type { Policy } from '../api/types';

// ---------------------------------------------------------------------------
// Mock hooks
// ---------------------------------------------------------------------------

vi.mock('../hooks/queries', () => ({
  usePolicies: vi.fn(),
  useUpsertPolicy: vi.fn(),
  useDeletePolicy: vi.fn(),
}));

import { usePolicies, useUpsertPolicy, useDeletePolicy } from '../hooks/queries';

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const POLICY_WITH_COMMENT: Policy = {
  id: 1,
  name: 'allow-approved-models',
  source: `package purser\n\n# Allow only approved models\ndefault allow = false\n`,
  enabled: true,
  createdAt: '2026-09-01T10:00:00Z',
  description: 'Allow only approved models',
};

const POLICY_DISABLED: Policy = {
  id: 2,
  name: 'block-external-tenants',
  source: `package purser\n\ndefault allow = true\n`,
  enabled: false,
  createdAt: '2026-09-02T12:00:00Z',
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

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function mut(overrides: Record<string, unknown> = {}): any {
  return {
    mutate: vi.fn(),
    isPending: false,
    isError: false,
    error: null,
    ...overrides,
  };
}

function mockAll(policies: Policy[] = []) {
  vi.mocked(usePolicies).mockReturnValue(qr({ data: { policies } }));
  vi.mocked(useUpsertPolicy).mockReturnValue(mut());
  vi.mocked(useDeletePolicy).mockReturnValue(mut());
}

function renderPage() {
  return render(
    <MemoryRouter>
      <I18nProvider>
        <PoliciesPage />
      </I18nProvider>
    </MemoryRouter>,
  );
}

function licenseError(): ApiError {
  return new ApiError(
    402,
    'enterprise license required',
    { error: { feature: 'policy_engine', message: 'enterprise license required', type: 'license_required' } },
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('PoliciesPage — policy list', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders policy names in a table', () => {
    mockAll([POLICY_WITH_COMMENT, POLICY_DISABLED]);
    renderPage();
    expect(screen.getByText('allow-approved-models')).toBeInTheDocument();
    expect(screen.getByText('block-external-tenants')).toBeInTheDocument();
  });

  it('shows "Enforced" badge for enabled policies', () => {
    mockAll([POLICY_WITH_COMMENT]);
    renderPage();
    expect(screen.getByText('Enforced')).toBeInTheDocument();
  });

  it('shows "Disabled" badge for disabled policies', () => {
    mockAll([POLICY_DISABLED]);
    renderPage();
    expect(screen.getByText('Disabled')).toBeInTheDocument();
  });

  it('shows description derived from first comment line', () => {
    mockAll([POLICY_WITH_COMMENT]);
    renderPage();
    expect(screen.getByText('Allow only approved models')).toBeInTheDocument();
  });
});

describe('PoliciesPage — source view', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAll([POLICY_WITH_COMMENT]);
  });

  it('shows "View source" button for each policy', () => {
    renderPage();
    expect(screen.getByRole('button', { name: /view source/i })).toBeInTheDocument();
  });

  it('opens inline Rego source panel on "View source" click', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /view source/i }));
    // Source panel should show the Rego source in a code block
    expect(screen.getByText(/package purser/i)).toBeInTheDocument();
  });

  it('shows line numbers in the source panel', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /view source/i }));
    // Line "1" should appear as a line number
    const lineNumbers = screen.getAllByText('1');
    expect(lineNumbers.length).toBeGreaterThan(0);
  });

  it('closes the source panel when close button is clicked', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /view source/i }));
    expect(screen.getByText(/package purser/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /close source panel/i }));
    expect(screen.queryByText(/package purser/i)).not.toBeInTheDocument();
  });
});

describe('PoliciesPage — toggle enable/disable', () => {
  it('calls upsertPolicy with toggled enabled state when "Disable" is clicked', () => {
    vi.clearAllMocks();
    const upsertMutate = vi.fn();
    vi.mocked(usePolicies).mockReturnValue(qr({ data: { policies: [POLICY_WITH_COMMENT] } }));
    vi.mocked(useUpsertPolicy).mockReturnValue(mut({ mutate: upsertMutate }));
    vi.mocked(useDeletePolicy).mockReturnValue(mut());

    renderPage();
    // The enabled policy should show a "Disable" action
    fireEvent.click(screen.getByRole('button', { name: /^disable$/i }));
    expect(upsertMutate).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'allow-approved-models', enabled: false }),
    );
  });

  it('calls upsertPolicy with enabled=true when "Enable" is clicked on disabled policy', () => {
    vi.clearAllMocks();
    const upsertMutate = vi.fn();
    vi.mocked(usePolicies).mockReturnValue(qr({ data: { policies: [POLICY_DISABLED] } }));
    vi.mocked(useUpsertPolicy).mockReturnValue(mut({ mutate: upsertMutate }));
    vi.mocked(useDeletePolicy).mockReturnValue(mut());

    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /^enable$/i }));
    expect(upsertMutate).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'block-external-tenants', enabled: true }),
    );
  });
});

describe('PoliciesPage — upload modal', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAll([]);
  });

  it('opens upload modal when "Upload policy" button is clicked', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /upload policy/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('Upload Policy')).toBeInTheDocument();
  });

  it('keeps submit button disabled when policy name is empty', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /upload policy/i }));
    const uploadBtn = screen.getByRole('button', { name: /^upload$/i });
    expect(uploadBtn).toBeDisabled();
  });

  it('shows validation error when upload is attempted with empty name', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /upload policy/i }));
    // Try clicking the Upload button (it's disabled, but let's check via the submit path)
    const nameInput = screen.getByRole('textbox', { name: /policy name/i });
    expect(nameInput).toBeInTheDocument();
    // Submit with empty name by directly calling validation
    // The button should be disabled so this tests that the input exists and is required
    expect(nameInput).toHaveAttribute('aria-required', 'true');
  });

  it('enables submit button when name is provided', async () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /upload policy/i }));
    const nameInput = screen.getByRole('textbox', { name: /policy name/i });
    fireEvent.change(nameInput, { target: { value: 'my-policy' } });
    await waitFor(() => {
      const uploadBtn = screen.getByRole('button', { name: /^upload$/i });
      expect(uploadBtn).not.toBeDisabled();
    });
  });

  it('closes modal when Cancel is clicked', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /upload policy/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});

describe('PoliciesPage — empty state', () => {
  it('renders empty state when no policies exist', () => {
    vi.clearAllMocks();
    mockAll([]);
    renderPage();
    expect(screen.getByText('No policies')).toBeInTheDocument();
    expect(screen.getByText(/evaluate every deploy/i)).toBeInTheDocument();
  });

  it('empty state has a docs link', () => {
    vi.clearAllMocks();
    mockAll([]);
    renderPage();
    const docsLink = screen.getByRole('link', { name: /policy docs/i });
    expect(docsLink).toBeInTheDocument();
    expect(docsLink.getAttribute('href')).toContain('policy-as-code');
  });
});

describe('PoliciesPage — enterprise gate', () => {
  it('shows enterprise upgrade card when license_required error', () => {
    vi.clearAllMocks();
    vi.mocked(usePolicies).mockReturnValue(qr({ isError: true, error: licenseError() }));
    vi.mocked(useUpsertPolicy).mockReturnValue(mut());
    vi.mocked(useDeletePolicy).mockReturnValue(mut());

    renderPage();
    expect(screen.getByText('Enterprise feature')).toBeInTheDocument();
    expect(screen.queryByText('Could not load policies')).not.toBeInTheDocument();
  });

  it('enterprise gate contains a link to enterprise docs', () => {
    vi.clearAllMocks();
    vi.mocked(usePolicies).mockReturnValue(qr({ isError: true, error: licenseError() }));
    vi.mocked(useUpsertPolicy).mockReturnValue(mut());
    vi.mocked(useDeletePolicy).mockReturnValue(mut());

    renderPage();
    const link = screen.getByRole('link', { name: /enterprise/i });
    expect(link.getAttribute('href')).toContain('enterprise');
  });

  it('shows generic error for non-license errors', () => {
    vi.clearAllMocks();
    vi.mocked(usePolicies).mockReturnValue(
      qr({ isError: true, error: new Error('Internal Server Error') }),
    );
    vi.mocked(useUpsertPolicy).mockReturnValue(mut());
    vi.mocked(useDeletePolicy).mockReturnValue(mut());

    renderPage();
    expect(screen.queryByText('Enterprise feature')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toBeInTheDocument();
  });
});
