// ---------------------------------------------------------------------------
// Runtime configuration, resolved from Vite env vars (baked at build time).
//
//   VITE_PURSER_MOCK          "0"/"false"/"off"/"no" -> use the REAL client.
//                             anything else (or unset) -> keep the in-memory
//                             mock, so `npm run build`/`dev` works with NO
//                             backend running. Mock is the default.
//   VITE_PURSER_API_BASE      Control-plane base for `/api/v1` calls.
//                             Default: same-origin "/api/v1".
//   VITE_PURSER_GATEWAY_BASE  OpenAI-compatible Gateway base for `/v1` calls.
//                             Default: same-origin "/v1" (override when the
//                             Gateway lives on another host/port).
//
// The seam in ./client.ts reads this once to pick mock vs. real; components and
// hooks never see these values.
// ---------------------------------------------------------------------------

/** Read a non-empty string env var, else the fallback. */
function envStr(key: string, fallback: string): string {
  const v = import.meta.env[key];
  return typeof v === 'string' && v.trim().length > 0 ? v.trim() : fallback;
}

/** Trailing slashes would produce `//` when concatenated with a path. */
function trimTrailingSlash(url: string): string {
  return url.replace(/\/+$/, '');
}

/** Mock is ON unless explicitly disabled — build/dev must work offline. */
function resolveMock(): boolean {
  const v = import.meta.env.VITE_PURSER_MOCK;
  if (v === undefined || v === null || v === '') return true;
  const s = String(v).trim().toLowerCase();
  return !(s === '0' || s === 'false' || s === 'off' || s === 'no');
}

export const config = {
  /** true -> in-memory mock backend; false -> real HTTP client. */
  mock: resolveMock(),
  /** Control-plane management-plane base, e.g. "/api/v1". */
  apiBase: trimTrailingSlash(envStr('VITE_PURSER_API_BASE', '/api/v1')),
  /** Gateway inference-plane base, e.g. "/v1" or "https://gw:8443/v1". */
  gatewayBase: trimTrailingSlash(envStr('VITE_PURSER_GATEWAY_BASE', '/v1')),
} as const;
