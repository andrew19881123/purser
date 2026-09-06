// Routing. We use history routing (createBrowserRouter) because the control
// plane's nginx serves a try_files fallback to index.html, making every deep
// link work cleanly without a '#' fragment. URLs are clean and bookmarkable.
// If you ever need to run the SPA in an air-gapped environment without URL-
// rewrite support, swap back to hash routing with a one-liner:
//   import { createHashRouter } from 'react-router-dom';
//   export const router = createHashRouter([...]);
import { createBrowserRouter } from 'react-router-dom';
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

export const router = createBrowserRouter([
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
