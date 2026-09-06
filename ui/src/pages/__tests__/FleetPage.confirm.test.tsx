/**
 * FleetPage — confirm-dialog hardening tests (H11, M3).
 *
 * Verifies:
 * 1. Drain button shows window.confirm before calling the mutation.
 * 2. Drain mutation is NOT called when the user cancels.
 * 3. Drain mutation IS called when the user confirms.
 * 4. Remove button opens the custom Modal instead of mutating immediately.
 * 5. Modal confirm calls remove.mutate with the correct node id.
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

// ---- setup ------------------------------------------------------------------

beforeEach(() => {
  drainMutate.mockClear();
  removeMutate.mockClear();
  vi.restoreAllMocks();
});

// ---- tests ------------------------------------------------------------------

describe('FleetPage — drain confirm', () => {
  it('drain_node_shows_confirm_before_mutating', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    render(<FleetPage />, { wrapper: Wrapper });

    fireEvent.click(screen.getByText('Drain'));

    expect(window.confirm).toHaveBeenCalledOnce();
    expect(drainMutate).not.toHaveBeenCalled();
  });

  it('drain_node_calls_mutate_when_user_confirms', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<FleetPage />, { wrapper: Wrapper });

    fireEvent.click(screen.getByText('Drain'));

    expect(window.confirm).toHaveBeenCalledOnce();
    expect(drainMutate).toHaveBeenCalledWith('node-1');
  });
});

describe('FleetPage — remove node modal', () => {
  it('remove_node_opens_modal_without_mutating', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    // Click the remove button in the table row
    const allRemoveButtons = screen.getAllByText('Remove');
    fireEvent.click(allRemoveButtons[0]);

    // Modal should be visible
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('Remove node from fleet?')).toBeInTheDocument();

    // Mutation must NOT have fired yet
    expect(removeMutate).not.toHaveBeenCalled();
  });

  it('remove_node_cancel_closes_modal_without_mutating', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    const allRemoveButtons = screen.getAllByText('Remove');
    fireEvent.click(allRemoveButtons[0]);
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Cancel'));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(removeMutate).not.toHaveBeenCalled();
  });

  it('remove_node_confirm_calls_mutate_with_node_id', () => {
    render(<FleetPage />, { wrapper: Wrapper });

    const allRemoveButtons = screen.getAllByText('Remove');
    fireEvent.click(allRemoveButtons[0]);

    const modal = screen.getByRole('dialog');
    fireEvent.click(within(modal).getByText('Remove'));

    expect(removeMutate).toHaveBeenCalledWith('node-1');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
