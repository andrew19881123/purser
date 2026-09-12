/**
 * Router reachability sweep.
 *
 * Regression: several fully-built v0.6 pages were unreachable because
 * `router.tsx` still pointed their paths at <ComingSoonPage /> (or never added
 * a route at all). These tests drive the REAL `routes` table from router.tsx
 * through a MemoryRouter and assert each path resolves to its real page rather
 * than the "Coming soon" placeholder or the 404 NotFoundPage.
 *
 * `../api/client` has a top-level await (it code-splits the opt-in mock
 * fixtures), so it must be replaced with a factory — see PlaygroundPage tests.
 * A never-resolving `api` keeps every page's queries in the loading state, so
 * each page renders its data-independent PageHeader / actions — enough to prove
 * the route wiring without needing per-page fixtures.
 */
import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { ThemeProvider } from '../lib/theme';

vi.mock('../api/client', () => ({
  api: new Proxy({}, { get: () => () => new Promise(() => {}) }),
  makeChat: vi.fn(() => ({
    baseUrl: '/v1',
    streamChat: vi.fn(),
    listModels: vi.fn(() => new Promise(() => {})),
  })),
}));

// Imported AFTER the mock is registered so router.tsx's page graph resolves the
// mocked client.
import { routes } from '../router';

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  return render(
    <QueryClientProvider client={client}>
      <ThemeProvider>
        <I18nProvider>
          <RouterProvider router={router} />
        </I18nProvider>
      </ThemeProvider>
    </QueryClientProvider>,
  );
}

describe('router reachability', () => {
  it('/api-keys renders ApiKeysPage, not ComingSoon', () => {
    renderAt('/api-keys');
    expect(screen.getByRole('heading', { level: 1, name: 'API Keys' })).toBeInTheDocument();
    expect(screen.queryByText(/coming soon/i)).not.toBeInTheDocument();
  });

  it('/platform/users renders PlatformUsersPage', () => {
    renderAt('/platform/users');
    expect(screen.getByRole('heading', { level: 1, name: 'Platform Users' })).toBeInTheDocument();
    expect(screen.queryByText('This page does not exist.')).not.toBeInTheDocument();
  });

  it('/planner/what-if renders WhatIfPlannerPage, not ComingSoon', () => {
    renderAt('/planner/what-if');
    expect(
      screen.getByRole('heading', { level: 1, name: 'What-if Hardware ROI Planner' }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/coming soon/i)).not.toBeInTheDocument();
  });

  it('/platform/orgs/:orgId/teams renders TeamsListPage, not NotFound', () => {
    renderAt('/platform/orgs/org-1/teams');
    // "Create Team" action is unique to TeamsListPage.
    expect(screen.getByRole('button', { name: 'Create Team' })).toBeInTheDocument();
    expect(screen.queryByText('This page does not exist.')).not.toBeInTheDocument();
  });
});
