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

/** Open the delete dialog for the first model card's delete button. */
async function openDeleteDialog(user: ReturnType<typeof userEvent.setup>) {
  await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());
  const deleteButtons = screen.getAllByLabelText('Delete');
  await user.click(deleteButtons[0]);
  await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());
}

/** Type the model ID into the confirmation input and click confirm. */
async function confirmDeletion(user: ReturnType<typeof userEvent.setup>, modelId = 'llama-8b') {
  const input = screen.getByRole('textbox');
  await user.type(input, modelId);
  const confirmBtn = screen.getByRole('button', { name: 'Delete permanently' });
  await user.click(confirmBtn);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('CatalogPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getCatalog).mockResolvedValue([feasibleEntry, infeasibleEntry]);
  });

  // --- Delete model ----------------------------------------------------------

  describe('Delete model', () => {
    it('shows delete button on each model card', async () => {
      renderPage();
      await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

      const deleteButtons = screen.getAllByLabelText('Delete');
      // Both feasible and infeasible model cards have a delete button
      expect(deleteButtons).toHaveLength(2);
    });

    it('opens confirmation dialog when delete button clicked', async () => {
      const { user } = renderPage();
      await openDeleteDialog(user);

      // Modal is visible with the right title
      expect(screen.getByRole('dialog')).toBeInTheDocument();
      expect(screen.getByText('Delete model')).toBeInTheDocument();
    });

    it('requires typing model name to enable confirm button', async () => {
      const { user } = renderPage();
      await openDeleteDialog(user);

      const confirmBtn = screen.getByRole('button', { name: 'Delete permanently' });
      // Initially disabled — nothing typed yet
      expect(confirmBtn).toBeDisabled();

      // Type something wrong — still disabled
      const input = screen.getByRole('textbox');
      await user.type(input, 'wrong-name');
      expect(confirmBtn).toBeDisabled();

      // Clear and type the correct model ID — now enabled
      await user.clear(input);
      await user.type(input, 'llama-8b');
      expect(confirmBtn).not.toBeDisabled();
    });

    it('calls DELETE API when confirmed', async () => {
      vi.mocked(api.deleteModel).mockResolvedValue(undefined);
      vi.mocked(api.getCatalog)
        .mockResolvedValueOnce([feasibleEntry, infeasibleEntry])
        .mockResolvedValueOnce([infeasibleEntry]);

      const { user } = renderPage();
      await openDeleteDialog(user);
      await confirmDeletion(user);

      await waitFor(() => expect(api.deleteModel).toHaveBeenCalledWith('llama-8b'));
    });

    it('shows error when model has active deployments (409)', async () => {
      vi.mocked(api.deleteModel).mockRejectedValue(
        new ApiError(409, 'model is referenced by one or more active deployments; tear them down first'),
      );

      const { user } = renderPage();
      await openDeleteDialog(user);
      await confirmDeletion(user);

      await waitFor(() =>
        expect(
          screen.getByText('Cannot delete: model is used by an active deployment'),
        ).toBeInTheDocument(),
      );
      // Dialog stays open so the user can take action
      expect(screen.getByRole('dialog')).toBeInTheDocument();
    });

    it('removes model from list after successful delete', async () => {
      vi.mocked(api.deleteModel).mockResolvedValue(undefined);
      // Second getCatalog call returns catalog without the deleted model
      vi.mocked(api.getCatalog)
        .mockResolvedValueOnce([feasibleEntry, infeasibleEntry])
        .mockResolvedValueOnce([infeasibleEntry]);

      const { user } = renderPage();
      await openDeleteDialog(user);
      await confirmDeletion(user);

      await waitFor(() => expect(api.deleteModel).toHaveBeenCalledWith('llama-8b'));
      // After success, getCatalog is refetched (cache invalidation)
      await waitFor(() => expect(api.getCatalog).toHaveBeenCalledTimes(2));
    });

    it('closes dialog after successful delete', async () => {
      vi.mocked(api.deleteModel).mockResolvedValue(undefined);
      vi.mocked(api.getCatalog)
        .mockResolvedValueOnce([feasibleEntry, infeasibleEntry])
        .mockResolvedValueOnce([infeasibleEntry]);

      const { user } = renderPage();
      await openDeleteDialog(user);
      await confirmDeletion(user);

      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    });

    it('cancels delete without calling API when dialog is dismissed', async () => {
      const { user } = renderPage();
      await openDeleteDialog(user);

      const cancelBtn = screen.getByRole('button', { name: 'Cancel' });
      await user.click(cancelBtn);

      expect(api.deleteModel).not.toHaveBeenCalled();
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
  });

  // --- Preview deploy -------------------------------------------------------

  describe('Preview Deploy', () => {
    it('shows preview button on each feasible model card', async () => {
      renderPage();
      await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

      // Only the feasible model has "Preview Split"
      const previewBtns = screen.getAllByLabelText('Preview Split');
      expect(previewBtns).toHaveLength(1);
    });

    it('opens preview modal when clicked', async () => {
      vi.mocked(api.previewModelPlan).mockResolvedValue(feasiblePlanResult);
      const { user } = renderPage();

      await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());
      await user.click(screen.getByLabelText('Preview Split'));

      await waitFor(() =>
        expect(screen.getByText('Fleet split preview')).toBeInTheDocument(),
      );
    });

    it('shows node assignments from plan response', async () => {
      vi.mocked(api.previewModelPlan).mockResolvedValue(feasiblePlanResult);
      const { user } = renderPage();

      await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

      const previewBtn = screen.getByLabelText('Preview Split');
      await user.click(previewBtn);

      await waitFor(() =>
        expect(screen.getByText('Fleet split preview')).toBeInTheDocument(),
      );

      // Assignments list shows node id (appears in assignments + pipeline)
      const nodeLabels = screen.getAllByText('node-gpu-01');
      expect(nodeLabels.length).toBeGreaterThanOrEqual(1);
      expect(screen.getByText('layers 0–31')).toBeInTheDocument();
      // HOST badge
      expect(screen.getByText('HOST')).toBeInTheDocument();

      // Pipeline order
      expect(screen.getByText('Pipeline order')).toBeInTheDocument();
    });

    it('shows "Not feasible" when plan is infeasible', async () => {
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

    it('shows loading state while fetching plan', async () => {
      // Promise that never resolves — simulates a long-running plan request
      vi.mocked(api.previewModelPlan).mockImplementation(() => new Promise(() => {}));
      const { user } = renderPage();

      await waitFor(() => expect(screen.getByText('Llama 3.1 8B')).toBeInTheDocument());

      const previewBtn = screen.getByLabelText('Preview Split');
      await user.click(previewBtn);

      // Button should be disabled (spinner) while the plan is loading
      await waitFor(() => expect(previewBtn).toBeDisabled());
    });
  });
});
