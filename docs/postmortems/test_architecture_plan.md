# Test Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a local-first, registry-driven test layer that makes the five
known classes of component-junction gaps structurally impossible to reintroduce
silently, with zero new CI jobs.

**Architecture:** A checked-in JSON manifest (`tests/contract/features.json`) is
the source of truth for every feature's junctions. Go contract tests verify the
route/openapi/permission/docs axes; TS/vitest contract tests verify the
client/page/nav axes; both read the same manifest. A Go completeness meta-test
prevents the manifest from silently going stale. A Go E2E harness (ephemeral
ports, condition-based readiness) spins up a native mock-engine stack for a
golden-path and a gateway-restart test. A local opt-in git pre-push hook runs the
fast contract tests; `make e2e` runs the heavy suite on demand.

**Tech Stack:** Go (`testing`, `encoding/json`, `net/http/httptest`,
`os/exec`), TypeScript + vitest (jsdom), git `core.hooksPath`, Make.

**Spec:** `docs/postmortems/test_architecture.md`

## Global Constraints

- **Zero new CI jobs.** Do not modify `.github/workflows/`. Protection is local.
- **Manifest format is JSON, not YAML.** The UI has no YAML parser
  (`ui/package.json` has none); JSON is parsed natively by both Go
  (`encoding/json`) and TS. File: `tests/contract/features.json`.
- **Manifest is read, never duplicated.** Both toolchains read
  `tests/contract/features.json`; neither hardcodes its contents.
- **Each side verifies only axes it can read robustly in its own language.**
  Go: routes/openapi/perm/docs. TS: client/page/nav. Neither parses the other's
  source language.
- **Environment before Go/Rust commands:** `source /home/andrea/Projects/purser/env.sh`
  (absolute path — `.toolchain/` is absent in worktrees). Go: `CGO_ENABLED=0`.
  Rust: `CARGO_TARGET_DIR=/tmp/purser-shared-target`, always `-p <crate>`.
- **Conventional commits, signed off**, with `Co-Authored-By: Claude <noreply@anthropic.com>`.
  Stage explicit paths only — never `git add -A`.
- **Enforced-vocabulary source of truth:** permission strings are the
  `Perm*` constants in `go/controlplane/permissions/permissions.go`; the served
  catalog `All()` is derived from them (post-fix state, commit f467649).
- **Real deploy route** is `POST /api/v1/models/{id}/deploy` (`server.go:1434-1435`),
  NOT the `/api/v1/models/llama-8b/deploy` string in the older `tools/e2e_full.sh`.
- **Gateway liveness** is `GET /healthz` (`rust/crates/gateway/src/routes/health.rs:30`);
  control-plane readiness is `GET /api/v1/cluster/health`.

---

## File Structure

- `tests/contract/features.json` — the manifest (source of truth).
- `tests/contract/manifest.go` — shared Go loader (`package contract`): parses
  `features.json` into typed structs; used by all Go contract tests.
- `tests/contract/completeness_test.go` — every registered non-exempt route ∈ manifest.
- `tests/contract/backend_test.go` — routes-exist, openapi, perm, docs axes.
- `ui/src/contract/manifest.ts` — shared TS loader: imports `features.json`, typed.
- `ui/src/contract/features.contract.test.tsx` — client/page/nav axes.
- `tests/e2e/harness.go` — `package e2e`: build, ephemeral ports, start, readiness, teardown.
- `tests/e2e/golden_path_test.go` — enroll→deploy→route→chat(mock).
- `tests/e2e/gateway_restart_test.go` — restart gateway, assert route self-heal.
- `tools/hooks/pre-push` — the hook script.
- `tools/hooks/install.sh` — sets `core.hooksPath`.
- `Makefile` — add `contract`, `e2e`, `verify`, `install-hooks` targets.
- `go.work` or a `tests/go.mod` — see Task 1 note on module wiring.

---

## Task 1: Manifest + shared Go loader + completeness meta-test

**Files:**
- Create: `tests/contract/features.json`
- Create: `tests/contract/manifest.go`
- Create: `tests/contract/completeness_test.go`
- Create: `tests/contract/go.mod` (module `github.com/purser/purser/tests/contract`)

**Interfaces:**
- Produces: `contract.Feature` struct and `contract.Load(path string) ([]Feature, error)`;
  `contract.RegisteredRoutes(serverGoPath string) ([]string, error)`.

**Note on module wiring:** the contract tests live outside `go/controlplane`.
Give `tests/contract/` its own `go.mod` with a `replace
github.com/purser/purser/go/controlplane => ../../go/controlplane` directive so
it can import the control-plane package if needed. Simpler alternative if the
import proves unnecessary: read `server.go` as a text file (the routes are
extracted by regex, not by importing the server), which avoids the module
dependency entirely. Prefer the text-read approach — it is what
`RegisteredRoutes` below does.

