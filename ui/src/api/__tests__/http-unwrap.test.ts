/**
 * Unit tests for the HTTP client's array-unwrap and join-token fixes.
 *
 * The control plane wraps list responses in an envelope object:
 *   GET /nodes       → { nodes: [...] }
 *   GET /models      → { models: [...] }
 *   GET /deployments → { deployments: [...] }
 *   GET /apikeys     → { apikeys: [...] }
 *
 * Before the fix the client always returned [] because it checked
 * `Array.isArray(raw)` against the envelope, not the inner array.
 *
 * The join-token test verifies that `normalizeJoinInfo` accepts the wire
 * format where the field is named "token" (not "joinToken").
 */
import { describe, it, expect, vi, afterEach } from 'vitest';
import { createHttpApi } from '../http';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Stub the global `fetch` to return a camel-case JSON body. */
function stubFetch(body: unknown, status = 200) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      headers: new Headers({ 'Content-Type': 'application/json' }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

// ---------------------------------------------------------------------------
// listNodes unwrap
// ---------------------------------------------------------------------------

describe('createHttpApi — listNodes unwrap', () => {
  it('unwraps {nodes:[...]} envelope and returns node list', async () => {
    const nodePayload = {
      id: 'node-01',
      node_id: 'node-01',
      hostname: 'test-box',
      os: 'linux',
      arch: 'x86_64',
      backends: ['cpu'],
      gpus: [],
      ram_total_gb: 32,
      ram_available_gb: 16,
      mem_bandwidth_gbs: 0,
      disk_free_gb: 100,
      engine_versions: {},
      last_seen: new Date().toISOString(),
      state: 'ready',
    };
    stubFetch({ nodes: [nodePayload] });

    const api = createHttpApi('/api/v1');
    const nodes = await api.listNodes();

    expect(nodes).toHaveLength(1);
    // nodeId is populated either from node_id (camelized to nodeId) or from id
    expect(nodes[0].profile.nodeId).toBeTruthy();
  });

  it('returns [] when envelope has empty nodes array', async () => {
    stubFetch({ nodes: [] });

    const api = createHttpApi('/api/v1');
    const nodes = await api.listNodes();

    expect(nodes).toEqual([]);
  });

  it('still works when response is a bare array (backward compat)', async () => {
    stubFetch([]);

    const api = createHttpApi('/api/v1');
    const nodes = await api.listNodes();

    expect(nodes).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// listDeployments unwrap
// ---------------------------------------------------------------------------

describe('createHttpApi — listDeployments unwrap', () => {
  it('unwraps {deployments:[...]} envelope', async () => {
    stubFetch({ deployments: [] });

    const api = createHttpApi('/api/v1');
    const deps = await api.listDeployments();

    expect(deps).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// listApiKeys unwrap
// ---------------------------------------------------------------------------

describe('createHttpApi — listApiKeys unwrap', () => {
  it('unwraps {apikeys:[...]} envelope', async () => {
    const keyPayload = {
      id: 'key-1',
      name: 'test-key',
      team: 'eng',
      prefix: 'sk-purser-abc',
      role: 'admin',
      created_at: new Date().toISOString(),
      last_used_at: null,
      monthly_quota: null,
      used_this_month: 0,
      revoked: false,
    };
    stubFetch({ apikeys: [keyPayload] });

    const api = createHttpApi('/api/v1');
    const keys = await api.listApiKeys();

    expect(keys).toHaveLength(1);
  });

  it('returns [] when apikeys envelope is empty', async () => {
    stubFetch({ apikeys: [] });

    const api = createHttpApi('/api/v1');
    const keys = await api.listApiKeys();

    expect(keys).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// getCatalog unwrap
// ---------------------------------------------------------------------------

describe('createHttpApi — getCatalog unwrap', () => {
  it('unwraps {models:[...]} envelope', async () => {
    stubFetch({ models: [] });

    const api = createHttpApi('/api/v1');
    const catalog = await api.getCatalog();

    expect(catalog).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// getJoinInfo — token field fix
// ---------------------------------------------------------------------------

describe('createHttpApi — getJoinInfo token field', () => {
  it('populates joinToken from wire "token" field (POST /join-token response)', async () => {
    stubFetch({
      cluster_id: 'default',
      expires_at: '2026-09-12T00:00:00Z',
      token: 'prsr_join_abc123xyz',
    });

    const api = createHttpApi('/api/v1');
    const info = await api.getJoinInfo();

    expect(info.joinToken).toBe('prsr_join_abc123xyz');
  });

  it('also accepts the camelCase "joinToken" field for backward compatibility', async () => {
    stubFetch({
      join_token: 'prsr_join_oldformat',
      control_plane_url: 'https://purser.lan:8443',
      expires_at: '2026-09-12T00:00:00Z',
    });

    const api = createHttpApi('/api/v1');
    const info = await api.getJoinInfo();

    expect(info.joinToken).toBe('prsr_join_oldformat');
  });

  it('expiresAt is populated from expires_at field', async () => {
    const expiry = '2026-09-15T12:00:00Z';
    stubFetch({
      cluster_id: 'default',
      expires_at: expiry,
      token: 'prsr_join_xyz',
    });

    const api = createHttpApi('/api/v1');
    const info = await api.getJoinInfo();

    expect(info.expiresAt).toBe(expiry);
  });
});

// ---------------------------------------------------------------------------
// normalizeNodeView — nodeId from id field
// ---------------------------------------------------------------------------

describe('createHttpApi — normalizeNodeView nodeId from id', () => {
  it('populates nodeId from top-level id when profile.nodeId is missing', async () => {
    const payload = {
      id: 'node-flat-01',
      hostname: 'flat-node',
      os: 'linux',
      arch: 'x86_64',
      backends: ['cpu'],
      gpus: [],
      ram_total_gb: 16,
      ram_available_gb: 8,
      mem_bandwidth_gbs: 0,
      disk_free_gb: 50,
      engine_versions: {},
      last_seen: new Date().toISOString(),
      state: 'ready',
    };
    stubFetch({ nodes: [payload] });

    const api = createHttpApi('/api/v1');
    const nodes = await api.listNodes();

    expect(nodes).toHaveLength(1);
    // After camelization, `id` stays `id`, and nodeId should be populated from it.
    expect(nodes[0].profile.nodeId).toBeTruthy();
  });
});
