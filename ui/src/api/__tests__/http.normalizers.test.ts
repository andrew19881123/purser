// ---------------------------------------------------------------------------
// Integration-style tests for the HTTP client normalizers.
// These tests mock fetch at the transport level so every normalizer exercised
// is the real code path — not a stale hand-written fixture.
//
// Real API shapes captured from GET /api/v1/... on 2026-09-12:
//   - nodes: { nodes: [{ id, hardware_profile, state, ... }] }
//   - models: { models: [{ id, spec, fit: { deployable, nodeCount, estimated } }] }
//   - deployments: { deployments: [{ id, planId, state, detail: { engines } }] }
//   - cluster/health: { total_nodes, ready_nodes, status }
//   - join-token: { token, cluster_id, expires_at }
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createHttpApi } from '../http';

// The base URL is irrelevant — fetch is fully mocked.
const BASE = '/api/v1';
const api = createHttpApi(BASE);

/**
 * Stub the global fetch to return a plain-object Response-alike for the
 * NEXT call only. Using a plain object avoids jsdom Response constructor
 * limitations with body streams.
 */
function mockFetch(body: unknown, status = 200) {
  const text = JSON.stringify(body);
  const responseStub = {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => 'application/json' },
    text: () => Promise.resolve(text),
    json: () => Promise.resolve(JSON.parse(text)),
  } as unknown as Response;
  vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(responseStub);
}

