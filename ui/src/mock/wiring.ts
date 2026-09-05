// ---------------------------------------------------------------------------
// Mock wiring — the single aggregation point for every in-memory fixture the
// UI can use in OPT-IN mock mode.
//
// This module (and everything it pulls in: backend.ts, chat.ts, data.ts,
// planner.ts) is loaded via a DYNAMIC `import()` in ../api/client.ts, and ONLY
// when mock mode is explicitly enabled. Rollup therefore splits the fixtures
// into a separate chunk that a default (real) production build never loads, so
// no fabricated data ships in the main bundle.
// ---------------------------------------------------------------------------
import type { OpenAIModel } from '../api/types';
import { mockBackend } from './backend';
import { mockChatTransport } from './chat';

export { mockBackend, mockChatTransport };

/** Mock "served models" = the models of the active mock deployments. */
export function mockListModels(): Promise<OpenAIModel[]> {
  return mockBackend.listDeployments().then((deps) => {
    const ids = Array.from(
      new Set(deps.filter((d) => d.state === 'active').map((d) => d.plan.modelId)),
    );
    return ids.map((id) => ({ id, object: 'model' as const, ownedBy: 'purser' }));
  });
}
