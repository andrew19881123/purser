/**
 * Contract tests — UI axes: client, page, nav.
 *
 * Driven by the shared manifest at tests/contract/features.json (Task 1).
 * Three axes:
 *   client — every manifest `client` method exists on the mockBackend (which
 *            implements PurserApi — the interface is a TS type, gone at runtime).
 *   page   — every manifest `page` path resolves to a real route element, not
 *            ComingSoonPage or NotFoundPage.
 *   nav    — every manifest `nav` key exists in both en.ts and it.ts locales.
 */
import { describe, it, expect, vi } from 'vitest';
import { createMemoryRouter } from 'react-router-dom';
import { FEATURES } from './manifest';

vi.mock('../api/client', () => ({
  api: new Proxy({}, { get: () => () => new Promise(() => {}) }),
  makeChat: vi.fn(() => ({
    baseUrl: '/v1',
    streamChat: vi.fn(),
    listModels: vi.fn(() => new Promise(() => {})),
  })),
}));

// Imported AFTER the mock so pages that import api/client see the mock.
import { routes } from '../router';
import * as enModule from '../i18n/en';
import * as itModule from '../i18n/it';

// ---------------------------------------------------------------------------
// (client) every manifest client method exists on the PurserApi implementation.
// The PurserApi interface is a TypeScript type — gone at runtime — so we assert
// against the mock backend which implements it.
// ---------------------------------------------------------------------------
describe('contract: client methods', () => {
  it('every manifest client method is a real PurserApi method', async () => {
    const { mockBackend } = await import('../mock/wiring');
    const impl = mockBackend as unknown as Record<string, unknown>;
    for (const f of FEATURES) {
      for (const m of f.client) {
        expect(typeof impl[m], `${f.name}: client method ${m}`).toBe('function');
      }
    }
  });
});

// ---------------------------------------------------------------------------
// (page) every manifest page resolves to a real route (not ComingSoon/NotFound).
// Uses createMemoryRouter to drive the real `routes` table synchronously;
// inspects the matched leaf element's constructor name without rendering.
// ---------------------------------------------------------------------------
describe('contract: pages reachable', () => {
  for (const f of FEATURES) {
    if (!f.page) continue;
    it(`${f.name}: ${f.page} resolves to a real page`, () => {
      const router = createMemoryRouter(routes, { initialEntries: [f.page!] });
      const matches = router.state.matches;
      const leaf = matches[matches.length - 1];
      const route = leaf?.route as unknown as { element?: { type?: { name?: string } } };
      const el = route?.element;
      const name = el?.type?.name ?? '';
      expect(
        ['ComingSoonPage', 'NotFoundPage'],
        `${f.name} page ${f.page} resolved to ${name || '(unknown)'}`,
      ).not.toContain(name);
    });
  }
});

// ---------------------------------------------------------------------------
// (nav) every manifest nav key exists in both locale files.
// ---------------------------------------------------------------------------
describe('contract: nav keys', () => {
  const en = (enModule as { en?: Record<string, string> }).en ??
    (enModule as { default?: Record<string, string> }).default!;
  const it2 = (itModule as { it?: Record<string, string> }).it ??
    (itModule as { default?: Record<string, string> }).default!;

  for (const f of FEATURES) {
    if (!f.nav) continue;
    it(`${f.name}: nav key ${f.nav} in both locales`, () => {
      expect(en[f.nav!], `en missing ${f.nav}`).toBeDefined();
      expect(it2[f.nav!], `it missing ${f.nav}`).toBeDefined();
    });
  }
});
