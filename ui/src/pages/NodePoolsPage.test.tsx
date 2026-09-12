/**
 * NodePoolsPage — unit tests for the v0.7 edit + delete additions.
 *
 * Strategy: mock the hooks/queries layer and i18n so the component renders
 * synchronously with controlled data and no real HTTP calls. We verify:
 *   - pool edit uses the modal pattern and submits the changed fields;
 *   - pool delete is arm→confirm (first click arms, second confirms) and
 *     calls the delete hook with the pool id;
 *   - the existing create/list behaviour is untouched (create button present).
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, within, act } from '@testing-library/react';

vi.mock('../i18n', () => ({
  useT: () => (key: string, params?: Record<string, string>) =>
    params ? `${key}:${JSON.stringify(params)}` : key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

vi.mock('../hooks/queries', () => ({
  useNodePools: vi.fn(),
  useCreateNodePool: vi.fn(),
  usePoolNodes: vi.fn(),
  usePoolQuotas: vi.fn(),
  useAssignNodeToPool: vi.fn(),
  useRemoveNodeFromPool: vi.fn(),
  useUpdateNodePool: vi.fn(),
  useDeleteNodePool: vi.fn(),
}));

import { NodePoolsPage } from './NodePoolsPage';
import * as queries from '../hooks/queries';
import type { NodePool } from '../api/types';

const mq = queries as unknown as Record<string, ReturnType<typeof vi.fn>>;

function mkPool(overrides: Partial<NodePool> = {}): NodePool {
  return {
    id: 'pool-1',
    name: 'gpu-pool-a',
    description: 'primary',
    owner_type: 'platform',
    owner_id: 'platform',
    policy: 'shared',
    node_ids: [],
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

const idleMutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn().mockResolvedValue(undefined), isPending: false, isError: false, error: null });

beforeEach(() => {
  vi.clearAllMocks();
  mq.useNodePools.mockReturnValue({ data: { pools: [mkPool()] }, isLoading: false, isError: false, error: null, refetch: vi.fn() });
  mq.useCreateNodePool.mockReturnValue(idleMutation());
  mq.usePoolNodes.mockReturnValue({ data: { node_ids: [] }, isLoading: false });
  mq.usePoolQuotas.mockReturnValue({ data: { quotas: [] } });
  mq.useAssignNodeToPool.mockReturnValue(idleMutation());
  mq.useRemoveNodeFromPool.mockReturnValue(idleMutation());
  mq.useUpdateNodePool.mockReturnValue(idleMutation());
  mq.useDeleteNodePool.mockReturnValue(idleMutation());
});

describe('NodePoolsPage — edit pool', () => {
  it('opens the edit modal and submits the changed name via useUpdateNodePool', () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    mq.useUpdateNodePool.mockReturnValue({ ...idleMutation(), mutateAsync });

    render(<NodePoolsPage />);
    fireEvent.click(screen.getByText('platform.pools.edit'));

    const dialog = screen.getByRole('dialog');
    // The name input is prefilled with the current name.
    const input = within(dialog).getByDisplayValue('gpu-pool-a') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'gpu-pool-b' } });
    fireEvent.click(within(dialog).getByText('platform.pools.save'));

    expect(mutateAsync).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'pool-1', input: expect.objectContaining({ name: 'gpu-pool-b' }) }),
    );
  });
});

describe('NodePoolsPage — delete pool (arm→confirm)', () => {
  it('first click arms without deleting; second click confirms and calls useDeleteNodePool', async () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    mq.useDeleteNodePool.mockReturnValue({ ...idleMutation(), mutateAsync });

    render(<NodePoolsPage />);

    // First click arms — no mutation yet.
    fireEvent.click(screen.getByText('platform.pools.delete'));
    expect(mutateAsync).not.toHaveBeenCalled();

    // Armed state shows the confirm label; clicking it deletes.
    const confirmBtn = screen.getByText(/platform\.pools\.deleteConfirm/);
    await act(async () => {
      fireEvent.click(confirmBtn);
    });
    expect(mutateAsync).toHaveBeenCalledWith('pool-1');
  });
});

describe('NodePoolsPage — existing behaviour intact', () => {
  it('still renders the create pool action', () => {
    render(<NodePoolsPage />);
    expect(screen.getAllByText('platform.pools.createPool').length).toBeGreaterThan(0);
  });
});
