// Reads the shared feature manifest. Path is relative to this file; the JSON
// lives at repo-root tests/contract/features.json.
import features from '../../../tests/contract/features.json';

export interface Feature {
  name: string;
  routes: string[];
  openapi: boolean;
  client: string[];
  page: string | null;
  nav: string | null;
  docs: string | null;
  perm: string | null;
  gated: boolean;
}

export const FEATURES = features as Feature[];
