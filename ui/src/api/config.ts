// ---------------------------------------------------------------------------
// Runtime configuration for the UI.
//
// Two layers, resolved once at module load (highest precedence first):
//
//   1. RUNTIME  — `window.__PURSER_CONFIG__`, injected by `env.js` which is
//      loaded (synchronously) BEFORE the app bundle. In the container image
//      `env.js` is regenerated at start-up from environment variables by
//      `deploy/docker/docker-entrypoint.d/40-purser-runtime-config.sh`. This is
//      how the API base URL is configured per-deployment WITHOUT rebuilding the
//      bundle (Vite bakes `import.meta.env` at BUILD time, so build env alone
//      cannot be changed on `helm install`). Air-gap safe: `env.js` is a local
//      file, no external calls.
//   2. BUILD    — Vite env vars (`VITE_PURSER_*`), baked at build time. Handy
//      for local `npm run dev` / `.env.local`.
//   3. DEFAULT  — same-origin relative bases, REAL backend.
//
//   Mock backend:
//     Mock is strictly OPT-IN (local dev / offline demos). It is OFF by default
//     so a production build talks to the real control-plane REST API. Enable it
//     with `VITE_PURSER_MOCK=1` (build time) or `window.__PURSER_CONFIG__.mock`
//     === true (runtime, e.g. `PURSER_UI_MOCK=1` on the container).
//     API base:  `PURSER_API_BASE_URL` (runtime) / `VITE_PURSER_API_BASE`
//                (build).  Default: same-origin "/api/v1".
//     Gateway:   `PURSER_GATEWAY_BASE_URL` (runtime) / `VITE_PURSER_GATEWAY_BASE`
//                (build).  Default: same-origin "/v1".
//
// The seam in ./client.ts reads this once to pick mock vs. real; components and
// hooks never see these values.
// ---------------------------------------------------------------------------

/** OIDC configuration written into window.__PURSER_CONFIG__.oidc by the
 *  container entrypoint when PURSER_OIDC_ISSUER, PURSER_OIDC_CLIENT_ID, and
 *  PURSER_OIDC_REDIRECT_URI are all set. */
interface OIDCRuntimeConfig {
  issuer: string;
  clientId: string;
  redirectUri: string;
}

/** Shape of the optional runtime override object injected via `env.js`. */
interface PurserRuntimeConfig {
  /** Control-plane management-plane base, e.g. "/api/v1". */
  apiBase?: string;
  /** Gateway inference-plane base, e.g. "/v1". */
  gatewayBase?: string;
  /** true -> in-memory mock backend (opt-in; default is the real client). */
  mock?: boolean;
  /** OIDC configuration for the admin UI login flow. Present only when all
   *  three PURSER_OIDC_* env vars are set in the container. */
  oidc?: OIDCRuntimeConfig;
}

declare global {
  interface Window {
    __PURSER_CONFIG__?: PurserRuntimeConfig;
  }
}

/** Runtime overrides injected before the bundle loads (empty if none / SSR). */
function runtime(): PurserRuntimeConfig {
  if (typeof window === 'undefined') return {};
  return window.__PURSER_CONFIG__ ?? {};
}

/** First non-empty of: runtime override, Vite build env, static fallback. */
function resolveBase(runtimeVal: string | undefined, envKey: string, fallback: string): string {
  if (typeof runtimeVal === 'string' && runtimeVal.trim().length > 0) {
    return trimTrailingSlash(runtimeVal.trim());
  }
  const envVal = import.meta.env[envKey];
  if (typeof envVal === 'string' && envVal.trim().length > 0) {
    return trimTrailingSlash(envVal.trim());
  }
  return trimTrailingSlash(fallback);
}

/** Trailing slashes would produce `//` when concatenated with a path. */
function trimTrailingSlash(url: string): string {
  return url.replace(/\/+$/, '');
}

/** True only when a value clearly means "on". Everything else is off. */
function isTruthyFlag(v: unknown): boolean {
  const s = String(v ?? '').trim().toLowerCase();
  return s === '1' || s === 'true' || s === 'on' || s === 'yes';
}

/**
 * Mock is OFF by default (real backend). It turns on ONLY when explicitly
 * requested — a runtime `window.__PURSER_CONFIG__.mock === true`, or the
 * build-time `VITE_PURSER_MOCK` flag set to a truthy value. This guarantees a
 * shipped image never serves fabricated data unless an operator opts in.
 */
function resolveMock(): boolean {
  const rt = runtime().mock;
  if (typeof rt === 'boolean') return rt;
  return isTruthyFlag(import.meta.env.VITE_PURSER_MOCK);
}

const rt = runtime();

export const config = {
  /** true -> in-memory mock backend; false (default) -> real HTTP client. */
  mock: resolveMock(),
  /** Control-plane management-plane base, e.g. "/api/v1". */
  apiBase: resolveBase(rt.apiBase, 'VITE_PURSER_API_BASE', '/api/v1'),
  /** Gateway inference-plane base, e.g. "/v1" or "https://gw:8443/v1". */
  gatewayBase: resolveBase(rt.gatewayBase, 'VITE_PURSER_GATEWAY_BASE', '/v1'),
  /** OIDC configuration, or null when OIDC is not configured. All three of
   *  oidcIssuer / oidcClientId / oidcRedirectUri must be present in the
   *  runtime config for this to be non-null. */
  oidc: (rt.oidc?.issuer && rt.oidc?.clientId && rt.oidc?.redirectUri)
    ? { issuer: rt.oidc.issuer, clientId: rt.oidc.clientId, redirectUri: rt.oidc.redirectUri }
    : null,
} as const;

/**
 * Call this when the control-plane API responds with 401 Unauthorized.
 *
 * When OIDC is configured the browser is redirected to the IdP authorization
 * endpoint so the user can log in with their corporate account
 * (EntraID / Okta / Keycloak). The redirect uses the authorization code flow
 * with openid+email scopes — no PKCE in v0.2, just the login redirect.
 *
 * When OIDC is not configured this is a no-op.
 */
export function handleUnauthorized(): void {
  const oidcCfg = config.oidc;
  if (!oidcCfg) return;

  // Build the IdP authorization URL. The path follows the OAuth 2.0 /
  // OpenID Connect authorization endpoint convention used by EntraID, Okta,
  // and Keycloak (all reachable at <issuer>/oauth2/v2.0/authorize or similar).
  const authUrl = new URL(`${oidcCfg.issuer}/oauth2/v2.0/authorize`);
  authUrl.searchParams.set('client_id', oidcCfg.clientId);
  authUrl.searchParams.set('redirect_uri', oidcCfg.redirectUri);
  authUrl.searchParams.set('response_type', 'code');
  authUrl.searchParams.set('scope', 'openid email');

  if (typeof window !== 'undefined') {
    window.location.href = authUrl.toString();
  }
}
