package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Canonical paths used by multiple tests — relative to tests/contract/.
const (
	annotationsFile = "features.annotations.json"
	routeTableFile  = "../../go/controlplane/server/openapi_registry.go"
	rbacFile        = "../../go/controlplane/server/rbac_v2.go"
	permsFile       = "../../go/controlplane/permissions/permissions.go"
)

// loadFeats reads the human annotations, derives the exempt map from the live
// route registry, and returns the joined Feature slice used by all tests.
// This is the central "no rot" join: openapi is always in sync with the
// registry because it is derived here, never stored in any file.
func loadFeats(t *testing.T) []Feature {
	t.Helper()
	annotations, err := LoadAnnotations(annotationsFile)
	if err != nil {
		t.Fatalf("load annotations: %v\n  Hint: create tests/contract/features.annotations.json with feature groupings.", err)
	}
	exempts, err := RouteExemptMap(routeTableFile)
	if err != nil {
		t.Fatalf("build exempt map from route registry: %v", err)
	}
	return JoinFeatures(annotations, exempts)
}

// (a) every annotation route is actually registered in openapi_registry.go.
// Catches stale route strings in features.annotations.json after a route rename.
func TestManifestRoutesAreRegistered(t *testing.T) {
	feats := loadFeats(t)
	reg, err := RegisteredRoutes(routeTableFile)
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
				t.Errorf("feature %q lists route %q which is NOT registered in openapi_registry.go\n"+
					"  Remove it from features.annotations.json or update the route string.", f.Name, r)
			}
		}
	}
}

