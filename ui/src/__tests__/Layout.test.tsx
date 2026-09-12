// Layout.test.tsx — sidebar section structure and active-section detection.
import { render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { ThemeProvider } from '../lib/theme';
import { Layout } from '../components/Layout';

// Minimal child routes so Outlet renders without errors.
function makeRouter(initialPath: string) {
  return createMemoryRouter(
    [
      {
        path: '/',
        element: <Layout />,
        children: [
          { index: true,                           element: <div>Home</div> },
          { path: 'fleet',                         element: <div>Fleet</div> },
          { path: 'catalog',                       element: <div>Catalog</div> },
          { path: 'deployments',                   element: <div>Deployments</div> },
          { path: 'playground',                    element: <div>Playground</div> },
          { path: 'platform/dataplanes',           element: <div>DataPlanes</div> },
          { path: 'platform/pools',                element: <div>Pools</div> },
          { path: 'planner/what-if',               element: <div>WhatIf</div> },
          { path: 'platform/orgs',                 element: <div>Orgs</div> },
          { path: 'api-keys',                      element: <div>ApiKeys</div> },
          { path: 'platform/service-accounts',     element: <div>ServiceAccounts</div> },
          { path: 'platform/policies',             element: <div>Policies</div> },
          { path: 'approvals',                     element: <div>Approvals</div> },
          { path: 'audit',                         element: <div>Audit</div> },
          { path: 'chargeback',                    element: <div>Chargeback</div> },
          { path: 'slo',                           element: <div>SLO</div> },
          { path: 'join-token',                    element: <div>JoinToken</div> },
          { path: 'settings',                      element: <div>Settings</div> },
          { path: '*',                             element: <div>NotFound</div> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  );
}

function renderLayout(path: string) {
  const router = makeRouter(path);
  return render(
    <ThemeProvider>
      <I18nProvider>
        <RouterProvider router={router} />
      </I18nProvider>
    </ThemeProvider>,
  );
}

// ---------------------------------------------------------------------------
// Section visibility
// ---------------------------------------------------------------------------

describe('Layout — section labels', () => {
  it('renders all 5 section labels', () => {
    renderLayout('/fleet');
    expect(screen.getByText('Inference')).toBeInTheDocument();
    expect(screen.getByText('Platform')).toBeInTheDocument();
    expect(screen.getByText('Governance')).toBeInTheDocument();
    expect(screen.getByText('Observability')).toBeInTheDocument();
    expect(screen.getByText('Administration')).toBeInTheDocument();
  });

  it('renders nav links in each section', () => {
    renderLayout('/fleet');
    // INFERENCE
    expect(screen.getByRole('link', { name: /fleet/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /catalog/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /deployments/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /playground/i })).toBeInTheDocument();
    // PLATFORM
    expect(screen.getByRole('link', { name: /data planes/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /node pools/i })).toBeInTheDocument();
    // GOVERNANCE
    expect(screen.getByRole('link', { name: /organizations/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /api keys/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /service accounts/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /policies/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /approvals/i })).toBeInTheDocument();
    // OBSERVABILITY
    expect(screen.getByRole('link', { name: /audit log/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /chargeback/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /slo/i })).toBeInTheDocument();
    // ADMINISTRATION
    expect(screen.getByRole('link', { name: /add node/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /settings/i })).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Active section detection
// ---------------------------------------------------------------------------

function getActiveSectionLabel(container: HTMLElement): string | undefined {
  return container
    .querySelector('.nav__section--active .nav__section-label')
    ?.textContent ?? undefined;
}

describe('Layout — active section', () => {
  it('marks Inference active on /fleet', () => {
    const { container } = renderLayout('/fleet');
    expect(getActiveSectionLabel(container)).toBe('Inference');
  });

  it('marks Inference active on /catalog', () => {
    const { container } = renderLayout('/catalog');
    expect(getActiveSectionLabel(container)).toBe('Inference');
  });

  it('marks Inference active on /deployments', () => {
    const { container } = renderLayout('/deployments');
    expect(getActiveSectionLabel(container)).toBe('Inference');
  });

  it('marks Platform active on /platform/pools', () => {
    const { container } = renderLayout('/platform/pools');
    expect(getActiveSectionLabel(container)).toBe('Platform');
  });

  it('marks Platform active on /planner/what-if', () => {
    const { container } = renderLayout('/planner/what-if');
    expect(getActiveSectionLabel(container)).toBe('Platform');
  });

  it('marks Governance active on /platform/orgs', () => {
    const { container } = renderLayout('/platform/orgs');
    expect(getActiveSectionLabel(container)).toBe('Governance');
  });

  it('marks Governance active on /approvals', () => {
    const { container } = renderLayout('/approvals');
    expect(getActiveSectionLabel(container)).toBe('Governance');
  });

  it('marks Governance active on /api-keys', () => {
    const { container } = renderLayout('/api-keys');
    expect(getActiveSectionLabel(container)).toBe('Governance');
  });

  it('marks Observability active on /audit', () => {
    const { container } = renderLayout('/audit');
    expect(getActiveSectionLabel(container)).toBe('Observability');
  });

  it('marks Observability active on /chargeback', () => {
    const { container } = renderLayout('/chargeback');
    expect(getActiveSectionLabel(container)).toBe('Observability');
  });

  it('marks Observability active on /slo', () => {
    const { container } = renderLayout('/slo');
    expect(getActiveSectionLabel(container)).toBe('Observability');
  });

  it('marks Administration active on /join-token', () => {
    const { container } = renderLayout('/join-token');
    expect(getActiveSectionLabel(container)).toBe('Administration');
  });

  it('marks Administration active on /settings', () => {
    const { container } = renderLayout('/settings');
    expect(getActiveSectionLabel(container)).toBe('Administration');
  });

  it('has exactly one active section at a time', () => {
    const { container } = renderLayout('/fleet');
    const activeSections = container.querySelectorAll('.nav__section--active');
    expect(activeSections).toHaveLength(1);
  });
});