- [ ] **Step 1: Write `features.json` with 5 real features**

```json
[
  {
    "name": "api-keys",
    "routes": ["GET /api/v1/apikeys", "POST /api/v1/apikeys", "DELETE /api/v1/apikeys/{id}"],
    "openapi": true,
    "client": ["listApiKeys", "createApiKey", "revokeApiKey"],
    "page": "/api-keys",
    "nav": "nav.apiKeys",
    "docs": "configuration/api-keys.md",
    "perm": "team:keys:create",
    "gated": false
  },
  {
    "name": "roles",
    "routes": ["GET /api/v1/platform/orgs/{orgId}/roles", "POST /api/v1/platform/orgs/{orgId}/roles", "DELETE /api/v1/platform/orgs/{orgId}/roles/{id}"],
    "openapi": true,
    "client": ["listRoles", "createRole", "deleteRole"],
    "page": "/platform/roles",
    "nav": "nav.roles",
    "docs": "configuration/rbac.md",
    "perm": "org:roles:create",
    "gated": false
  },
  {
    "name": "data-planes",
    "routes": ["GET /api/v1/platform/dataplanes", "POST /api/v1/platform/dataplanes", "DELETE /api/v1/platform/dataplanes/{id}"],
    "openapi": true,
    "client": ["listDataPlanes", "createDataPlane", "deleteDataPlane"],
    "page": "/platform/dataplanes",
    "nav": "nav.dataplanes",
    "docs": "configuration/data-plane.md",
    "perm": null,
    "gated": false
  },
  {
    "name": "compliance",
    "routes": ["GET /api/v1/compliance/ai-act/technical-doc", "POST /api/v1/gdpr/erasure"],
    "openapi": true,
    "client": ["getAiActTechnicalDoc", "eraseSubject"],
    "page": "/compliance",
    "nav": "nav.compliance",
    "docs": "enterprise/ai-act-compliance.md",
    "perm": null,
    "gated": true
  },
  {
    "name": "join-token",
    "routes": ["POST /api/v1/join-token", "GET /api/v1/enrollment-bundle"],
    "openapi": true,
    "client": ["createJoinToken"],
    "page": "/join-token",
    "nav": "nav.joinTokens",
    "docs": "install/enrollment-bundle.md",
    "perm": null,
    "gated": false
  }
]
```

(Verify each `routes` string, `client` method, `page`, `nav` key, `docs` path,
and `perm` against the live code before finalizing — they are the assertions.)

- [ ] **Step 2: Write `manifest.go` — the loader + route extractor**

```go
package contract

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
)

type Feature struct {
	Name    string   `json:"name"`
	Routes  []string `json:"routes"`
	OpenAPI bool     `json:"openapi"`
	Client  []string `json:"client"`
	Page    *string  `json:"page"`
	Nav     *string  `json:"nav"`
	Docs    *string  `json:"docs"`
	Perm    *string  `json:"perm"`
	Gated   bool     `json:"gated"`
}

func Load(path string) ([]Feature, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fs []Feature
	if err := json.Unmarshal(b, &fs); err != nil {
		return nil, err
	}
	return fs, nil
}

// routeRe matches: s.mux.HandleFunc("GET /api/v1/x", ...) and
// s.mux.Handle("POST /api/v1/y", ...) — capturing the "METHOD /path" literal.
var routeRe = regexp.MustCompile(`\.(?:HandleFunc|Handle)\("([A-Z]+ /[^"]+)"`)

