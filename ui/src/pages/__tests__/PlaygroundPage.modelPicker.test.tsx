/**
 * PlaygroundPage — model picker de-duplication regression tests.
 *
 * Regression: the control plane allows several ACTIVE deployments of the SAME
 * model (deploying a model N times is how you scale throughput — the Gateway
 * load-balances across the replicas behind ONE route keyed by model id). When
 * the Gateway was unreachable, `GET /v1/models` failed and the Playground fell
 * back to the models of the active deployments WITHOUT de-duplicating, so a
 * cluster running three ACTIVE `tinyllama-1b` deployments rendered three
 * identical options in the picker.
 *
 * The picker lists MODELS, not deployments: one model = one option.
 *
 * Verifies:
 * (a) the exact regression — 3 ACTIVE deployments of one model => 1 option;
 * (b) distinct models are all listed, in first-seen order, stable across render;
 * (c) a non-ACTIVE deployment's model is not listed;
 * (d) the Gateway's served list wins when present, and is de-duplicated too;
 * (e) empty case — no active deployments + Gateway unreachable => DEFAULT_MODEL
 *     is offered and the "no active deployment" notice is shown.
 *
 * Assertions read the real `<select>` the operator sees (its `<option>`s).
 */
import { describe, it, expect, vi, beforeAll, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../../i18n';
import { PlaygroundPage } from '../PlaygroundPage';
import type { Deployment, DeploymentState, OpenAIModel } from '../../api/types';

// ---------------------------------------------------------------------------
// Mocks
//
// `../../api/client` has a top-level await (it code-splits the opt-in mock
// fixtures), so it must be replaced with a factory — see CatalogPage.test.tsx.
// `../../hooks/queries` is the cleanest seam for the data the picker reads.
// ---------------------------------------------------------------------------

vi.mock('../../api/client', () => ({
  makeChat: vi.fn(() => ({
    baseUrl: '/v1',
    streamChat: vi.fn(),
    listModels: vi.fn(async () => []),
  })),
}));

vi.mock('../../hooks/queries', () => ({
  useDeployments: vi.fn(),
  useGatewayModels: vi.fn(),
}));

import { useDeployments, useGatewayModels } from '../../hooks/queries';

const mockUseDeployments = useDeployments as ReturnType<typeof vi.fn>;
const mockUseGatewayModels = useGatewayModels as ReturnType<typeof vi.fn>;

// ---------------------------------------------------------------------------
// Fixtures / helpers
// ---------------------------------------------------------------------------

/** Mirrors DEFAULT_MODEL in PlaygroundPage.tsx. */
const DEFAULT_MODEL = 'qwen3-moe-235b';

function deployment(id: string, modelId: string, state: DeploymentState = 'active'): Deployment {
  return {
    id,
    state,
    createdAt: '2026-09-12T00:00:00Z',
    nodeStatus: [],
    plan: {
      planId: `plan-${id}`,
      modelId,
      quantization: 'q4_k_m',
      assignments: [],
      pipelineOrder: [],
      estimated: {
        decodeTokSMin: 0,
        decodeTokSMax: 0,
        prefillTokSMin: 0,
        prefillTokSMax: 0,
        headroomGb: 0,
      },
      cost: 0,
      explanation: [],
    },
  };
}

function served(ids: string[]): OpenAIModel[] {
  return ids.map((id) => ({ id, object: 'model' as const, ownedBy: 'purser' }));
}

/** `GET /v1/models` succeeded and returned this list. */
function mockGatewayServed(ids: string[]) {
  mockUseGatewayModels.mockReturnValue({
    data: served(ids),
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  });
}

/** `GET /v1/models` failed / the Gateway is unreachable. */
function mockGatewayUnreachable() {
  mockUseGatewayModels.mockReturnValue({
    data: undefined,
    isLoading: false,
    isError: true,
    error: new Error('Gateway unreachable'),
    refetch: vi.fn(),
  });
}

function mockDeployments(data: Deployment[]) {
  mockUseDeployments.mockReturnValue({
    data,
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  });
}

function tree() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <I18nProvider>
          <PlaygroundPage />
        </I18nProvider>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function renderPage() {
  return render(tree());
}

/** The option values the operator actually sees in the model `<select>`. */
function modelOptions(container: HTMLElement): string[] {
  const select = container.querySelector('select');
  if (!select) throw new Error('model <select> not found');
  return Array.from(select.options).map((o) => o.value);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('PlaygroundPage model picker', () => {
  beforeAll(() => {
    // jsdom does not implement Element.prototype.scrollTo, which the chat log
    // effect calls on every message change.
    Element.prototype.scrollTo = vi.fn();
  });

  beforeEach(() => {
    mockUseDeployments.mockReset();
    mockUseGatewayModels.mockReset();
  });

  it('(a) collapses three ACTIVE deployments of the SAME model into ONE option', () => {
    // The exact live-cluster regression: 3 x ACTIVE tinyllama-1b, Gateway down.
    mockGatewayUnreachable();
    mockDeployments([
      deployment('dep-1', 'tinyllama-1b'),
      deployment('dep-2', 'tinyllama-1b'),
      deployment('dep-3', 'tinyllama-1b'),
    ]);

    const { container } = renderPage();

    expect(modelOptions(container)).toEqual(['tinyllama-1b']);
  });

  it('(b) lists distinct models in first-seen order, stable across re-render', () => {
    mockGatewayUnreachable();
    mockDeployments([
      deployment('dep-1', 'zeta-70b'),
      deployment('dep-2', 'alpha-8b'),
      deployment('dep-3', 'zeta-70b'),
      deployment('dep-4', 'mid-13b'),
    ]);

    const { container, rerender } = renderPage();
    expect(modelOptions(container)).toEqual(['zeta-70b', 'alpha-8b', 'mid-13b']);

    // Re-render (e.g. a poll tick) must not reorder the picker.
    rerender(tree());
    expect(modelOptions(container)).toEqual(['zeta-70b', 'alpha-8b', 'mid-13b']);
  });

  it('(c) does not list a model whose only deployment is not ACTIVE', () => {
    mockGatewayUnreachable();
    mockDeployments([
      deployment('dep-1', 'live-model', 'active'),
      deployment('dep-2', 'stopped-model', 'stopping'),
      deployment('dep-3', 'planned-model', 'planned'),
    ]);

    const { container } = renderPage();

    expect(modelOptions(container)).toEqual(['live-model']);
  });

  it('(d) prefers the Gateway served list, de-duplicated', () => {
    mockGatewayServed(['served-a', 'served-a', 'served-b']);
    mockDeployments([deployment('dep-1', 'deployment-only-model')]);

    const { container } = renderPage();

    expect(modelOptions(container)).toEqual(['served-a', 'served-b']);
  });

  it('(e) falls back to DEFAULT_MODEL and shows the notice when nothing is available', () => {
    mockGatewayUnreachable();
    mockDeployments([]);

    const { container } = renderPage();

    expect(modelOptions(container)).toEqual([DEFAULT_MODEL]);
    expect(screen.getByRole('note')).toBeInTheDocument();
  });
});
