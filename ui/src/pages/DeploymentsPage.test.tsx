// ---------------------------------------------------------------------------
// TDD tests for DeploymentHealthBadge (sub-component of DeploymentsPage).
// Each test mocks useModelHealth to verify the correct Badge tone and label.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DeploymentHealthBadge } from './DeploymentsPage';

// Mock the queries module so we control what useModelHealth returns.
vi.mock('../hooks/queries', () => ({
  useModelHealth: vi.fn(),
  // Stub the rest so imports that resolve the module don't fail.
  useDeployments: vi.fn(() => ({ data: [], isLoading: false, isError: false })),
  useNodes: vi.fn(() => ({ data: [] })),
  useUndeploy: vi.fn(() => ({ mutate: vi.fn(), isPending: false })),
}));

// TS helper: typed access to the mocked function.
import { useModelHealth } from '../hooks/queries';
const mockedUseModelHealth = vi.mocked(useModelHealth);

describe('DeploymentHealthBadge', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders_healthy_badge_when_model_healthy', () => {
    mockedUseModelHealth.mockReturnValue({
      data: {
        modelId: 'llama-8b',
        status: 'healthy',
        deploymentId: 'dep-1',
        deploymentState: 'active',
        nodeCount: 2,
      },
      isLoading: false,
      isError: false,
      error: null,
      isPending: false,
      isSuccess: true,
    } as ReturnType<typeof useModelHealth>);

    render(<DeploymentHealthBadge modelId="llama-8b" />);

    expect(screen.getByText('Healthy')).toBeInTheDocument();
  });

  it('renders_degraded_badge_when_model_degraded', () => {
    mockedUseModelHealth.mockReturnValue({
      data: {
        modelId: 'llama-8b',
        status: 'degraded',
        deploymentId: 'dep-1',
        deploymentState: 'provisioning',
        nodeCount: 2,
      },
      isLoading: false,
      isError: false,
      error: null,
      isPending: false,
      isSuccess: true,
    } as ReturnType<typeof useModelHealth>);

    render(<DeploymentHealthBadge modelId="llama-8b" />);

    expect(screen.getByText('Degraded')).toBeInTheDocument();
  });

  it('renders_unavailable_when_no_deployment', () => {
    mockedUseModelHealth.mockReturnValue({
      data: {
        modelId: 'llama-8b',
        status: 'unavailable',
        deploymentId: '',
        deploymentState: '',
        nodeCount: 0,
        errorMessage: 'no deployment found for this model',
      },
      isLoading: false,
      isError: false,
      error: null,
      isPending: false,
      isSuccess: true,
    } as ReturnType<typeof useModelHealth>);

    render(<DeploymentHealthBadge modelId="llama-8b" />);

    expect(screen.getByText('Unavailable')).toBeInTheDocument();
  });

  it('renders_neutral_when_loading', () => {
    mockedUseModelHealth.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      error: null,
      isPending: true,
      isSuccess: false,
    } as ReturnType<typeof useModelHealth>);

    render(<DeploymentHealthBadge modelId="llama-8b" />);

    // Loading state shows a neutral placeholder.
    expect(screen.getByText('—')).toBeInTheDocument();
  });
});
