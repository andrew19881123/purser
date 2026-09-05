// ---------------------------------------------------------------------------
// Canned model specs and plan-preview fixtures for the Model Studio mock.
// One ModelSpec per import source type so every source shows a believable
// result without any network traffic.
// ---------------------------------------------------------------------------
import type { ImportSource, ImportSourceType, ModelSpec, PlanPreviewResult } from '../api/types';
import { buildPlan } from './planner';
import type { NodeView } from '../api/types';

// --- canned ModelSpec per source type --------------------------------------

const hfLlama: ModelSpec = {
  modelId: 'hf:meta-llama/Llama-3.1-8B-Instruct',
  family: 'Llama 3.1 8B',
  architecture: 'LlamaForCausalLM',
  paramsTotalB: 8,
  paramsActiveB: 8,
  layers: 32,
  hiddenSize: 4096,
  nKvHeads: 8,
  headDim: 128,
  attentionType: 'gqa',
  contextMax: 131072,
  isMoe: false,
  draft: { available: false, type: '', tailLayers: 0 },
  engine: 'llama.cpp',
  quantizations: [
    { name: 'Q4_K_M', sizeGb: 5, requiresFp4: false, quality: 0.91, emulatedFp4: false },
    { name: 'Q5_K_M', sizeGb: 6, requiresFp4: false, quality: 0.93, emulatedFp4: false },
    { name: 'Q8_0', sizeGb: 9, requiresFp4: false, quality: 0.99, emulatedFp4: false },
  ],
};

const objStorageGemma: ModelSpec = {
  modelId: 'obj:gemma-2-27b',
  family: 'Gemma 2 27B',
  architecture: 'Gemma2ForCausalLM',
  paramsTotalB: 27,
  paramsActiveB: 27,
  layers: 46,
  hiddenSize: 4608,
  nKvHeads: 16,
  headDim: 256,
  attentionType: 'mha',
  contextMax: 8192,
  isMoe: false,
  draft: { available: false, type: '', tailLayers: 0 },
  engine: 'llama.cpp',
  quantizations: [
    { name: 'Q4_K_M', sizeGb: 17, requiresFp4: false, quality: 0.9, emulatedFp4: false },
    { name: 'Q8_0', sizeGb: 29, requiresFp4: false, quality: 0.98, emulatedFp4: false },
  ],
};

const sageMakerMistral: ModelSpec = {
  modelId: 'sm:mistral-7b-instruct',
  family: 'Mistral 7B Instruct',
  architecture: 'MistralForCausalLM',
  paramsTotalB: 7,
  paramsActiveB: 7,
  layers: 32,
  hiddenSize: 4096,
  nKvHeads: 8,
  headDim: 128,
  attentionType: 'gqa',
  contextMax: 32768,
  isMoe: false,
  draft: { available: false, type: '', tailLayers: 0 },
  engine: 'llama.cpp',
  quantizations: [
    { name: 'Q4_K_M', sizeGb: 4, requiresFp4: false, quality: 0.9, emulatedFp4: false },
    { name: 'Q5_K_M', sizeGb: 5, requiresFp4: false, quality: 0.92, emulatedFp4: false },
  ],
};

const vertexGeminiNano: ModelSpec = {
  modelId: 'vertex:gemini-nano-2b',
  family: 'Gemini Nano 2B',
  architecture: 'GemmaForCausalLM',
  paramsTotalB: 2,
  paramsActiveB: 2,
  layers: 26,
  hiddenSize: 2048,
  nKvHeads: 4,
  headDim: 256,
  attentionType: 'mha',
  contextMax: 32768,
  isMoe: false,
  draft: { available: false, type: '', tailLayers: 0 },
  engine: 'llama.cpp',
  quantizations: [
    { name: 'Q4_K_M', sizeGb: 2, requiresFp4: false, quality: 0.88, emulatedFp4: false },
    { name: 'Q8_0', sizeGb: 3, requiresFp4: false, quality: 0.97, emulatedFp4: false },
  ],
};

const azureMLPhi3: ModelSpec = {
  modelId: 'azure:phi-3-mini-4k',
  family: 'Phi-3 Mini 4K',
  architecture: 'Phi3ForCausalLM',
  paramsTotalB: 3.8,
  paramsActiveB: 3.8,
  layers: 32,
  hiddenSize: 3072,
  nKvHeads: 8,
  headDim: 96,
  attentionType: 'mha',
  contextMax: 4096,
  isMoe: false,
  draft: { available: false, type: '', tailLayers: 0 },
  engine: 'llama.cpp',
  quantizations: [
    { name: 'Q4_K_M', sizeGb: 2.4, requiresFp4: false, quality: 0.88, emulatedFp4: false },
    { name: 'Q5_K_M', sizeGb: 2.9, requiresFp4: false, quality: 0.91, emulatedFp4: false },
  ],
};

/** Map a source type → its canned ModelSpec. */
export const CANNED_MODELS: Record<Exclude<ImportSourceType, 'catalog'>, ModelSpec> = {
  huggingface: hfLlama,
  object_storage: objStorageGemma,
  sagemaker: sageMakerMistral,
  vertexai: vertexGeminiNano,
  azure_ml: azureMLPhi3,
};

/** Pick the canned model for a given external import source. */
export function cannedModelForSource(source: ImportSource): ModelSpec {
  return CANNED_MODELS[source.type];
}

/**
 * Build a mock PlanPreviewResult for the given model and fleet.
 * Returns feasible=true for models ≤ 50 GB (fits the mock fleet),
 * feasible=false with a canned reason for anything larger.
 */
export function mockPreviewPlan(model: ModelSpec, nodes: NodeView[]): PlanPreviewResult {
  const smallest = model.quantizations.reduce((a, b) => (a.sizeGb < b.sizeGb ? a : b));
  const fleetGb = nodes
    .filter((n) => n.profile.state === 'ready' || n.profile.state === 'running')
    .reduce((s, n) => {
      const vram = n.profile.gpus.reduce((g, x) => g + x.vramGb * x.count, 0);
      const unified = n.profile.gpus.some((g) => g.unified);
      return s + (unified ? Math.max(vram, n.profile.ramTotalGb) : vram || n.profile.ramTotalGb);
    }, 0);

  if (smallest.sizeGb * 0.85 > fleetGb) {
    return {
      feasible: false,
      reason: `Not enough memory: ${model.family} requires at least ${smallest.sizeGb} GB (smallest variant), but the fleet's usable capacity is ${Math.round(fleetGb)} GB.`,
    };
  }

  const plan = buildPlan(model, nodes, { forceNodeCount: null, preference: 'balanced' });
  return { feasible: true, plan };
}
