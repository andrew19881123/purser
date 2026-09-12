package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
	routeTable := filepath.Join("..", "..", "go", "controlplane", "server", "openapi_registry.go")
	reg, err := RegisteredRoutes(routeTable)
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
	// The spec declares a base URL (e.g. "/api/v1") in servers[0].url; all
	// paths in spec.paths are relative to that base.  Route strings in
	// features.json carry the full absolute path, so we strip the base prefix
	// before the map lookup.
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
	routeTable := filepath.Join("..", "..", "go", "controlplane", "server", "openapi_registry.go")
	reg, _ := RegisteredRoutes(routeTable)
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
var endpointCiteRe = regexp.MustCompile(`(/(?:api/v1|admin)/[A-Za-z0-9_/{}-]+)`)
