/**
 * DataPlanesPage — unit tests.
 *
 * Strategy: mock the hooks layer and i18n so no real API calls are made.
 * We verify: table columns, tier badge encoding, status pill, expand/collapse,
 * register modal flow, join-token shown-once, and config refresh.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, act } from '@testing-library/react';
import { DataPlanesPage } from './DataPlanesPage';

vi.mock('../i18n', () => ({
  useT: () => (key: string) => key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

vi.mock('../hooks/queries', () => ({
  useDataPlanes: vi.fn(),
  useCreateDataPlane: vi.fn(),
  useRefreshDataPlaneConfig: vi.fn(),
  useUpdateDataPlane: vi.fn(),
  useDeleteDataPlane: vi.fn(),
  useDataPlaneNodes: vi.fn(),
  useAssignNodeToDataPlane: vi.fn(),
  useUnassignNodeFromDataPlane: vi.fn(),
}));

import * as queries from '../hooks/queries';

const mq = queries as unknown as {
  useDataPlanes: ReturnType<typeof vi.fn>;
  useCreateDataPlane: ReturnType<typeof vi.fn>;
  useRefreshDataPlaneConfig: ReturnType<typeof vi.fn>;
  useUpdateDataPlane: ReturnType<typeof vi.fn>;
  useDeleteDataPlane: ReturnType<typeof vi.fn>;
  useDataPlaneNodes: ReturnType<typeof vi.fn>;
  useAssignNodeToDataPlane: ReturnType<typeof vi.fn>;
  useUnassignNodeFromDataPlane: ReturnType<typeof vi.fn>;
};

function success<T>(data: T) {
  return { isLoading: false, isError: false, error: null, data, refetch: vi.fn() };
}
function loading() {
  return { isLoading: true, isError: false, error: null, data: undefined, refetch: vi.fn() };
}

const mutationStub = { mutate: vi.fn(), mutateAsync: vi.fn().mockResolvedValue(undefined), isPending: false, isError: false, error: null };

function mkDp(overrides: Partial<import('../api/types').DataPlane> = {}): import('../api/types').DataPlane {
  return {
    id: 'dp-test-01',
    name: 'test-cluster',
    tier: 'production',
    gatewayUrl: 'https://gpu.test.com',
    status: 'active',
    configSnapshot: { routingTable: { 'llama3': {} }, authBundle: { key: {} }, policyBundle: [] },
    lastHeartbeat: new Date(Date.now() - 60_000).toISOString(),
    nodeCount: 4,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mq.useDataPlanes.mockReturnValue(success([]));
  mq.useCreateDataPlane.mockReturnValue(mutationStub);
  mq.useRefreshDataPlaneConfig.mockReturnValue(mutationStub);
  mq.useUpdateDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync: vi.fn().mockResolvedValue(undefined), isPending: false, isError: false, error: null });
  mq.useDeleteDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync: vi.fn().mockResolvedValue(undefined), isPending: false, isError: false, error: null });
  mq.useDataPlaneNodes.mockReturnValue({ data: [], isLoading: false, isError: false, error: null, refetch: vi.fn() });
  mq.useAssignNodeToDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync: vi.fn().mockResolvedValue(undefined), isPending: false, isError: false, error: null });
  mq.useUnassignNodeFromDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync: vi.fn().mockResolvedValue(undefined), isPending: false, isError: false, error: null });
});

// ---------------------------------------------------------------------------
// Table rendering
// ---------------------------------------------------------------------------

describe('DataPlanesPage — table', () => {
  it('shows empty state when no data planes', () => {
    mq.useDataPlanes.mockReturnValue(success([]));
    render(<DataPlanesPage />);
    expect(screen.getByText(/no data planes registered/i)).toBeDefined();
  });

  it('renders table with name, tier, status, gateway, heartbeat, nodes columns', () => {
    mq.useDataPlanes.mockReturnValue(success([mkDp()]));
    render(<DataPlanesPage />);
    expect(screen.getByText('test-cluster')).toBeDefined();
    expect(screen.getByText('https://gpu.test.com')).toBeDefined();
    // node count
    expect(screen.getByText('4')).toBeDefined();
  });

  it('renders tier badge correctly for production', () => {
    mq.useDataPlanes.mockReturnValue(success([mkDp({ tier: 'production' })]));
    const { container } = render(<DataPlanesPage />);
    const badges = container.querySelectorAll('[data-testid="tier-badge"]');
    expect(badges.length).toBeGreaterThan(0);
    expect(badges[0].textContent).toBe('production');
  });

  it('renders tier badge for staging', () => {
    mq.useDataPlanes.mockReturnValue(success([mkDp({ tier: 'staging' })]));
    const { container } = render(<DataPlanesPage />);
    const badges = container.querySelectorAll('[data-testid="tier-badge"]');
    expect(badges[0].textContent).toBe('staging');
  });

  it('shows status pill for active DP', () => {
    mq.useDataPlanes.mockReturnValue(success([mkDp({ status: 'active' })]));
    const { container } = render(<DataPlanesPage />);
    const pills = container.querySelectorAll('[data-testid="dp-status-pill"]');
    expect(pills.length).toBeGreaterThan(0);
    expect(pills[0].textContent).toContain('active');
  });

  it('shows status pill for degraded DP', () => {
    mq.useDataPlanes.mockReturnValue(success([mkDp({ status: 'degraded' })]));
    const { container } = render(<DataPlanesPage />);
    const pills = container.querySelectorAll('[data-testid="dp-status-pill"]');
    expect(pills[0].textContent).toContain('degraded');
  });

  it('shows "never" when lastHeartbeat is null', () => {
    mq.useDataPlanes.mockReturnValue(success([mkDp({ lastHeartbeat: null })]));
    render(<DataPlanesPage />);
    expect(screen.getByText('never')).toBeDefined();
  });

  it('shows loading state', () => {
    mq.useDataPlanes.mockReturnValue(loading());
    render(<DataPlanesPage />);
    // LoadingBlock renders a spinner — just verify no crash and no table
    expect(screen.queryByRole('table')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Register modal
// ---------------------------------------------------------------------------

describe('DataPlanesPage — register modal', () => {
  it('opens register modal on button click', () => {
    render(<DataPlanesPage />);
    fireEvent.click(screen.getByTestId('register-dp-btn'));
    // Modal is open when the name input is present
    expect(screen.getByTestId('dp-name-input')).toBeDefined();
  });

  it('submit button is disabled when name is empty', () => {
    render(<DataPlanesPage />);
    fireEvent.click(screen.getByTestId('register-dp-btn'));
    const btn = screen.getByTestId('register-dp-submit');
    expect(btn).toBeDisabled();
  });

  it('calls createDataPlane with correct values on submit', () => {
    const mutate = vi.fn();
    mq.useCreateDataPlane.mockReturnValue({ mutate, isPending: false });
    render(<DataPlanesPage />);
    fireEvent.click(screen.getByTestId('register-dp-btn'));
    fireEvent.change(screen.getByTestId('dp-name-input'), { target: { value: 'new-cluster' } });
    fireEvent.change(screen.getByTestId('dp-gateway-input'), { target: { value: 'https://new.example.com' } });
    fireEvent.click(screen.getByTestId('register-dp-submit'));
    expect(mutate).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'new-cluster', gatewayUrl: 'https://new.example.com' }),
      expect.any(Object),
    );
  });
});

// ---------------------------------------------------------------------------
// Join token shown-once
// ---------------------------------------------------------------------------

describe('DataPlanesPage — join token', () => {
  it('shows join token modal after successful creation', async () => {
    const mockToken = 'dp_abc123secret';
    let successCallback: ((r: import('../api/types').DataPlaneWithToken) => void) | undefined;
    const mutate = vi.fn((_input: unknown, opts: { onSuccess?: (r: import('../api/types').DataPlaneWithToken) => void }) => {
      successCallback = opts.onSuccess;
    });
    mq.useCreateDataPlane.mockReturnValue({ mutate, isPending: false });

    render(<DataPlanesPage />);
    fireEvent.click(screen.getByTestId('register-dp-btn'));
    fireEvent.change(screen.getByTestId('dp-name-input'), { target: { value: 'x' } });
    fireEvent.click(screen.getByTestId('register-dp-submit'));

    await act(async () => {
      successCallback?.({
        dataplane: mkDp({ name: 'x' }),
        joinToken: mockToken,
      });
    });

    expect(screen.getByTestId('join-token-warning')).toBeDefined();
    expect(screen.getByTestId('join-token-value').textContent).toBe(mockToken);
  });

  it('shows warning text in join token modal', async () => {
    const mockToken = 'dp_xyz';
    let successCallback: ((r: import('../api/types').DataPlaneWithToken) => void) | undefined;
    const mutate = vi.fn((_input: unknown, opts: { onSuccess?: (r: import('../api/types').DataPlaneWithToken) => void }) => {
      successCallback = opts.onSuccess;
    });
    mq.useCreateDataPlane.mockReturnValue({ mutate, isPending: false });

    render(<DataPlanesPage />);
    fireEvent.click(screen.getByTestId('register-dp-btn'));
    fireEvent.change(screen.getByTestId('dp-name-input'), { target: { value: 'y' } });
    fireEvent.click(screen.getByTestId('register-dp-submit'));

    await act(async () => {
      successCallback?.({ dataplane: mkDp({ name: 'y' }), joinToken: mockToken });
    });

    expect(screen.getByText(/will not be shown again/i)).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// Config refresh
// ---------------------------------------------------------------------------

describe('DataPlanesPage — config refresh', () => {
  it('refresh button calls correct API', () => {
    const mutate = vi.fn();
    mq.useRefreshDataPlaneConfig.mockReturnValue({ mutate, isPending: false });
    mq.useDataPlanes.mockReturnValue(success([mkDp()]));

    render(<DataPlanesPage />);
    // Expand the row first
    fireEvent.click(screen.getByText('test-cluster'));
    // Click refresh
    fireEvent.click(screen.getByTestId('refresh-config-btn'));
    expect(mutate).toHaveBeenCalledWith('dp-test-01', expect.any(Object));
  });
});

// ---------------------------------------------------------------------------
// Edit DP (modal pattern)
// ---------------------------------------------------------------------------

describe('DataPlanesPage — edit', () => {
  it('opens edit modal and submits changed fields via useUpdateDataPlane', async () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    mq.useUpdateDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync, isPending: false, isError: false, error: null });
    mq.useDataPlanes.mockReturnValue(success([mkDp()]));

    render(<DataPlanesPage />);
    fireEvent.click(screen.getByText('test-cluster'));
    fireEvent.click(screen.getByTestId('edit-dp-btn'));

    const nameInput = screen.getByTestId('edit-dp-name') as HTMLInputElement;
    expect(nameInput.value).toBe('test-cluster');
    fireEvent.change(nameInput, { target: { value: 'renamed-cluster' } });

    await act(async () => {
      fireEvent.click(screen.getByTestId('edit-dp-submit'));
    });

    expect(mutateAsync).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'dp-test-01', input: expect.objectContaining({ name: 'renamed-cluster' }) }),
    );
  });
});

// ---------------------------------------------------------------------------
// Delete DP (arm→confirm)
// ---------------------------------------------------------------------------

describe('DataPlanesPage — delete (arm→confirm)', () => {
  it('first click arms; second click confirms and calls useDeleteDataPlane', async () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    mq.useDeleteDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync, isPending: false, isError: false, error: null });
    mq.useDataPlanes.mockReturnValue(success([mkDp()]));

    render(<DataPlanesPage />);
    fireEvent.click(screen.getByText('test-cluster'));

    fireEvent.click(screen.getByTestId('delete-dp-btn'));
    expect(mutateAsync).not.toHaveBeenCalled();

    // Armed → confirm
    await act(async () => {
      fireEvent.click(screen.getByTestId('delete-dp-confirm'));
    });
    expect(mutateAsync).toHaveBeenCalledWith('dp-test-01');
  });
});

// ---------------------------------------------------------------------------
// Node assign / unassign
// ---------------------------------------------------------------------------

describe('DataPlanesPage — node assignment', () => {
  it('assign node calls useAssignNodeToDataPlane with dp id and node id', async () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    mq.useAssignNodeToDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync, isPending: false, isError: false, error: null });
    mq.useDataPlanes.mockReturnValue(success([mkDp()]));

    render(<DataPlanesPage />);
    fireEvent.click(screen.getByText('test-cluster'));

    fireEvent.change(screen.getByTestId('assign-node-input'), { target: { value: 'node-9' } });
    await act(async () => {
      fireEvent.click(screen.getByTestId('assign-node-btn'));
    });

    expect(mutateAsync).toHaveBeenCalledWith({ id: 'dp-test-01', nodeId: 'node-9' });
  });

  it('renders assigned nodes and unassign is confirm-first', async () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    mq.useUnassignNodeFromDataPlane.mockReturnValue({ mutate: vi.fn(), mutateAsync, isPending: false, isError: false, error: null });
    mq.useDataPlaneNodes.mockReturnValue(success([{ id: 'node-1', hostname: 'gpu-1', state: 'ready' }]));
    mq.useDataPlanes.mockReturnValue(success([mkDp()]));

    render(<DataPlanesPage />);
    fireEvent.click(screen.getByText('test-cluster'));

    // The assigned node is listed.
    expect(screen.getByText('gpu-1')).toBeDefined();

    // Arm then confirm.
    fireEvent.click(screen.getByTestId('unassign-node-node-1'));
    expect(mutateAsync).not.toHaveBeenCalled();
    await act(async () => {
      fireEvent.click(screen.getByTestId('unassign-node-confirm-node-1'));
    });
    expect(mutateAsync).toHaveBeenCalledWith({ id: 'dp-test-01', nodeId: 'node-1' });
  });
});
