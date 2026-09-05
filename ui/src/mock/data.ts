// ---------------------------------------------------------------------------
// Mock fixtures for the Phase-1F skeleton. Everything here is fake but shaped
// exactly like the future `/api/v1` responses (see ../api/types.ts). Swapping
// these for real data in Phase 2 means replacing the client, not the UI.
// ---------------------------------------------------------------------------
import type {
  ApiKey,
  JoinInfo,
  ModelSpec,
  NodeView,
} from '../api/types';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();

// --- Fleet: a small heterogeneous prosumer cluster -------------------------

export const mockNodes: NodeView[] = [
  {
    profile: {
      nodeId: 'node-dgx-01',
      hostname: 'dgx-spark-01',
      os: 'linux',
      arch: 'x86_64',
      backends: ['cuda', 'cpu'],
      gpus: [
        { name: 'NVIDIA GB10 (DGX Spark)', vramGb: 128, unified: true, fp4Native: true, count: 1 },
      ],
      ramTotalGb: 128,
      ramAvailableGb: 96,
      memBandwidthGbs: 273,
      diskFreeGb: 3200,
      engineVersions: { 'llama.cpp': 'b4021', 'purser-agent': '0.1.0' },
      lastSeen: iso(2_000),
      state: 'running',
    },
    metrics: {
      prefillTokS: 680,
      decodeTokS: 46,
      ramUsedGb: 22,
      vramUsedGb: 74,
      queueDepth: 1,
      acceptedTokensRatio: 0.71,
    },
    role: 'host',
    linkQuality: 'excellent',
    deploymentId: 'dep-qwen3-moe',
  },
  {
    profile: {
      nodeId: 'node-mac-01',
      hostname: 'mac-studio-a',
      os: 'darwin',
      arch: 'arm64',
      backends: ['metal', 'cpu'],
      gpus: [
        { name: 'Apple M3 Ultra', vramGb: 96, unified: true, fp4Native: false, count: 1 },
      ],
      ramTotalGb: 96,
      ramAvailableGb: 70,
      memBandwidthGbs: 819,
      diskFreeGb: 1500,
      engineVersions: { 'llama.cpp': 'b4021', 'purser-agent': '0.1.0' },
      lastSeen: iso(3_500),
      state: 'running',
    },
    metrics: {
      prefillTokS: 540,
      decodeTokS: 44,
      ramUsedGb: 61,
      vramUsedGb: 0,
      queueDepth: 0,
      acceptedTokensRatio: 0.69,
    },
    role: 'worker',
    linkQuality: 'good',
    deploymentId: 'dep-qwen3-moe',
  },
  {
    profile: {
      nodeId: 'node-rtx-01',
      hostname: 'workstation-rtx',
      os: 'linux',
      arch: 'x86_64',
      backends: ['cuda', 'cpu'],
      gpus: [
        { name: 'NVIDIA RTX 4090', vramGb: 24, unified: false, fp4Native: false, count: 2 },
      ],
      ramTotalGb: 64,
      ramAvailableGb: 48,
      memBandwidthGbs: 1008,
      diskFreeGb: 900,
      engineVersions: { 'llama.cpp': 'b4021', 'purser-agent': '0.1.0' },
      lastSeen: iso(1_500),
      state: 'ready',
    },
    metrics: null,
    role: null,
    linkQuality: 'good',
    deploymentId: null,
  },
  {
    profile: {
      nodeId: 'node-mac-02',
      hostname: 'mac-studio-b',
      os: 'darwin',
      arch: 'arm64',
      backends: ['metal', 'cpu'],
      gpus: [
        { name: 'Apple M2 Max', vramGb: 64, unified: true, fp4Native: false, count: 1 },
      ],
      ramTotalGb: 64,
      ramAvailableGb: 50,
      memBandwidthGbs: 400,
      diskFreeGb: 700,
      engineVersions: { 'llama.cpp': 'b3990', 'purser-agent': '0.1.0' },
      lastSeen: iso(52_000),
      state: 'degraded',
    },
    metrics: {
      prefillTokS: 120,
      decodeTokS: 9,
      ramUsedGb: 41,
      vramUsedGb: 0,
      queueDepth: 6,
      acceptedTokensRatio: 0.4,
    },
    role: null,
    linkQuality: 'poor',
    deploymentId: null,
  },
  {
    profile: {
      nodeId: 'node-edge-01',
      hostname: 'office-nuc',
      os: 'linux',
      arch: 'x86_64',
      backends: ['cpu'],
      gpus: [],
      ramTotalGb: 32,
      ramAvailableGb: 12,
      memBandwidthGbs: 51,
      diskFreeGb: 210,
      engineVersions: { 'purser-agent': '0.1.0' },
      lastSeen: iso(240_000),
      state: 'unreachable',
    },
    metrics: null,
    role: null,
    linkQuality: 'unknown',
    deploymentId: null,
  },
];

// --- Catalog: models curated toward MoE + draft (good for pipeline split) --