// RegisteredRoutes extracts every route literal registered in server.go.
func RegisteredRoutes(serverGoPath string) ([]string, error) {
	b, err := os.ReadFile(serverGoPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range routeRe.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out, nil
}
```

- [ ] **Step 3: Write the failing completeness test**

```go
package contract

import (
	"path/filepath"
	"strings"
	"testing"
)

// Routes that intentionally have no feature row (auth flows, internal
// gateway→CP ingest, machine-only). Each MUST carry a reason.
var exemptRoutes = map[string]string{
	"GET /auth/login":              "OIDC login redirect, not a feature surface",
	"GET /auth/callback":           "OIDC callback",
	"GET /auth/logout":             "OIDC logout",
	"POST /auth/backchannel-logout": "OIDC backchannel",
	"POST /auth/token":             "OIDC token exchange",
	"GET /auth/ldap-login":         "LDAP login redirect",
	"POST /auth/ldap-login":        "LDAP login",
	"POST /api/v1/usage":           "internal gateway→CP usage ingest",
	"POST /api/v1/inference-events": "internal gateway→CP audit ingest",
	"GET /api/v1/openapi.json":     "the contract document itself",
}

func TestManifestCoversEveryRegisteredRoute(t *testing.T) {
	feats, err := Load("features.json")
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	inManifest := map[string]bool{}
	for _, f := range feats {
		for _, r := range f.Routes {
			inManifest[r] = true
		}
	}
	serverGo := filepath.Join("..", "..", "go", "controlplane", "server", "server.go")
	routes, err := RegisteredRoutes(serverGo)
	if err != nil {
		t.Fatalf("extract routes: %v", err)
	}
	var missing []string
	for _, r := range routes {
		if inManifest[r] || exemptRoutes[r] != "" {
			continue
		}
		missing = append(missing, r)
	}
	if len(missing) > 0 {
		t.Errorf("%d registered route(s) are neither in features.json nor exempt:\n  %s\n\n"+
			"Add a feature row for each, or add it to exemptRoutes with a reason.",
			len(missing), strings.Join(missing, "\n  "))
	}
}
```

- [ ] **Step 4: Run it — expect FAIL listing the many not-yet-covered routes**

```bash
source /home/andrea/Projects/purser/env.sh
cd tests/contract && go mod tidy && go test ./... -run TestManifestCoversEveryRegisteredRoute -v
```
Expected: FAIL, listing every registered route not in the 5 seed features.

- [ ] **Step 5: Populate `features.json` until green**

Add a feature row (or an `exemptRoutes` entry with a reason) for every route the
failure lists. Re-run until PASS. This is the step that makes "untracked
feature" impossible — the manifest now provably covers the whole route table.

- [ ] **Step 6: Commit**

```bash
git add tests/contract/features.json tests/contract/manifest.go tests/contract/completeness_test.go tests/contract/go.mod tests/contract/go.sum
git commit -s -m "test(contract): feature manifest + completeness meta-test"
```

---

## Task 2: Go contract — routes-exist, openapi, perm, docs axes

**Files:**
- Create: `tests/contract/backend_test.go`
- Modify: none (reads `features.json` via `Load`, reads artifacts by path)

**Interfaces:**
- Consumes: `contract.Load`, `contract.Feature` (Task 1).

- [ ] **Step 1: Write the failing backend-axis test**

```go
package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFeats(t *testing.T) []Feature {
	t.Helper()
	f, err := Load("features.json")
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return f
}

// (a) every manifest route is actually registered in server.go
func TestManifestRoutesAreRegistered(t *testing.T) {
	feats := loadFeats(t)
	serverGo := filepath.Join("..", "..", "go", "controlplane", "server", "server.go")
	reg, err := RegisteredRoutes(serverGo)
	if err != nil {
		t.Fatal(err)
	}
	regSet := map[string]bool{}
	for _, r := range reg {
		regSet[r] = true
	}
	for _, f := range feats {
		for _, r := range f.Routes {
			if !regSet[r] {
				t.Errorf("feature %q lists route %q which is NOT registered in server.go", f.Name, r)
			}
		}
	}
}

// (b) openapi:true features have every route path present in the served openapi.json
func TestOpenAPIFeaturesInServedSpec(t *testing.T) {
	feats := loadFeats(t)
	specPath := filepath.Join("..", "..", "go", "controlplane", "server", "openapi.json")
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatalf("openapi.json invalid: %v", err)
	}
	for _, f := range feats {
		if !f.OpenAPI {
			continue
		}
		for _, r := range f.Routes {
			parts := strings.SplitN(r, " ", 2)
			method, path := strings.ToLower(parts[0]), parts[1]
			ops, ok := spec.Paths[path]
			if !ok {
				t.Errorf("feature %q: path %q missing from openapi.json", f.Name, path)
				continue
			}
			if _, ok := ops[method]; !ok {
				t.Errorf("feature %q: %s %q missing from openapi.json", f.Name, method, path)
			}
		}
	}
}

// (c) perm strings are in the ENFORCED vocabulary (the Perm* constants)
func TestManifestPermsAreEnforced(t *testing.T) {
	feats := loadFeats(t)
	permsGo := filepath.Join("..", "..", "go", "controlplane", "permissions", "permissions.go")
	b, err := os.ReadFile(permsGo)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, f := range feats {
		if f.Perm == nil {
			continue
		}
		// the enforced vocabulary is the set of "..." literals assigned to Perm* consts
		if !strings.Contains(src, `= "`+*f.Perm+`"`) {
			t.Errorf("feature %q: perm %q is not an enforced Perm* constant in permissions.go", f.Name, *f.Perm)
		}
	}
}

