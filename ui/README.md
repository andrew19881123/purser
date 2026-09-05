# Purser UI (operator dashboard)

**Status: navigable dashboard wired to the real control-plane REST API by
default (in-memory mock is opt-in for offline dev).**

React + Vite + TypeScript single-page app, served by the Control Plane. The
whole bundle is **self-contained / offline**: no CDN, no web fonts, no external
services at runtime (air-gap requirement). Icons are inline SVG, the favicon is
a `data:` URI, and typography uses the system font stack.

## Commands (run inside `ui/`)

```bash
npm install       # project-local deps only (never -g); lands in ui/node_modules
npm run dev       # dev server with HMR
npm run build     # static production bundle -> ui/dist/
npm run typecheck # tsc --noEmit
npm run preview   # serve the built bundle locally
```

`npm run build` emits a static bundle to **`ui/dist/`** (`index.html` + hashed
JS/CSS in `dist/assets/`). Asset paths are relative (`base: './'`) so the
Control Plane can serve the SPA from any mount path.

## Stack & rationale

- **React + Vite + TS** — product decision (mature ecosystem/components); Vite
  gives a small static bundle and fast HMR.
- **react-router (hash router)** — deep links work under any static host with
  zero server rewrite config (safe for air-gapped control-plane serving).
- **@tanstack/react-query** — declarative loading/error/refetch/polling that map
  onto our actionable error states; the single seam to swap the mock backend for
  the real `/api/v1` client in Phase 2 without touching components.

## Structure

```
src/
  api/        types.ts (TS mirror of proto/purser/v1), client.ts (PurserApi seam),
              openai.ts (OpenAI-compatible chat client, mock + Phase-2 SSE transport)
  mock/       data.ts, planner.ts (fit/plan/capacity), backend.ts (/api/v1 mock), chat.ts (SSE mock)
  hooks/      queries.ts (React Query hooks)
  i18n/       t() wrapper + en/it string files
  components/ Layout, icons, ui.tsx (accessible primitives)
  pages/      Onboarding, Fleet, Catalog, Deploy, Deployments, Playground, Settings
  styles/     tokens.css (light/dark), global.css
```

## Backend wiring (real by default; mock opt-in)

The `PurserApi` seam in `src/api/client.ts` selects its implementation **once,
from config** (`src/api/config.ts`) — no component changes:

- `createHttpApi('/api/v1')` (`src/api/http.ts`) — real `fetch` client. **This is
  the default**, so a shipped build talks to the real control plane.
- `mockBackend` (`src/mock/*`) — in-memory, offline, **opt-in only** (serves
  fabricated data). It is pulled in via a dynamic `import()` gated on the flag,
  so its fixtures are code-split out of the default bundle. Enable it with
  `VITE_PURSER_MOCK=1` (build) or `PURSER_UI_MOCK=1` (container runtime).

### Runtime configuration (containers)

Vite bakes `import.meta.env` at **build** time, so the built bundle can't be
re-pointed by build env. To configure a built image per-deployment, the app also
reads `window.__PURSER_CONFIG__`, injected by **`env.js`** which loads *before*
the bundle. The container regenerates `env.js` from environment variables at
start-up (`deploy/docker/docker-entrypoint.d/40-purser-runtime-config.sh`); the
Helm chart exposes these as `ui.apiBaseUrl` / `ui.gatewayBaseUrl` / `ui.extraEnv`.
The file is local (air-gap safe) and served uncached so a restart takes effect.

Precedence (highest first): `window.__PURSER_CONFIG__` (runtime) → `VITE_*`
(build) → same-origin defaults.

| Runtime env (container) | Build env (`.env.local`) | Default | Purpose |
|-------------------------|--------------------------|---------|---------|
| `PURSER_API_BASE_URL` | `VITE_PURSER_API_BASE` | `/api/v1` | Control-plane management API base (same origin) |
| `PURSER_GATEWAY_BASE_URL` | `VITE_PURSER_GATEWAY_BASE` | `/v1` | OpenAI-compatible Gateway base (may be another host/port) |
| `PURSER_UI_MOCK` | `VITE_PURSER_MOCK` | off (**real**) | `1`/`true`/`on`/`yes` → opt in to the in-memory mock |

The real client maps the `PurserApi` methods onto the control-plane REST surface
(`GET /nodes`, `GET /nodes/{id}`, `GET /models`, `POST /models/{id}/deploy`,
`GET /deployments`, `DELETE /deployments/{id}`, `GET /plans/{id}`,
`GET /cluster/health`, `POST /apikeys`, `GET /metrics` via SSE) and converts
snake_case ⇄ camelCase with a thin, tolerant serializer. The Playground streams
from the Gateway (`POST /v1/chat/completions` + SSE, `GET /v1/models`) with an
`Authorization: Bearer <key>` provided in the UI. The `purser.v1` proto contracts
drive `src/api/types.ts`. HTTP errors (401/403/404/429/503/504 + transport
failures) are mapped to actionable, localized messages in `src/lib/errors.ts`.