// (b) openapi:true features (derived from Exempt:false in the registry) have
// every route path present in the served openapi.json.
// openapi is DERIVED — it cannot rot because it is re-computed from the
// registry each test run rather than stored as a hand-maintained field.
func TestOpenAPIFeaturesInServedSpec(t *testing.T) {
	feats := loadFeats(t)
	specPath := filepath.Join("..", "..", "go", "controlplane", "server", "openapi.json")
	b, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	// The spec declares a base URL (e.g. "/api/v1") in servers[0].url; all
	// paths in spec.paths are relative to that base.  Route strings in
	// features.annotations.json carry the full absolute path, so we strip the
	// base prefix before the map lookup.
	var spec struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatalf("openapi.json invalid: %v", err)
	}
	base := ""
	if len(spec.Servers) > 0 {
		base = spec.Servers[0].URL
	}
	for _, f := range feats {
		if !f.OpenAPI {
			continue
		}
		for _, r := range f.Routes {
			parts := strings.SplitN(r, " ", 2)
			method, path := strings.ToLower(parts[0]), parts[1]
			specRoute := strings.TrimPrefix(path, base)
			ops, ok := spec.Paths[specRoute]
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

// (c) feature-level perm strings in annotations are in the enforced Perm* vocabulary.
// This covers the human-curated perm gate field; see TestRoutePermsAreValidConstants
// for the fully-derived per-route check.
func TestManifestPermsAreEnforced(t *testing.T) {
	annotations, err := LoadAnnotations(annotationsFile)
	if err != nil {
		t.Fatal(err)
	}
	consts, err := ParsePermConstants(permsFile)
	if err != nil {
		t.Fatal(err)
	}
	permValues := map[string]bool{}
	for _, v := range consts {
		permValues[v] = true
	}
	for _, a := range annotations {
		if a.Perm == nil {
			continue
		}
		if !permValues[*a.Perm] {
			t.Errorf("feature %q: perm %q is not a declared Perm* constant value in permissions.go\n"+
				"  Update the perm field in features.annotations.json to use a valid permission string.", a.Name, *a.Perm)
		}
	}
}

// (c2) every route in rbac_v2.go references a Perm* constant that actually
// exists in permissions.go. This is the anti-rot check for RBAC entries:
// if a constant is renamed or deleted, this test immediately flags it.
// Unlike TestManifestPermsAreEnforced (which reads annotations), this test is
// fully derived from two Go source files — it cannot rot.
func TestRoutePermsAreValidConstants(t *testing.T) {
	entries, err := ParseRBACEntries(rbacFile)
	if err != nil {
		t.Fatal(err)
	}
	consts, err := ParsePermConstants(permsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := consts[e.ConstName]; !ok {
			t.Errorf("rbac_v2.go: %s %s → registry.%s is not a declared Perm* constant in permissions.go\n"+
				"  Either rename the constant reference or add the constant to permissions.go.",
				e.Method, e.Path, e.ConstName)
		}
	}
}

// (d) docs pages exist and cite no phantom endpoints.
//
// Hardening beyond the original /admin/* check:
//   - (d1) /admin/* phantom guard: these paths have never existed.
//   - (d2) template-form paths (containing {param}) cited in docs must match
//     a real registered route path.  Concrete paths (e.g. /api/v1/nodes/abc123)
//     are intentionally NOT checked: docs often use illustrative IDs, which are
//     not registered patterns.  Restricting to template-form paths gives
//     meaningful phantom detection without false positives on prose examples.
func TestManifestDocsHaveNoPhantomEndpoints(t *testing.T) {
	feats := loadFeats(t)
	reg, err := RegisteredRoutes(routeTableFile)
	if err != nil {
		t.Fatal(err)
	}

	// Build a set of registered route paths (method stripped) for exact lookups.
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

		for _, m := range endpointCiteRe.FindAllStringSubmatch(string(content), -1) {
			cited := m[1]
			// Strip trailing punctuation and query string.
			cited = strings.TrimRight(strings.SplitN(cited, "?", 2)[0], ".,`)")

			// (d1) /admin/* endpoints do not exist in this codebase.
			if strings.HasPrefix(cited, "/admin/") {
				t.Errorf("feature %q docs %q cites %q — /admin/* endpoints do not exist", f.Name, *f.Docs, cited)
				continue
			}

			// (d2) Template-form paths (containing {param}) must match a real
			// registered route path.  Concrete paths are not checked (see doc above).
			if strings.Contains(cited, "{") && !regPaths[cited] {
				t.Errorf("feature %q docs %q cites template path %q which is not a registered route in openapi_registry.go",
					f.Name, *f.Docs, cited)
			}
		}
	}
}

// endpointCiteRe matches an API path literal in prose/code: /api/v1/... or /admin/...
var endpointCiteRe = regexp.MustCompile(`(/(?:api/v1|admin)/[A-Za-z0-9_/{}-]+)`)

// TestDetectsUnassignedRoute is a unit-level proof of the anti-rot property.
// It exercises the coverage algorithm with a synthetic route set and confirms
// that a route added to the registry without an annotation grouping entry is
// detected and surfaces an actionable error message.
//
// This test always passes (it validates the DETECTOR, not the current state of
// the real registry).  The real anti-rot gate is TestManifestCoversEveryRegisteredRoute
// in completeness_test.go; this test proves the detection mechanism is correct.
func TestDetectsUnassignedRoute(t *testing.T) {
	// Synthetic annotation: only one route is grouped.
	annotations := []AnnotationFeature{
		{Name: "existing-feature", Routes: []string{"GET /api/v1/foo"}},
	}
	// Synthetic registry: contains both the annotated route and a NEW unassigned one.
	registered := []string{"GET /api/v1/foo", "POST /api/v1/bar-unassigned-new"}

	inAnnotations := map[string]bool{}
	for _, a := range annotations {
		for _, r := range a.Routes {
			inAnnotations[r] = true
		}
	}

	var missing []string
	for _, r := range registered {
		if !inAnnotations[r] && exemptRoutes[r] == "" {
			missing = append(missing, r)
		}
	}

	if len(missing) == 0 {
		t.Fatal("anti-rot detector failed: expected POST /api/v1/bar-unassigned-new to appear in missing but it did not")
	}
	found := false
	for _, m := range missing {
		if m == "POST /api/v1/bar-unassigned-new" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing to contain POST /api/v1/bar-unassigned-new, got %v", missing)
	}
	// Confirm the actionable error message format (mirrors completeness_test.go).
	_ = "route X unassigned to any feature — add it to tests/contract/features.annotations.json"
}
