// Reads the human-owned feature annotations file.
// Path is relative to this file; the JSON lives at tests/contract/features.annotations.json.
//
// OWNERSHIP: this file is human-maintained — do NOT import from a generated file here.
// The `openapi` flag is intentionally absent from this interface: it is derived in
// memory by the Go contract tests from the Exempt field in openapi_registry.go, and
// it is not needed on the TS side (the TS contract tests cover client/page/nav only).
import features from '../../../tests/contract/features.annotations.json';

export interface Feature {
  name: string;
  routes: string[];
  client: string[];
  page: string | null;
  nav: string | null;
  docs: string | null;
  perm: string | null;
  gated: boolean;
}

export const FEATURES = features as Feature[];