// (d) docs pages exist and cite no phantom endpoints
func TestManifestDocsHaveNoPhantomEndpoints(t *testing.T) {
	feats := loadFeats(t)
	serverGo := filepath.Join("..", "..", "go", "controlplane", "server", "server.go")
	reg, _ := RegisteredRoutes(serverGo)
	regPaths := map[string]bool{}
	for _, r := range reg {
		regPaths[strings.SplitN(r, " ", 2)[1]] = true
	}
	for _, f := range feats {
		if f.Docs == nil {
			continue
		}
		docPath := filepath.Join("..", "..", "website", "docs", *f.Docs)
		content, err := os.ReadFile(docPath)
		if err != nil {
			t.Errorf("feature %q: docs page %q not found", f.Name, *f.Docs)
			continue
		}
		// find /api/v1/... and /admin/... endpoint literals cited in the doc
		for _, m := range endpointCiteRe.FindAllStringSubmatch(string(content), -1) {
			cited := m[1]
			// strip trailing punctuation and query
			cited = strings.TrimRight(strings.SplitN(cited, "?", 2)[0], ".,`)")
			if strings.HasPrefix(cited, "/admin/") {
				t.Errorf("feature %q docs %q cites %q — /admin/* endpoints do not exist", f.Name, *f.Docs, cited)
			}
		}
	}
}

