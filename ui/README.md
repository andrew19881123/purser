# Purser UI (operator dashboard)

**Status: Phase 1F skeleton — navigable, backend mocked.**

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

## Backend wiring (mock ↔ real)

The `PurserApi` seam in `src/api/client.ts` selects its implementation **once,
from env** (`src/api/config.ts`) — no component changes:

- `mockBackend` (default) — in-memory, offline. Build/dev work with no server.
- `createHttpApi('/api/v1')` (`src/api/http.ts`) — real `fetch` client, chosen
  when `VITE_PURSER_MOCK=0`.

Env vars (see `.env.example`; copy to `.env.local` to override):

| Var | Default | Purpose |
|-----|---------|---------|
| `VITE_PURSER_MOCK` | mock ON | `0`/`false`/`off`/`no` → real HTTP client |
| `VITE_PURSER_API_BASE` | `/api/v1` | Control-plane management API base (same origin) |
| `VITE_PURSER_GATEWAY_BASE` | `/v1` | OpenAI-compatible Gateway base (may be another host/port) |

The real client maps the `PurserApi` methods onto the control-plane REST surface
(`GET /nodes`, `GET /nodes/{id}`, `GET /models`, `POST /models/{id}/deploy`,
`GET /deployments`, `DELETE /deployments/{id}`, `GET /plans/{id}`,
`GET /cluster/health`, `POST /apikeys`, `GET /metrics` via SSE) and converts
snake_case ⇄ camelCase with a thin, tolerant serializer. The Playground streams
from the Gateway (`POST /v1/chat/completions` + SSE, `GET /v1/models`) with an
`Authorization: Bearer <key>` provided in the UI. The `purser.v1` proto contracts
drive `src/api/types.ts`. HTTP errors (401/403/404/429/503/504 + transport
failures) are mapped to actionable, localized messages in `src/lib/errors.ts`.