export const mockModels: ModelSpec[] = [
  {
    modelId: 'qwen3-moe-235b',
    family: 'Qwen3',
    architecture: 'Qwen3MoeForCausalLM',
    paramsTotalB: 235,
    paramsActiveB: 22,
    layers: 94,
    hiddenSize: 4096,
    nKvHeads: 4,
    headDim: 128,
    attentionType: 'gqa',
    contextMax: 262144,
    isMoe: true,
    draft: { available: true, type: 'eagle', tailLayers: 2 },
    engine: 'llama.cpp',
    quantizations: [
      { name: 'NVFP4', sizeGb: 132, requiresFp4: true, quality: 0.97, emulatedFp4: false },
      { name: 'Q4_K_M', sizeGb: 142, requiresFp4: false, quality: 0.94, emulatedFp4: false },
      { name: 'Q3_K_M', sizeGb: 110, requiresFp4: false, quality: 0.88, emulatedFp4: false },
    ],
  },
  {
    modelId: 'llama3.1-70b',
    family: 'Llama 3.1',
    architecture: 'LlamaForCausalLM',
    paramsTotalB: 70,
    paramsActiveB: 70,
    layers: 80,
    hiddenSize: 8192,
    nKvHeads: 8,
    headDim: 128,
    attentionType: 'gqa',
    contextMax: 131072,
    isMoe: false,
    draft: { available: true, type: 'ngram', tailLayers: 0 },
    engine: 'llama.cpp',
    quantizations: [
      { name: 'Q4_K_M', sizeGb: 43, requiresFp4: false, quality: 0.93, emulatedFp4: false },
      { name: 'Q5_K_M', sizeGb: 50, requiresFp4: false, quality: 0.95, emulatedFp4: false },
      { name: 'Q8_0', sizeGb: 75, requiresFp4: false, quality: 0.99, emulatedFp4: false },
    ],
  },
  {
    modelId: 'mixtral-8x22b',
    family: 'Mixtral',
    architecture: 'MixtralForCausalLM',
    paramsTotalB: 141,
    paramsActiveB: 39,
    layers: 56,
    hiddenSize: 6144,
    nKvHeads: 8,
    headDim: 128,
    attentionType: 'gqa',
    contextMax: 65536,
    isMoe: true,
    draft: { available: false, type: '', tailLayers: 0 },
    engine: 'llama.cpp',
    quantizations: [
      { name: 'Q4_K_M', sizeGb: 86, requiresFp4: false, quality: 0.92, emulatedFp4: false },
      { name: 'Q6_K', sizeGb: 116, requiresFp4: false, quality: 0.97, emulatedFp4: false },
    ],
  },
  {
    modelId: 'deepseek-r1-671b',
    family: 'DeepSeek R1',
    architecture: 'DeepseekV3ForCausalLM',
    paramsTotalB: 671,
    paramsActiveB: 37,
    layers: 61,
    hiddenSize: 7168,
    nKvHeads: 128,
    headDim: 128,
    attentionType: 'mla',
    contextMax: 163840,
    isMoe: true,
    draft: { available: true, type: 'mtp', tailLayers: 1 },
    engine: 'llama.cpp',
    quantizations: [
      { name: 'NVFP4', sizeGb: 384, requiresFp4: true, quality: 0.96, emulatedFp4: false },
      { name: 'Q4_K_M', sizeGb: 404, requiresFp4: false, quality: 0.93, emulatedFp4: false },
    ],
  },
  {
    modelId: 'phi4-14b',
    family: 'Phi-4',
    architecture: 'Phi3ForCausalLM',
    paramsTotalB: 14,
    paramsActiveB: 14,
    layers: 40,
    hiddenSize: 5120,
    nKvHeads: 10,
    headDim: 128,
    attentionType: 'gqa',
    contextMax: 16384,
    isMoe: false,
    draft: { available: false, type: '', tailLayers: 0 },
    engine: 'llama.cpp',
    quantizations: [
      { name: 'Q4_K_M', sizeGb: 9, requiresFp4: false, quality: 0.9, emulatedFp4: false },
      { name: 'Q8_0', sizeGb: 15, requiresFp4: false, quality: 0.98, emulatedFp4: false },
    ],
  },
];

// --- Onboarding / enrollment ------------------------------------------------

export const mockJoinInfo: JoinInfo = {
  joinToken: 'prsr_join_7Kd2xQ9fLm3PvR8sT1wZ4bN6cH0aJ5e',
  controlPlaneUrl: 'https://purser.lan:8443',
  expiresAt: iso(-60 * 60 * 1000), // expires in 1h
};

// --- Settings: API keys -----------------------------------------------------

export const mockApiKeys: ApiKey[] = [
  {
    id: 'key_platform',
    name: 'platform-gateway',
    team: 'Platform',
    prefix: 'sk-purser-9f2a',
    role: 'admin',
    createdAt: iso(1000 * 60 * 60 * 24 * 40),
    lastUsedAt: iso(1000 * 60 * 3),
    monthlyQuota: null,
    usedThisMonth: 184203,
    revoked: false,
  },
  {
    id: 'key_data',
    name: 'data-science-notebooks',
    team: 'Data Science',
    prefix: 'sk-purser-4c81',
    role: 'inference',
    createdAt: iso(1000 * 60 * 60 * 24 * 12),
    lastUsedAt: iso(1000 * 60 * 60 * 5),
    monthlyQuota: 500000,
    usedThisMonth: 421890,
    revoked: false,
  },
  {
    id: 'key_legacy',
    name: 'legacy-intranet-bot',
    team: 'IT Ops',
    prefix: 'sk-purser-1b0d',
    role: 'viewer',
    createdAt: iso(1000 * 60 * 60 * 24 * 90),
    lastUsedAt: null,
    monthlyQuota: 50000,
    usedThisMonth: 0,
    revoked: true,
  },
];
