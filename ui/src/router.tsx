// Routing. We use history routing (createBrowserRouter) because the control
// plane's nginx serves a try_files fallback to index.html, making every deep
// link work cleanly without a '#' fragment. URLs are clean and bookmarkable.
// If you ever need to run the SPA in an air-gapped environment without URL-
// rewrite support, swap back to hash routing with a one-liner:
//   import { createHashRouter } from 'react-router-dom';
//   export const router = createHashRouter([...]);
import { createBrowserRouter, type RouteObject } from 'react-router-dom';
import { Layout } from './components/Layout';
import { OnboardingPage } from './pages/OnboardingPage';
import { FleetPage } from './pages/FleetPage';
import { CatalogPage } from './pages/CatalogPage';
import { DeployPage } from './pages/DeployPage';
import { DeploymentsPage } from './pages/DeploymentsPage';
import { JoinTokenPage } from './pages/JoinTokenPage';
import { ModelStudioPage } from './pages/ModelStudioPage';
import { PlaygroundPage } from './pages/PlaygroundPage';
import { SettingsPage } from './pages/SettingsPage';
import { AuditPage } from './pages/AuditPage';
import { ApprovalsPage } from './pages/ApprovalsPage';
import { ChargebackPage } from './pages/ChargebackPage';
import { NotFoundPage } from './pages/NotFoundPage';
import { OrganizationsPage } from './pages/OrganizationsPage';
import { TeamsListPage } from './pages/TeamsListPage';
import { TeamPage } from './pages/TeamPage';
import { NodePoolsPage } from './pages/NodePoolsPage';
import { DataPlanesPage } from './pages/DataPlanesPage';
import { ServiceAccountsPage } from './pages/ServiceAccountsPage';
import { PoliciesPage } from './pages/PoliciesPage';
import { PlatformUsersPage } from './pages/PlatformUsersPage';
import { ApiKeysPage } from './pages/ApiKeysPage';
import { WhatIfPlannerPage } from './pages/WhatIfPlannerPage';
import { SLOPage } from './pages/SLOPage';

// Route table, exported separately from the configured `router` so tests can
// build an isolated `createMemoryRouter(routes, { initialEntries })` against the
// exact same config (see routing.reachability.test.tsx).
export const routes: RouteObject[] = [
  {
    path: '/',
    element: <Layout />,
    children: [
      { index: true, element: <OnboardingPage /> },
      { path: 'fleet', element: <FleetPage /> },
      { path: 'catalog', element: <CatalogPage /> },
      { path: 'model-studio', element: <ModelStudioPage /> },
      { path: 'deployments', element: <DeploymentsPage /> },
      { path: 'deploy/:modelId', element: <DeployPage /> },
      { path: 'join-token', element: <JoinTokenPage /> },
      { path: 'playground', element: <PlaygroundPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: 'audit', element: <AuditPage /> },
      { path: 'approvals', element: <ApprovalsPage /> },
      { path: 'chargeback', element: <ChargebackPage /> },
      // v0.4 platform model routes
      { path: 'platform/orgs', element: <OrganizationsPage /> },
      { path: 'platform/orgs/:orgId/teams', element: <TeamsListPage /> },
      { path: 'platform/orgs/:orgId/teams/:teamId', element: <TeamPage /> },
      { path: 'platform/pools', element: <NodePoolsPage /> },
      { path: 'platform/users', element: <PlatformUsersPage /> },
      // v0.6 platform routes
      { path: 'platform/dataplanes', element: <DataPlanesPage /> },
      { path: 'platform/service-accounts', element: <ServiceAccountsPage /> },
      { path: 'platform/policies', element: <PoliciesPage /> },
      { path: 'planner/what-if', element: <WhatIfPlannerPage /> },
      { path: 'api-keys', element: <ApiKeysPage /> },
      { path: 'slo', element: <SLOPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export const router = createBrowserRouter(routes);
