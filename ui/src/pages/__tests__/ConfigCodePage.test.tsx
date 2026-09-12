// ConfigCodePage tests — config-as-code viewer/diff/apply (v0.6).
//
// Covers:
//   (a) the exported config is rendered read-only from a mocked useConfigExport;
//   (b) the Diff action submits the editor content to the diff mutation and
//       renders the returned diff summary;
//   (c) Apply is confirm-first — a first click arms a confirmation, and the
//       apply mutation only fires after an explicit confirm;
//   (d) an error/empty path (export error surfaces an actionable error state).
import { render, screen, fireEvent, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../../i18n';
import { ConfigCodePage } from '../ConfigCodePage';
import type { ConfigDiff, ConfigApplyResult } from '../../api/types';

// ---------------------------------------------------------------------------
// Mock hooks
// ---------------------------------------------------------------------------

vi.mock('../../hooks/queries', () => ({
  useConfigExport: vi.fn(),
  useConfigDiff: vi.fn(),
  useConfigApply: vi.fn(),
}));

import { useConfigExport, useConfigDiff, useConfigApply } from '../../hooks/queries';

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const EXPORTED_YAML = `apiVersion: purser/v1
kind: ClusterConfig
cluster:
  id: demo-cluster
models:
  - id: llama3-8b
deployments:
  - model: llama3-8b
`;

const DIFF_RESULT: ConfigDiff = {
  modelsToAdd: [{ id: 'qwen3-235b' }],
  modelsToRemove: [],
  deploymentsToAdd: [{ model: 'llama3-70b' }],
  deploymentsToRemove: ['old-dep'],
  quotasToUpsert: [],
};

const APPLY_RESULT: ConfigApplyResult = {
  modelsAdded: 2,
  deploymentsAdded: 0,
  quotasUpserted: 0,
  orgsAdded: 0,
  nodePoolsAdded: 0,
  slosUpserted: 0,
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
    data: undefined,
    reset: vi.fn(),
    ...overrides,
  };
}

function mockAll(
  opts: {
    exportQr?: ReturnType<typeof qr>;
    diff?: ReturnType<typeof mut>;
    apply?: ReturnType<typeof mut>;
  } = {},
) {
  vi.mocked(useConfigExport).mockReturnValue(opts.exportQr ?? qr({ data: EXPORTED_YAML }));
  vi.mocked(useConfigDiff).mockReturnValue(opts.diff ?? mut());
  vi.mocked(useConfigApply).mockReturnValue(opts.apply ?? mut());
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/config']}>
        <I18nProvider>
          <ConfigCodePage />
        </I18nProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ---------------------------------------------------------------------------
// (a) exported config viewer
// ---------------------------------------------------------------------------

describe('ConfigCodePage — export viewer', () => {
  it('renders the exported config in a read-only panel', () => {
    mockAll();
    renderPage();
    // The exported YAML content is shown somewhere on the page.
    expect(screen.getByText(/demo-cluster/)).toBeInTheDocument();
    expect(screen.getByText(/ClusterConfig/)).toBeInTheDocument();
  });

  it('shows a loading indicator while the export is in flight', () => {
    mockAll({ exportQr: qr({ isLoading: true }) });
    renderPage();
    expect(screen.getByRole('status')).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// (b) diff action
// ---------------------------------------------------------------------------

describe('ConfigCodePage — diff', () => {
  it('submits the editor content to the diff mutation', () => {
    const diffMutate = vi.fn();
    mockAll({ diff: mut({ mutate: diffMutate }) });
    renderPage();

    const editor = screen.getByLabelText(/configuration to check/i);
    fireEvent.change(editor, { target: { value: 'apiVersion: purser/v1\nkind: ClusterConfig\n' } });

    fireEvent.click(screen.getByRole('button', { name: /^diff$/i }));

    expect(diffMutate).toHaveBeenCalledTimes(1);
    expect(diffMutate.mock.calls[0][0]).toContain('ClusterConfig');
  });

  it('renders the returned diff summary', () => {
    mockAll({ diff: mut({ data: DIFF_RESULT }) });
    renderPage();
    // The diff result region shows the models-to-add count and the removed deployment id.
    const region = screen.getByTestId('config-diff-result');
    expect(within(region).getByText(/qwen3-235b/)).toBeInTheDocument();
    expect(within(region).getByText(/old-dep/)).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// (c) apply is confirm-first
// ---------------------------------------------------------------------------

describe('ConfigCodePage — apply (confirm-first)', () => {
  it('does not apply on the first click and requires an explicit confirm', () => {
    const applyMutate = vi.fn();
    mockAll({ apply: mut({ mutate: applyMutate }) });
    renderPage();

    const editor = screen.getByLabelText(/configuration to check/i);
    fireEvent.change(editor, { target: { value: EXPORTED_YAML } });

    // First click arms the confirmation — must NOT mutate.
    fireEvent.click(screen.getByRole('button', { name: /apply configuration/i }));
    expect(applyMutate).not.toHaveBeenCalled();

    // A confirm control appears (in the dialog); clicking it fires the apply.
    const dialog = screen.getByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /confirm apply/i }));
    expect(applyMutate).toHaveBeenCalledTimes(1);
    expect(applyMutate.mock.calls[0][0]).toContain('ClusterConfig');
  });

  it('renders the apply result summary after a successful apply', () => {
    mockAll({ apply: mut({ data: APPLY_RESULT }) });
    renderPage();
    const region = screen.getByTestId('config-apply-result');
    // Models added count (2) is surfaced.
    expect(within(region).getByText('2')).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// (d) error path
// ---------------------------------------------------------------------------

describe('ConfigCodePage — error path', () => {
  it('shows an actionable error when the export fails', () => {
    mockAll({ exportQr: qr({ isError: true, error: new Error('boom') }) });
    renderPage();
    expect(screen.getByRole('alert')).toBeInTheDocument();
  });
});
