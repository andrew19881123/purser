// ---------------------------------------------------------------------------
// CatalogPage tests — delete model + preview fleet split
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { CatalogPage } from './CatalogPage';
import { ApiError } from '../api/http';
import type { CatalogEntry, PlanPreviewResult } from '../api/types';

// ---------------------------------------------------------------------------
// Mock the api/client module (has top-level await — must be fully replaced)
// ---------------------------------------------------------------------------

vi.mock('../api/client', () => ({
  api: {
    getCatalog: vi.fn(),
    deleteModel: vi.fn(),
    previewModelPlan: vi.fn(),
    // Unused by CatalogPage but required for PurserApi completeness at runtime:
    getCapacity: vi.fn(),
    listNodes: vi.fn(),
    getNode: vi.fn(),
    drainNode: vi.fn(),
    restartNode: vi.fn(),
    removeNode: vi.fn(),
    getModel: vi.fn(),
    importModel: vi.fn(),
    planDeployment: vi.fn(),
    createDeployment: vi.fn(),
    listDeployments: vi.fn(),
    getDeployment: vi.fn(),
    undeployDeployment: vi.fn(),
    getPlan: vi.fn(),
    getJoinInfo: vi.fn(),
    rotateJoinToken: vi.fn(),
    createJoinToken: vi.fn(),
    listApiKeys: vi.fn(),
    createApiKey: vi.fn(),
    revokeApiKey: vi.fn(),
    streamMetrics: vi.fn(() => () => {}),
  },
}));

// Import AFTER vi.mock so we get the mocked version
import { api } from '../api/client';

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const feasibleEntry: CatalogEntry = {
  model: {
    modelId: 'llama-8b',
    family: 'Llama 3.1 8B',
    architecture: 'LlamaForCausalLM',
    paramsTotalB: 8,
    paramsActiveB: 8,
    layers: 32,
    hiddenSize: 4096,
    nKvHeads: 8,
    headDim: 128,
    attentionType: 'gqa',
    contextMax: 8192,
    isMoe: false,
    draft: { available: false, type: '', tailLayers: 0 },
    quantizations: [
      { name: 'Q4_K_M', sizeGb: 5, requiresFp4: false, quality: 0.91, emulatedFp4: false },
    ],
    engine: 'llama.cpp',
  },
  fit: {
    fits: true,
    quantization: 'Q4_K_M',
    nodesNeeded: 1,
    estimated: {
      decodeTokSMin: 30,
      decodeTokSMax: 50,
      prefillTokSMin: 100,
      prefillTokSMax: 200,
      headroomGb: 2,
    },
    deficitGb: 0,
    reasonKey: 'fits',
  },
};

const infeasibleEntry: CatalogEntry = {
  model: {
    ...feasibleEntry.model,
    modelId: 'llama-70b',
    family: 'Llama 70B',
    paramsTotalB: 70,
    paramsActiveB: 70,
  },
  fit: {
    fits: false,
    quantization: null,
    nodesNeeded: 0,
    estimated: null,
    deficitGb: 20,
    reasonKey: 'not_enough_memory',
  },
};

const feasiblePlanResult: PlanPreviewResult = {
  feasible: true,
  plan: {
    planId: 'plan-test-1',
    modelId: 'llama-8b',
    quantization: 'Q4_K_M',
    assignments: [
      { nodeId: 'node-gpu-01', role: 'host', layerStart: 0, layerEnd: 31, draft: false },
    ],
    pipelineOrder: ['node-gpu-01'],
    estimated: {
      decodeTokSMin: 30,
      decodeTokSMax: 50,
      prefillTokSMin: 100,
      prefillTokSMax: 200,
      headroomGb: 2,
    },
    cost: 1.0,
    explanation: ['Single node fits all layers'],
  },
};

const infeasiblePlanResult: PlanPreviewResult = {
  feasible: false,
  reason: 'Insufficient VRAM: need 40 GB, fleet has 16 GB',
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return {
    user: userEvent.setup(),
    ...render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <I18nProvider>
            <CatalogPage />
          </I18nProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('CatalogPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getCatalog).mockResolvedValue([feasibleEntry, infeasibleEntry]);
  });

  // --- Task A: Delete model --------------------------------------------------

  it('delete_button_shows_confirm_dialog', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const { user } = renderPage();

    // Wait for the catalog to load
    await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

    // Click the delete button for the first (feasible) model
    const deleteButtons = screen.getAllByLabelText('Delete');
    await user.click(deleteButtons[0]);

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining('llama-8b'),
    );
    // confirm returned false so deleteModel should NOT have been called
    expect(api.deleteModel).not.toHaveBeenCalled();

    confirmSpy.mockRestore();
  });

  it('delete_success_invalidates_catalog', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(api.deleteModel).mockResolvedValue(undefined);
    // Second call returns catalog without the deleted model
    vi.mocked(api.getCatalog)
      .mockResolvedValueOnce([feasibleEntry, infeasibleEntry])
      .mockResolvedValueOnce([infeasibleEntry]);

    const { user } = renderPage();
    await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

    const deleteButtons = screen.getAllByLabelText('Delete');
    await user.click(deleteButtons[0]);

    await waitFor(() => expect(api.deleteModel).toHaveBeenCalledWith('llama-8b'));
    // After success, getCatalog should be called again (cache invalidation)
    await waitFor(() => expect(api.getCatalog).toHaveBeenCalledTimes(2));
  });

  it('delete_409_shows_in_use_error', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(api.deleteModel).mockRejectedValue(
      new ApiError(409, 'model is referenced by one or more active deployments; tear them down first'),
    );

    const { user } = renderPage();
    await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

    const deleteButtons = screen.getAllByLabelText('Delete');
    await user.click(deleteButtons[0]);

    await waitFor(() =>
      expect(
        screen.getByText('Cannot delete: model is used by an active deployment'),
      ).toBeInTheDocument(),
    );
  });

  // --- Task B: Preview deploy -----------------------------------------------

  it('preview_split_shows_assignments', async () => {
    vi.mocked(api.previewModelPlan).mockResolvedValue(feasiblePlanResult);
    const { user } = renderPage();

    await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

    // Only the feasible model has "Preview Split"
    const previewBtn = screen.getByLabelText('Preview Split');
    await user.click(previewBtn);

    await waitFor(() =>
      expect(screen.getByText('Fleet split preview')).toBeInTheDocument(),
    );

    // Assignments list shows node id (appears once in assignments + once in pipeline)
    const nodeLabels = screen.getAllByText('node-gpu-01');
    expect(nodeLabels.length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('layers 0–31')).toBeInTheDocument();
    // HOST badge
    expect(screen.getByText('HOST')).toBeInTheDocument();

    // Pipeline order
    expect(screen.getByText('Pipeline order')).toBeInTheDocument();
  });

  it('preview_infeasible_shows_reason', async () => {
    // The feasible model's plan preview comes back infeasible (planner found no fit)
    vi.mocked(api.previewModelPlan).mockResolvedValue(infeasiblePlanResult);
    const { user } = renderPage();

    await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

    const previewBtn = screen.getByLabelText('Preview Split');
    await user.click(previewBtn);

    await waitFor(() =>
      expect(screen.getByText('Fleet split preview')).toBeInTheDocument(),
    );

    expect(
      screen.getByText(/Cannot be deployed on this fleet/),
    ).toBeInTheDocument();
    // Reason string should be in the modal
    expect(screen.getByText(/Insufficient VRAM/)).toBeInTheDocument();
  });
});
