// ClusterStatusCard tests — HA / Raft topology panel (v0.6).
//
// The card consumes useClusterStatus() internally, so we mock the hooks module.
// Covers:
//   - raft mode: leader address + state are rendered, peer count shown;
//   - single-node (standalone) mode: is_leader true, no peers — rendered as a
//     graceful "standalone / no HA" state (NOT an error);
//   - loading and error/unknown states.
import { render, screen } from '@testing-library/react';
import type { ReactElement } from 'react';
import { I18nProvider } from '../../i18n';

vi.mock('../../hooks/queries', () => ({
  useClusterStatus: vi.fn(),
}));

import { useClusterStatus } from '../../hooks/queries';
import { ClusterStatusCard } from '../FleetPage';
import type { ClusterStatus } from '../../api/types';

function wrap(ui: ReactElement) {
  return render(<I18nProvider>{ui}</I18nProvider>);
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function qr(overrides: Record<string, unknown> = {}): any {
  return { data: undefined, isLoading: false, isError: false, error: null, refetch: vi.fn(), ...overrides };
}

const RAFT_STATUS: ClusterStatus = {
  mode: 'raft',
  isLeader: true,
  leader: '10.0.0.1:7000',
  state: 'Leader',
  stats: { numPeers: '2', term: '7', commitIndex: '4211' },
};

const STANDALONE_STATUS: ClusterStatus = {
  mode: 'standalone',
  isLeader: true,
};

beforeEach(() => {
  vi.clearAllMocks();
});

describe('ClusterStatusCard — raft mode', () => {
  it('renders the leader address and raft state', () => {
    vi.mocked(useClusterStatus).mockReturnValue(qr({ data: RAFT_STATUS }));
    wrap(<ClusterStatusCard />);
    expect(screen.getByText('Leader')).toBeInTheDocument();
    expect(screen.getByText('10.0.0.1:7000')).toBeInTheDocument();
  });

  it('surfaces the peer count from raft stats', () => {
    vi.mocked(useClusterStatus).mockReturnValue(qr({ data: RAFT_STATUS }));
    const { container } = wrap(<ClusterStatusCard />);
    // num_peers (2) + this leader = 3 total members is shown somewhere.
    expect(container.textContent).toMatch(/2/);
  });
});

describe('ClusterStatusCard — single-node', () => {
  it('renders standalone mode gracefully (no HA, not an error)', () => {
    vi.mocked(useClusterStatus).mockReturnValue(qr({ data: STANDALONE_STATUS }));
    const { container } = wrap(<ClusterStatusCard />);
    // No error/alert — a normal informational state.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(container.textContent?.toLowerCase()).toContain('standalone');
  });
});

describe('ClusterStatusCard — loading & unknown', () => {
  it('renders nothing crashy while loading', () => {
    vi.mocked(useClusterStatus).mockReturnValue(qr({ isLoading: true }));
    wrap(<ClusterStatusCard />);
    expect(screen.getByRole('status')).toBeInTheDocument();
  });

  it('shows an unknown badge when the endpoint errors', () => {
    vi.mocked(useClusterStatus).mockReturnValue(qr({ isError: true, error: new Error('nope') }));
    wrap(<ClusterStatusCard />);
    expect(screen.getByText(/unknown/i)).toBeInTheDocument();
  });
});
