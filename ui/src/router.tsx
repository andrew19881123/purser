// Routing. We use a HASH router on purpose: the SPA is a static bundle served
// by the control plane from an unknown mount path, possibly in an air-gapped
// environment with no URL-rewrite config. Hash routing makes every deep link
// work under any static host with zero server configuration. Swapping to
// history routing later is a one-liner (createBrowserRouter) if the control
// plane guarantees an index.html fallback.
import { createHashRouter } from 'react-router-dom';
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
import { NotFoundPage } from './pages/NotFoundPage';

export const router = createHashRouter([
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
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]);