beforeEach(() => {
  vi.restoreAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// listNodes — real Go API shape
// ---------------------------------------------------------------------------

describe('listNodes — real API shape', () => {
  const realNodeResponse = {
    nodes: [
      {
        id: 'node-10fb2f7a06f92660',
        hostname: 'kali',
        os: 'OS_LINUX',
        arch: 'ARCH_X86_64',
        state: 'NODE_STATE_RUNNING',
        ram_gb: 15.37,
        vram_gb: 0,
        advertised_agent_addr: '192.168.8.153:50161',
        advertised_inference_addr: '192.168.8.153:8000',
        last_seen: '2026-09-11T22:23:21.809491103Z',
        hardware_profile: {
          hostname: 'kali',
          os: 'OS_LINUX',
          arch: 'ARCH_X86_64',
          backends: ['BACKEND_CPU'],
          ramTotalGb: 15.37,
          ramAvailableGb: 4.17,
          memBandwidthGbs: 3.82,
          diskFreeGb: 2.84,
          engineVersions: { mock: 'built-in' },
          lastSeen: '2026-09-11T19:21:21.804356951Z',
          state: 'NODE_STATE_READY',
        },
        created_at: '2026-09-11T19:21:21.806129613Z',
        updated_at: '2026-09-11T22:23:21.810384388Z',
      },
    ],
  };

  it('unwraps the nodes wrapper array', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes).toHaveLength(1);
  });

  it('normalizes proto enum state: NODE_STATE_READY → ready', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes[0].profile.state).toBe('ready');
  });

  it('normalizes proto enum os: OS_LINUX → linux', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes[0].profile.os).toBe('linux');
  });

  it('normalizes proto enum arch: ARCH_X86_64 → x86_64', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes[0].profile.arch).toBe('x86_64');
  });

  it('normalizes proto enum backends: BACKEND_CPU → cpu', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes[0].profile.backends).toEqual(['cpu']);
  });

  it('back-fills nodeId from top-level id', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes[0].profile.nodeId).toBe('node-10fb2f7a06f92660');
  });

  it('gpus defaults to empty array for CPU-only nodes', async () => {
    mockFetch(realNodeResponse);
    const nodes = await api.listNodes();
    expect(nodes[0].profile.gpus).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// getCatalog — real Go API shape (spec field, deployable flag)
// ---------------------------------------------------------------------------

describe('getCatalog — real API shape', () => {
  const realCatalogResponse = {
    models: [
      {
        id: 'tinyllama-1b',
        family: 'demo',
        architecture: 'llama',
        params_total_b: 1.1,
        engine: 'mock',
        spec: {
          modelId: 'tinyllama-1b',
          family: 'demo',
          architecture: 'llama',
          paramsTotalB: 1.1,
          paramsActiveB: 1.1,
          layers: 16,
          hiddenSize: 2048,
          nKvHeads: 4,
          headDim: 64,
          attentionType: 'ATTENTION_TYPE_GQA',
          contextMax: '4096',
          quantizations: [{ name: 'q4_k_m', sizeGb: 0.7, quality: 0.9 }],
          engine: 'mock',
        },
        source: {},
        created_at: '2026-09-11T19:21:36.470054593Z',
        updated_at: '2026-09-11T19:21:36.470054593Z',
        fit: {
          model_id: 'tinyllama-1b',
          deployable: true,
          node_count: 1,
          quantization: 'q4_k_m',
          estimated: {
            decode_min_tok_s: 2.68,
            decode_max_tok_s: 4.97,
            prefill_min_tok_s: 21.4,
            prefill_max_tok_s: 39.7,
            headroom_gb: 1.4,
          },
        },
      },
    ],
  };

  it('extracts modelId from spec.modelId', async () => {
    mockFetch(realCatalogResponse);
    const catalog = await api.getCatalog();
    expect(catalog[0].model.modelId).toBe('tinyllama-1b');
  });

  it('quantizations is always an array (never crashes on .map)', async () => {
    mockFetch(realCatalogResponse);
    const catalog = await api.getCatalog();
    expect(Array.isArray(catalog[0].model.quantizations)).toBe(true);
    expect(catalog[0].model.quantizations[0].name).toBe('q4_k_m');
  });

  it('fit.fits derived from deployable: true', async () => {
    mockFetch(realCatalogResponse);
    const catalog = await api.getCatalog();
    expect(catalog[0].fit.fits).toBe(true);
  });

  it('fit.nodesNeeded from nodeCount', async () => {
    mockFetch(realCatalogResponse);
    const catalog = await api.getCatalog();
    expect(catalog[0].fit.nodesNeeded).toBe(1);
  });

  it('fit.estimated uses alternate field names decodeMinTokS', async () => {
    mockFetch(realCatalogResponse);
    const catalog = await api.getCatalog();
    const est = catalog[0].fit.estimated!;
    expect(est.decodeTokSMin).toBeCloseTo(2.68, 1);
    expect(est.decodeTokSMax).toBeCloseTo(4.97, 1);
    expect(est.prefillTokSMin).toBeCloseTo(21.4, 0);
    expect(est.headroomGb).toBeCloseTo(1.4, 1);
  });
});

// ---------------------------------------------------------------------------
// getCatalog — missing quantizations guard (would have been a crash)
// ---------------------------------------------------------------------------

describe('getCatalog — no-quantizations guard', () => {
  it('does not crash when quantizations is missing from spec', async () => {
    mockFetch({
      models: [
        {
          id: 'bare-model',
          spec: { modelId: 'bare-model', family: 'test' },
          fit: { deployable: false },
        },
      ],
    });
    const catalog = await api.getCatalog();
    // Must not throw; quantizations must be an array
    expect(Array.isArray(catalog[0].model.quantizations)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// listDeployments — real Go API shape (detail field, proto enum state)
// ---------------------------------------------------------------------------

describe('listDeployments — real API shape', () => {
  const realDeploymentResponse = {
    deployments: [
      {
        id: 'dep-0e1cc9e9715614e2e5a85f73',
        model_id: 'tinyllama-1b',
        plan_id: 'plan-tinyllama-1b-node-10fb2f7a06f92660-56b41691',
        state: 'DEPLOYMENT_STATE_ACTIVE',
        detail: {
          model_id: 'tinyllama-1b',
          quantization: 'q4_k_m',
          host_node_id: 'node-10fb2f7a06f92660',
          endpoint: 'http://192.168.8.153:8000',
          engines: [
            {
              node_id: 'node-10fb2f7a06f92660',
              agent_addr: '192.168.8.153:50161',
              role: 'host',
              handle: 'engine ready to serve',
            },
          ],
        },
        created_at: '2026-09-11T19:21:38.48794049Z',
        updated_at: '2026-09-11T19:21:38.502611042Z',
      },
    ],
  };

  it('normalizes DEPLOYMENT_STATE_ACTIVE → active', async () => {
    mockFetch(realDeploymentResponse);
    const deployments = await api.listDeployments();
    expect(deployments[0].state).toBe('active');
  });

  it('extracts modelId from top-level model_id', async () => {
    mockFetch(realDeploymentResponse);
    const deployments = await api.listDeployments();
    expect(deployments[0].plan.modelId).toBe('tinyllama-1b');
  });

  it('builds assignments from detail.engines', async () => {
    mockFetch(realDeploymentResponse);
    const deployments = await api.listDeployments();
    expect(deployments[0].plan.assignments).toHaveLength(1);
    expect(deployments[0].plan.assignments[0].nodeId).toBe('node-10fb2f7a06f92660');
    expect(deployments[0].plan.assignments[0].role).toBe('host');
  });

  it('sets nodeStatus state to running for active deployment', async () => {
    mockFetch(realDeploymentResponse);
    const deployments = await api.listDeployments();
    expect(deployments[0].nodeStatus[0].state).toBe('running');
  });

  it('preserves deployment id', async () => {
    mockFetch(realDeploymentResponse);
    const deployments = await api.listDeployments();
    expect(deployments[0].id).toBe('dep-0e1cc9e9715614e2e5a85f73');
  });
});

// ---------------------------------------------------------------------------
// getCapacity — real Go health endpoint shape
// ---------------------------------------------------------------------------

describe('getCapacity — real /cluster/health shape', () => {
  const realHealthResponse = {
    status: 'ok',
    total_nodes: 2,
    ready_nodes: 2,
    checked_at: '2026-09-11T22:25:25.209670202Z',
  };

  it('maps total_nodes → nodeCount', async () => {
    mockFetch(realHealthResponse);
    const cap = await api.getCapacity();
    expect(cap.nodeCount).toBe(2);
  });

  it('maps ready_nodes → readyNodeCount', async () => {
    mockFetch(realHealthResponse);
    const cap = await api.getCapacity();
    expect(cap.readyNodeCount).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// getJoinInfo — real POST /join-token shape (token, not joinToken)
// ---------------------------------------------------------------------------

describe('getJoinInfo — real /join-token shape', () => {
  const realJoinTokenResponse = {
    cluster_id: 'default',
    expires_at: '2026-09-12T22:24:09Z',
    token: 'eyJleHAiOjE3ODkyNTE4NDksIm5vbmNlIjoiY2RiN2ZkZjUxNWVlNjQ4ODFmODIxOTdhZTg1MTc4NzYifQ',
  };

  it('maps token → joinToken', async () => {
    mockFetch(realJoinTokenResponse);
    const info = await api.getJoinInfo();
    expect(info.joinToken).toBe(realJoinTokenResponse.token);
  });

  it('maps expires_at → expiresAt', async () => {
    mockFetch(realJoinTokenResponse);
    const info = await api.getJoinInfo();
    expect(info.expiresAt).toBe('2026-09-12T22:24:09Z');
  });
});
