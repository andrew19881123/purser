/**
 * Unit tests for PurserClient.
 * Uses Node.js built-in test runner (node:test) and assertions (node:assert).
 * fetch is mocked by replacing globalThis.fetch before each assertion.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PurserClient } from '../src/client';
import { NotFoundError, ConflictError } from '../src/errors';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Build a minimal fetch mock that returns a fixed status and JSON body.
 * The returned value is cast to `typeof fetch` so the type-checker is satisfied.
 */
function makeFetch(status: number, body: unknown): typeof fetch {
  return (async (_url: unknown, _init?: RequestInit) =>
    ({
      status,
      ok: status >= 200 && status < 300,
      json: async () => body,
      text: async () => JSON.stringify(body),
      body: null,
    }) as unknown as Response) as typeof fetch;
}

/**
 * Build a fetch mock that also captures the parsed request body for inspection.
 */
function makeCapturingFetch(
  status: number,
  body: unknown,
): { fetch: typeof fetch; getBody: () => unknown } {
  let capturedBody: unknown = null;

  const mockFetch = async (_url: unknown, init?: RequestInit) => {
    if (init?.body && typeof init.body === 'string') {
      capturedBody = JSON.parse(init.body);
    }
    return {
      status,
      ok: status >= 200 && status < 300,
      json: async () => body,
      text: async () => JSON.stringify(body),
      body: null,
    } as unknown as Response;
  };

  return {
    fetch: mockFetch as typeof fetch,
    getBody: () => capturedBody,
  };
}

// ---------------------------------------------------------------------------
// test_list_nodes — mock fetch, verify Node[] returned
// ---------------------------------------------------------------------------

test('test_list_nodes — returns Node array', async () => {
  const nodes = [
    { id: 'node-1', hostname: 'worker-1', state: 'NODE_STATE_READY', ram_gb: 128, vram_gb: 80 },
    { id: 'node-2', hostname: 'worker-2', state: 'NODE_STATE_RUNNING', ram_gb: 64, vram_gb: 40 },
  ];

  globalThis.fetch = makeFetch(200, { nodes });

  const client = new PurserClient('http://localhost:8080');
  const result = await client.listNodes();

  assert.equal(result.length, 2);
  assert.equal(result[0].id, 'node-1');
  assert.equal(result[0].hostname, 'worker-1');
  assert.equal(result[0].state, 'NODE_STATE_READY');
  assert.equal(result[1].id, 'node-2');
  assert.equal(result[1].state, 'NODE_STATE_RUNNING');
});

// ---------------------------------------------------------------------------
// test_404_throws_not_found
// ---------------------------------------------------------------------------

test('test_404_throws_not_found — getNode throws NotFoundError', async () => {
  globalThis.fetch = makeFetch(404, {
    error: 'not_found',
    message: 'node not found',
  });

  const client = new PurserClient('http://localhost:8080');

  await assert.rejects(
    () => client.getNode('no-such-node'),
    (err: unknown) => {
      assert.ok(err instanceof NotFoundError, 'expected NotFoundError');
      assert.equal((err as NotFoundError).statusCode, 404);
      assert.equal((err as NotFoundError).message, 'node not found');
      return true;
    },
  );
});

// ---------------------------------------------------------------------------
// test_409_throws_conflict
// ---------------------------------------------------------------------------

test('test_409_throws_conflict — deleteNode throws ConflictError', async () => {
  globalThis.fetch = makeFetch(409, {
    error: 'node_in_use',
    message: 'node still hosts one or more active deployments',
    deployments: ['dep-abc123'],
  });

  const client = new PurserClient('http://localhost:8080');

  await assert.rejects(
    () => client.deleteNode('node-1'),
    (err: unknown) => {
      assert.ok(err instanceof ConflictError, 'expected ConflictError');
      assert.equal((err as ConflictError).statusCode, 409);
      return true;
    },
  );
});

// ---------------------------------------------------------------------------
// test_create_api_key_with_role — POST body includes role
// ---------------------------------------------------------------------------

test('test_create_api_key_with_role — POST body includes role field', async () => {
  const { fetch: capturingFetch, getBody } = makeCapturingFetch(201, {
    id: 'key-a1b2c3d4',
    name: 'ci-pipeline',
    tenant: 'team-a',
    key: 'psk_AAABBBCCCDDDEEEFFFGGG',
    enabled: true,
    quota: 1000,
  });

  globalThis.fetch = capturingFetch;

  const client = new PurserClient('http://localhost:8080');
  const key = await client.createApiKey('ci-pipeline', {
    tenant: 'team-a',
    quota: 1000,
    role: 'operator',
  });

  // Verify the returned key object.
  assert.equal(key.id, 'key-a1b2c3d4');
  assert.equal(key.name, 'ci-pipeline');
  assert.equal(key.key, 'psk_AAABBBCCCDDDEEEFFFGGG');

  // Verify the request body included the role field.
  const body = getBody() as Record<string, unknown>;
  assert.ok(body !== null, 'request body should not be null');
  assert.equal(body['role'], 'operator', 'POST body must include role');
  assert.equal(body['name'], 'ci-pipeline');
  assert.equal(body['tenant'], 'team-a');
  assert.equal(body['quota'], 1000);
});
