/**
 * FleetPage — overflow menu + confirm-dialog hardening tests (v0.6).
 *
 * Verifies:
 * 1. Row click expands the node detail panel (accordion).
 * 2. Row click again collapses it.
 * 3. The ⋮ overflow menu button opens the action dropdown.
 * 4. Drain → opens a Modal (NOT window.confirm).
 * 5. Drain modal Cancel → no mutation.
 * 6. Drain modal Confirm → drain.mutate called with node id.
 * 7. Remove → opens the custom Modal.
 * 8. Remove modal Cancel → no mutation.
 * 9. Remove modal Confirm → remove.mutate called with node id.
 */
import { render, screen, fireEvent, within } from '@testing-library/react';
import { FleetPage } from '../FleetPage';
import { I18nProvider } from '../../i18n';
import type { ReactNode } from 'react';
import type { NodeView } from '../../api/types';

// ---- shared mock state (vi.hoisted ensures access inside vi.mock factory) ---

const { drainMutate, removeMutate } = vi.hoisted(() => ({
  drainMutate: vi.fn(),
  removeMutate: vi.fn(),
}));

// ---- mock the entire hooks/queries module -----------------------------------

vi.mock('../../hooks/queries', () => ({
  useCapacity: () => ({ isLoading: false, isError: false, data: null }),
  useNodes: () => ({
    isLoading: false,
    isError: false,
    data: [mockNode()],
    refetch: vi.fn(),
  }),
  useMetricsStream: () => ({ snapshot: null, streamError: false }),
  useNodeAction: () => ({
    drain: { mutate: drainMutate, isPending: false },
    restart: { mutate: vi.fn(), isPending: false },
    remove: { mutate: removeMutate, isPending: false },
  }),
  useReconcilerStatus: () => ({ isLoading: false, isError: false, data: undefined }),
  useSloCompliance: () => ({ isLoading: false, isError: false, data: null }),
  useClusterStatus: () => ({ isLoading: false, isError: false, data: undefined }),
}));

// ---- helpers ----------------------------------------------------------------

function mockNode(): NodeView {
  return {
    profile: {
      nodeId: 'node-1',
      hostname: 'test-node',
      state: 'ready',
      os: 'linux',
      arch: 'x86_64',
      gpus: [],
      backends: ['cpu'],
      ramTotalGb: 32,
      ramAvailableGb: 16,
      memBandwidthGbs: 0,
      diskFreeGb: 100,
      engineVersions: {},
      lastSeen: '',
    },
    metrics: null,
    role: null,
    linkQuality: 'good',
    deploymentId: null,
  };
}

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nProvider>{children}</I18nProvider>;
}

// Helper: open the ⋮ overflow menu for the one node in the test.
function openOverflowMenu() {
  // The aria-label is "Actions node-1" — matches the button we added.
  const btn = screen.getByRole('button', { name: /actions/i });
  fireEvent.click(btn);
}

// ---- setup ------------------------------------------------------------------

beforeEach(() => {
  drainMutate.mockClear();
  removeMutate.mockClear();
  vi.restoreAllMocks();
});

// ---- row expand / collapse --------------------------------------------------

describe('FleetPage — row expand / collapse', () => {
  it('clicking_a_row_expands_the_node_detail_panel', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    // Detail panel not visible initially.
    expect(screen.queryByText('Node ID')).not.toBeInTheDocument();

    // Click the node-cell <th> (has aria-expanded).
    const rowHeader = screen.getByRole('rowheader', { name: /test-node/i });
    fireEvent.click(rowHeader);

    // Detail panel should now be visible.
    expect(screen.getByText('Node ID')).toBeInTheDocument();
  });

  it('clicking_expanded_row_again_collapses_the_detail_panel', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    const rowHeader = screen.getByRole('rowheader', { name: /test-node/i });
    fireEvent.click(rowHeader);
    expect(screen.getByText('Node ID')).toBeInTheDocument();

    // Second click collapses it.
    fireEvent.click(rowHeader);
    expect(screen.queryByText('Node ID')).not.toBeInTheDocument();
  });
});

// ---- overflow menu ----------------------------------------------------------

describe('FleetPage — overflow menu', () => {
  it('overflow_menu_opens_on_click', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    // Before click: dropdown items should not be visible.
    expect(screen.queryByRole('menuitem', { name: /drain/i })).not.toBeInTheDocument();

    openOverflowMenu();

    expect(screen.getByRole('menuitem', { name: /drain/i })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: /restart/i })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: /remove/i })).toBeInTheDocument();
  });
});

// ---- drain ------------------------------------------------------------------

describe('FleetPage — drain confirm modal', () => {
  it('drain_opens_modal_via_overflow_menu_without_mutating', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    openOverflowMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /drain/i }));

    // Modal must appear.
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('Drain node?')).toBeInTheDocument();

    // Mutation must NOT have fired yet.
    expect(drainMutate).not.toHaveBeenCalled();
  });

  it('drain_modal_cancel_closes_without_mutating', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    openOverflowMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /drain/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Cancel'));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(drainMutate).not.toHaveBeenCalled();
  });

  it('drain_modal_confirm_calls_mutate_with_node_id', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    openOverflowMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /drain/i }));

    const modal = screen.getByRole('dialog');
    // Click the "Drain" button inside the modal footer.
    fireEvent.click(within(modal).getByRole('button', { name: /drain/i }));

    expect(drainMutate).toHaveBeenCalledWith('node-1');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});

// ---- remove -----------------------------------------------------------------

describe('FleetPage — remove node modal', () => {
  it('remove_opens_modal_via_overflow_menu_without_mutating', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    openOverflowMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /remove/i }));

    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('Remove node from fleet?')).toBeInTheDocument();
    expect(removeMutate).not.toHaveBeenCalled();
  });

  it('remove_modal_cancel_closes_without_mutating', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    openOverflowMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /remove/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Cancel'));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(removeMutate).not.toHaveBeenCalled();
  });

  it('remove_modal_confirm_calls_mutate_with_node_id', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    openOverflowMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /remove/i }));

    const modal = screen.getByRole('dialog');
    fireEvent.click(within(modal).getByRole('button', { name: /remove/i }));

    expect(removeMutate).toHaveBeenCalledWith('node-1');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