// endpointCiteRe matches an API path literal in prose/code: /api/v1/... or /admin/...
var endpointCiteRe = mustCompile(`(/(?:api/v1|admin)/[A-Za-z0-9_/{}-]+)`)
```

(Add `import "regexp"` and a small `mustCompile` helper, or inline
`regexp.MustCompile`. The docs axis is deliberately narrow: it only fails on a
cited endpoint that cannot exist — the `/admin/*` phantom class from the PKI
bug. Extending it to verify every cited `/api/v1/*` path is registered is a
reasonable enhancement but risks false positives on illustrative examples;
keep it to the phantom-prefix check for now.)

- [ ] **Step 2: Run — expect FAIL if any seed row is inconsistent, else PASS**

```bash
cd tests/contract && go test ./... -run 'TestManifest|TestOpenAPI' -v
```
If a real inconsistency exists (e.g. a manifest route not registered), fix the
manifest. If all seed rows are correct, these pass immediately — that is
acceptable for axes whose failing-first was already proven by construction.

- [ ] **Step 3: Prove the perm axis catches the regression**

Temporarily change one feature's `perm` to a served-but-not-enforced string
(e.g. `team:apikeys:manage`), run `TestManifestPermsAreEnforced`, confirm it
FAILS, then revert. This demonstrates the guard against the catalog≠enforced bug.

- [ ] **Step 4: Commit**

```bash
git add tests/contract/backend_test.go
git commit -s -m "test(contract): backend axes — routes, openapi, perm, docs"
```

---

## Task 3: TS contract — client, page, nav axes

**Files:**
- Create: `ui/src/contract/manifest.ts`
- Create: `ui/src/contract/features.contract.test.tsx`
- Modify: `ui/vite.config.ts` — ensure `features.json` is importable (resolve path).

**Interfaces:**
- Consumes: `tests/contract/features.json` (Task 1), the `routes` table from
  `ui/src/router.tsx`, the `PurserApi` interface from `ui/src/api/client.ts`,
  the nav arrays from `ui/src/components/Layout.tsx`, i18n keys from
  `ui/src/i18n/en.ts` and `it.ts`.

**Note:** the reachability sweep already exists
(`ui/src/__tests__/routing.reachability.test.tsx`, built during Tier 0). This
task's `page` axis is the manifest-driven generalization of it — drive the
manifest's `page` values through the same `createMemoryRouter(routes)` technique
and assert each resolves to a non-ComingSoon/non-NotFound element. Reuse that
file's `../api/client` Proxy mock verbatim.

- [ ] **Step 1: Write `manifest.ts`**

```ts
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
```

(If vitest cannot resolve the `../../../tests/...` import, read it with
`fs.readFileSync` + `JSON.parse` in the test setup instead — jsdom tests run in
Node, so `fs` is available.)

- [ ] **Step 2: Write the failing contract test**

```tsx
import { describe, it, expect, vi } from 'vitest';
import { createMemoryRouter } from 'react-router-dom';
import { FEATURES } from './manifest';

vi.mock('../api/client', () => ({
  api: new Proxy({}, { get: () => () => new Promise(() => {}) }),
  makeChat: vi.fn(() => ({ baseUrl: '/v1', streamChat: vi.fn(), listModels: vi.fn(() => new Promise(() => {})) })),
}));

import { routes } from '../router';
import type { PurserApi } from '../api/client';
import * as enModule from '../i18n/en';
import * as itModule from '../i18n/it';

// (client) every manifest client method exists on the PurserApi interface.
// The interface is a type, gone at runtime — so assert against the mock backend
// which implements it, or against the http client's returned object keys.
describe('contract: client methods', () => {
  it('every manifest client method is a real PurserApi method', async () => {
    const { mockBackend } = await import('../mock/wiring');
    const impl = mockBackend as unknown as Record<string, unknown>;
    for (const f of FEATURES) {
      for (const m of f.client) {
        expect(typeof impl[m], `${f.name}: client method ${m}`).toBe('function');
      }
    }
  });
});

// (page) every manifest page resolves to a real route (not ComingSoon/NotFound).
describe('contract: pages reachable', () => {
  for (const f of FEATURES) {
    if (!f.page) continue;
    it(`${f.name}: ${f.page} resolves to a real page`, () => {
      const router = createMemoryRouter(routes, { initialEntries: [f.page!] });
      const leaf = router.state.matches.at(-1);
      const el = leaf?.route.element as { type?: { name?: string } } | undefined;
      const name = el?.type?.name ?? '';
      expect(['ComingSoonPage', 'NotFoundPage'], `${f.name} page ${f.page}`).not.toContain(name);
    });
  }
});

// (nav) every manifest nav key exists in BOTH locales.
describe('contract: nav keys', () => {
  const en = (enModule as { en?: Record<string, string> }).en ?? (enModule as { default?: Record<string, string> }).default!;
  const it2 = (itModule as { it?: Record<string, string> }).it ?? (itModule as { default?: Record<string, string> }).default!;
  for (const f of FEATURES) {
    if (!f.nav) continue;
    it(`${f.name}: nav key ${f.nav} in both locales`, () => {
      expect(en[f.nav!], `en ${f.nav}`).toBeDefined();
      expect(it2[f.nav!], `it ${f.nav}`).toBeDefined();
    });
  }
});
```

(Adjust the `en`/`it` import shape and the route-element name extraction to the
actual exports — verify by reading `ui/src/i18n/en.ts` and how
`routing.reachability.test.tsx` inspects matched routes. The exact reflection
technique for the page element name must match what that existing test does.)

- [ ] **Step 3: Run — expect PASS (seed rows are correct) or FAIL on a real gap**

```bash
cd ui && npm test -- contract
```

- [ ] **Step 4: Prove the page axis catches the regression**

Temporarily point `api-keys` in `router.tsx` back to `<ComingSoonPage />`, run
the test, confirm the `api-keys` page assertion FAILS, then revert. Demonstrates
the guard against the built-but-unrouted bug.

- [ ] **Step 5: Commit**

```bash
git add ui/src/contract/manifest.ts ui/src/contract/features.contract.test.tsx ui/vite.config.ts
git commit -s -m "test(contract): UI axes — client, page, nav from shared manifest"
```

---

## Task 4: E2E harness (Go, ephemeral ports, condition-based readiness)

**Files:**
- Create: `tests/e2e/harness.go`
- Create: `tests/e2e/go.mod` (module `github.com/purser/purser/tests/e2e`)
- Create: `tests/e2e/harness_smoke_test.go` (proves the harness itself boots)

**Interfaces:**
- Produces: `e2e.Stack` with fields `CPBase`, `GatewayBase` (both `http://127.0.0.1:PORT`)
  and methods `Start(t *testing.T) *Stack`, `Stop()`, plus `JoinToken() string`.

- [ ] **Step 1: Write the harness**

```go
package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// freePort asks the OS for an unused TCP port, then releases it. Ephemeral
// ports eliminate the fixed-port collisions (9443, 50151) seen in manual runs.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type Stack struct {
	CPBase      string
	GatewayBase string
	token       string
	procs       []*exec.Cmd
	tmp         string
}

// waitReady polls url until it returns <500 or the deadline passes. Never sleeps
// a fixed duration — this is the condition-based-waiting rule.
func waitReady(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond) // poll interval, not a readiness guess
	}
	t.Fatalf("service at %s not ready within %s", url, timeout)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// tests/e2e -> repo root
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..")
}

func Start(t *testing.T) *Stack {
	t.Helper()
	root := repoRoot(t)
	cpPort, gwPort, grpcPort := freePort(t), freePort(t), freePort(t)
	tmp := t.TempDir()

	s := &Stack{
		CPBase:      fmt.Sprintf("http://127.0.0.1:%d", cpPort),
		GatewayBase: fmt.Sprintf("http://127.0.0.1:%d", gwPort),
		tmp:         tmp,
	}

	// gateway (fail-closed: MUST have API keys)
	gw := exec.Command(filepath.Join(root, "rust", "target", "debug", "purser-gateway"))
	gw.Env = append(os.Environ(),
		"PURSER_GATEWAY_HOST=127.0.0.1",
		fmt.Sprintf("PURSER_GATEWAY_PORT=%d", gwPort),
		"PURSER_GATEWAY_INTERNAL_TOKEN=e2e",
		"PURSER_GATEWAY_API_KEYS=testkey",
	)
	startProc(t, s, gw, filepath.Join(tmp, "gw.log"))

	// control-plane (mock engine, insecure gRPC to native agent)
	cp := exec.Command(filepath.Join(root, "bin", "control-plane"))
	cp.Env = append(os.Environ(),
		fmt.Sprintf("PURSER_ADDR=:%d", cpPort),
		fmt.Sprintf("PURSER_GRPC_ADDR=:%d", grpcPort),
		"PURSER_DB="+filepath.Join(tmp, "reg.db"),
		"PURSER_PKI_DIR="+filepath.Join(tmp, "pki"),
		"PURSER_ENGINE_BACKEND=mock",
		"PURSER_AGENT_GRPC_INSECURE=true",
		"PURSER_GATEWAY_ADDR="+s.GatewayBase,
		"PURSER_GATEWAY_TOKEN=e2e",
	)
	startProc(t, s, cp, filepath.Join(tmp, "cp.log"))

	waitReady(t, s.GatewayBase+"/healthz", 30*time.Second)
	waitReady(t, s.CPBase+"/api/v1/cluster/health", 30*time.Second)
	return s
}

func startProc(t *testing.T, s *Stack, cmd *exec.Cmd, logPath string) {
	t.Helper()
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", cmd.Path, err)
	}
	s.procs = append(s.procs, cmd)
}

func (s *Stack) Stop() {
	// reverse order
	for i := len(s.procs) - 1; i >= 0; i-- {
		if s.procs[i].Process != nil {
			_ = s.procs[i].Process.Kill()
		}
	}
}
```

- [ ] **Step 2: Write the smoke test that proves the harness boots**

```go
package e2e

import "testing"

func TestHarnessBoots(t *testing.T) {
	s := Start(t)
	defer s.Stop()
	if s.CPBase == "" || s.GatewayBase == "" {
		t.Fatal("stack did not report base URLs")
	}
}
```

- [ ] **Step 3: Build binaries, then run the smoke test**

```bash
source /home/andrea/Projects/purser/env.sh
CGO_ENABLED=0 go -C go/controlplane build -o ../../bin/control-plane .
export CARGO_TARGET_DIR=/tmp/purser-shared-target
( cd rust && cargo build -p purser-gateway -p purser-agent )
# stage rust binaries where the harness looks:
mkdir -p rust/target/debug && cp /tmp/purser-shared-target/debug/purser-gateway /tmp/purser-shared-target/debug/purser-agent rust/target/debug/
cd tests/e2e && go mod tidy && go test -run TestHarnessBoots -v
```
Expected: PASS (CP + gateway come up on ephemeral ports, /healthz + /cluster/health respond).

- [ ] **Step 4: Commit**

```bash
git add tests/e2e/harness.go tests/e2e/harness_smoke_test.go tests/e2e/go.mod tests/e2e/go.sum
git commit -s -m "test(e2e): native mock-engine harness — ephemeral ports, readiness poll"
```

---

## Task 5: E2E golden-path + gateway-restart

**Files:**
- Create: `tests/e2e/golden_path_test.go`
- Create: `tests/e2e/gateway_restart_test.go`
- Modify: `tests/e2e/harness.go` — add agent spawn + `JoinToken()` + `RestartGateway()`.

**Interfaces:**
- Consumes: `e2e.Start`, `e2e.Stack` (Task 4).
- Produces: `(*Stack).EnrollAgent(t)`, `(*Stack).DeployModel(t, id)`, `(*Stack).RestartGateway(t)`.

- [ ] **Step 1: Extend the harness with agent enrollment + deploy + restart helpers**

```go
// Add to harness.go.
import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// JoinToken mints a join token via POST /api/v1/join-token.
func (s *Stack) JoinToken(t *testing.T) string {
	t.Helper()
	resp, err := http.Post(s.CPBase+"/api/v1/join-token", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("join-token: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		t.Fatal("empty join token")
	}
	return out.Token
}

// EnrollAgent spawns purser-agent (mock) with the join token and waits for it
// to appear in GET /api/v1/nodes.
func (s *Stack) EnrollAgent(t *testing.T) {
	t.Helper()
	root := repoRoot(t)
	token := s.JoinToken(t)
	agentPort, inferPort := freePort(t), freePort(t)
	// grpc addr of CP: reuse the one Start stored — add a grpcBase field to Stack in Task 4.
	ag := exec.Command(filepath.Join(root, "rust", "target", "debug", "purser-agent"))
	ag.Env = append(os.Environ(),
		fmt.Sprintf("PURSER_AGENT_BIND=127.0.0.1:%d", agentPort),
		fmt.Sprintf("PURSER_INFERENCE_PORT=%d", inferPort),
		"PURSER_CONTROL_PLANE_ADDR="+s.grpcBase, // add grpcBase to Stack
		"PURSER_CLUSTER_ID=default",
		"PURSER_JOIN_TOKEN="+token,
	)
	startProc(t, s, ag, filepath.Join(s.tmp, "agent.log"))
	// wait until enrolled
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.CPBase + "/api/v1/nodes")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if bytes.Contains(b, []byte(`"id"`)) || bytes.Contains(b, []byte(`"node_id"`)) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("agent did not enroll within 30s")
}
```

(Task 4 must store the CP gRPC base — add `grpcBase string` to `Stack` and set
it in `Start` as `fmt.Sprintf("http://127.0.0.1:%d", grpcPort)`. Register a model
and deploy it via `POST /api/v1/models` then `POST /api/v1/models/{id}/deploy` —
use the mock model spec from `tools/e2e_full.sh` as the request body. Add
`DeployModel` and `RestartGateway` helpers following the same pattern;
`RestartGateway` kills the gateway proc and starts a fresh one on the SAME port
env as before.)

- [ ] **Step 2: Write the golden-path test**

```go
package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGoldenPath(t *testing.T) {
	s := Start(t)
	defer s.Stop()
	s.EnrollAgent(t)
	s.DeployModel(t, "tinyllama-1b") // registers + deploys, waits for ACTIVE

	// the whole pipe: a chat completion round-trips through gateway→CP→agent(mock)
	req := `{"model":"tinyllama-1b","messages":[{"role":"user","content":"hi"}],"stream":false}`
	r, _ := http.NewRequest("POST", s.GatewayBase+"/v1/chat/completions", strings.NewReader(req))
	r.Header.Set("Authorization", "Bearer testkey")
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("chat status %d: %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "choices") {
		t.Fatalf("no choices in response: %s", b)
	}
}
```

- [ ] **Step 3: Write the gateway-restart regression test**

```go
func TestGatewayRestartSelfHeals(t *testing.T) {
	s := Start(t)
	defer s.Stop()
	s.EnrollAgent(t)
	s.DeployModel(t, "tinyllama-1b")

	// precondition: /v1/models lists the model
	assertModelListed(t, s, "tinyllama-1b", true)

	// kill + restart the gateway — its route table is in-memory only
	s.RestartGateway(t)

	// the reconcile loop must repopulate routes without an operator re-deploy
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if modelListed(s, "tinyllama-1b") {
			return // self-healed
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("gateway did not self-heal its route table after restart (reconcile loop regression)")
}
```

(Write `assertModelListed` / `modelListed` helpers: GET `{GatewayBase}/v1/models`
with the bearer key, check whether the id is in the `data` array.)

- [ ] **Step 4: Run both**

```bash
cd tests/e2e && go test -run 'TestGoldenPath|TestGatewayRestart' -v
```
Expected: both PASS. (If `TestGatewayRestartSelfHeals` fails, the reconcile-loop
fix regressed — that is the test doing its job.)

- [ ] **Step 5: Commit**

```bash
git add tests/e2e/harness.go tests/e2e/golden_path_test.go tests/e2e/gateway_restart_test.go
git commit -s -m "test(e2e): golden-path + gateway-restart self-heal regression"
```

---

## Task 6: Pre-push hook + Make targets + install script

**Files:**
- Create: `tools/hooks/pre-push`
- Create: `tools/hooks/install.sh`
- Modify: `Makefile` — add `contract`, `e2e`, `verify`, `install-hooks`.

**Interfaces:**
- Consumes: the test packages from Tasks 1-5.

- [ ] **Step 1: Write the pre-push hook**

```bash
#!/usr/bin/env bash
# Fast local gate: runs the contract tests (no stack) before every push.
# Escape hatch: git push --no-verify. Fail-safe: a missing toolchain skips its
# layer rather than blocking with a cryptic error.
set -u
root="$(git rev-parse --show-toplevel)"
fail=0

if command -v go >/dev/null 2>&1; then
  echo "pre-push: contract (Go)…"
  ( cd "$root/tests/contract" && CGO_ENABLED=0 go test ./... ) || fail=1
else
  echo "pre-push: go not found — skipping Go contract tests" >&2
fi

if command -v npm >/dev/null 2>&1 && [ -d "$root/ui/node_modules" ]; then
  echo "pre-push: contract (TS)…"
  ( cd "$root/ui" && npm test -- contract --run ) || fail=1
else
  echo "pre-push: npm or ui/node_modules missing — skipping TS contract tests" >&2
fi

if [ "$fail" -ne 0 ]; then
  echo "" >&2
  echo "pre-push BLOCKED: contract tests failed. Fix them, or bypass with:" >&2
  echo "    git push --no-verify" >&2
  exit 1
fi
```

- [ ] **Step 2: Write the install script**

```bash
#!/usr/bin/env bash
# Activate the versioned hooks directory. Opt-in, run once per clone.
set -eu
root="$(git rev-parse --show-toplevel)"
chmod +x "$root/tools/hooks/pre-push"
git -C "$root" config core.hooksPath tools/hooks
echo "hooks installed: core.hooksPath = tools/hooks"
```

- [ ] **Step 3: Add Make targets**

```makefile
.PHONY: contract e2e verify install-hooks

contract: ## Fast contract tests (what the pre-push hook runs)
	cd tests/contract && CGO_ENABLED=0 go test ./...
	cd ui && npm test -- contract --run

e2e: ## Heavy E2E on a native mock-engine stack (builds binaries first)
	CGO_ENABLED=0 go -C go/controlplane build -o ../../bin/control-plane .
	cd rust && CARGO_TARGET_DIR=/tmp/purser-shared-target cargo build -p purser-gateway -p purser-agent
	mkdir -p rust/target/debug && cp /tmp/purser-shared-target/debug/purser-gateway /tmp/purser-shared-target/debug/purser-agent rust/target/debug/
	cd tests/e2e && go test ./...

verify: contract ## Reproduce CI locally: contract + unit + e2e
	cd go/controlplane && CGO_ENABLED=0 go test ./...
	cd rust && CARGO_TARGET_DIR=/tmp/purser-shared-target cargo test -p purser-gateway
	cd ui && npm test -- --run
	$(MAKE) e2e

install-hooks: ## Activate the local pre-push gate (opt-in, run once)
	bash tools/hooks/install.sh
```

(Match the existing Makefile's target style and `##` help convention if present;
read the current Makefile first and slot these in consistently.)

- [ ] **Step 4: Test the hook end-to-end**

```bash
bash tools/hooks/install.sh
# with a green tree: a dry-run push runs the hook
git push --dry-run 2>&1 | head    # expect the "contract (Go)…/(TS)…" lines
# prove it blocks: temporarily break a manifest row, confirm the hook fails,
# then git push --no-verify --dry-run succeeds, then revert.
```

- [ ] **Step 5: Commit**

```bash
git add tools/hooks/pre-push tools/hooks/install.sh Makefile
git commit -s -m "test(ci-local): pre-push contract gate + make contract/e2e/verify/install-hooks"
```

---

## Task 7: Documentation

**Files:**
- Create: `website/docs/development/testing.md`
- Modify: `website/mkdocs.yml` — add the page to nav.

- [ ] **Step 1: Write the testing guide**

Cover: the layer model (unit / contract / e2e) and *why* contract tests exist
(the junction-gap diagnosis); the manifest and **how to add a row when you add a
feature** (the completeness meta-test will otherwise fail your push); `make
contract` / `make e2e` / `make verify`; `make install-hooks` and the
`--no-verify` escape hatch; the GPU-blocked non-goal (e2e tests the pipe, not
inference quality).

- [ ] **Step 2: Add to mkdocs nav and verify the build**

```bash
source /home/andrea/Projects/purser/env.sh
cd website && mkdocs build --strict
```
Expected: exit 0, the new page in the built site.

- [ ] **Step 3: Commit**

```bash
git add website/docs/development/testing.md website/mkdocs.yml
git commit -s -m "docs(testing): test architecture guide + manifest maintenance"
```

---

## Self-Review Notes

- **Spec coverage:** manifest+meta-test (Task 1) ✓; Go axes routes/openapi/perm/docs
  (Task 2) ✓; TS axes client/page/nav (Task 3) ✓; harness (Task 4) ✓; golden-path
  + gateway-restart (Task 5) ✓; pre-push hook + Make (Task 6) ✓; docs (Task 7) ✓.
  The 5 known-gap guards map to: reachability→Task 3 page axis; permission-catalog→Task 2
  perm axis + the catalog==enforced invariant (fold into Task 2 as an extra assertion
  reading permissions.go's All() vs the Perm* set); gateway-restart→Task 5;
  doc-endpoint→Task 2 docs axis; api-client→Task 3 client axis. **All covered.**
- **`gated` axis:** verified in E2E (Task 5 can add a no-license 402 assertion on a
  gated route) rather than in the fast contract layer — noted as an enhancement,
  not a blocker for the thin slice.
- **Type consistency:** `contract.Feature` (Go) and `Feature` (TS) mirror the same
  JSON keys; `Load`/`FEATURES` are the two loaders; `Stack`/`Start`/`Stop`/
  `EnrollAgent`/`DeployModel`/`RestartGateway`/`JoinToken`/`grpcBase` are consistent
  across Tasks 4-5.
- **DoD "reintroduce each known bug, show red":** Task 2 Step 3 (perm) and Task 3
  Step 4 (page) do this explicitly; gateway-restart (Task 5) IS the reintroduction
  test by construction.
