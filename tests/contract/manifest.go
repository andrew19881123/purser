package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// AnnotationFeature is one row in features.annotations.json.
// This file is HUMAN-OWNED — the test framework and any automated tool NEVER
// write it. It captures only what cannot be derived from the Go source:
//
//   - Name + Routes: the grouping (which routes belong to this named feature).
//     Routes is the join key: when a new route appears in apiRoutes with no
//     matching annotation, TestManifestCoversEveryRegisteredRoute fails with
//     an actionable "add it to features.annotations.json" message.
//   - Client, Page, Nav, Gated: UI axes, knowable only from TS source.
//   - Docs:  the docs page path for this feature (human knowledge).
//   - Perm:  the representative UI-gate permission (human judgment; verified
//     against the Perm* vocabulary by TestManifestPermsAreEnforced).
//
// NOTE: the openapi flag is intentionally absent from this struct.  It is
// derived at test time from the Exempt flag in openapi_registry.go so it
// can never rot independently of the route registration.
type AnnotationFeature struct {
	Name   string   `json:"name"`
	Routes []string `json:"routes"`
	Client []string `json:"client"`
	Page   *string  `json:"page"`
	Nav    *string  `json:"nav"`
	Docs   *string  `json:"docs"`
	Perm   *string  `json:"perm"`
	Gated  bool     `json:"gated"`
}

// Feature is the joined view used by all contract tests: human annotations
// plus the OpenAPI flag derived in memory from the route-registry Exempt field.
// Tests work exclusively with Feature; AnnotationFeature is an IO type.
type Feature struct {
	Name    string
	Routes  []string
	OpenAPI bool // DERIVED: true when every route in this feature is non-exempt
	Client  []string
	Page    *string
	Nav     *string
	Docs    *string
	Perm    *string
	Gated   bool
}

// LoadAnnotations reads the human-owned annotations file and returns the slice
// of annotation rows.  It never modifies the file.
func LoadAnnotations(path string) ([]AnnotationFeature, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []AnnotationFeature
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// JoinFeatures builds the test-visible Feature slice by combining human
// annotations with the in-memory derived exempt map (route → exempt flag).
// A feature's OpenAPI field is true when EVERY route in that feature is
// non-exempt (Exempt:false in openapi_registry.go).
func JoinFeatures(annotations []AnnotationFeature, exempts map[string]bool) []Feature {
	out := make([]Feature, 0, len(annotations))
	for _, a := range annotations {
		inOpenAPI := len(a.Routes) > 0
		for _, r := range a.Routes {
			if exempts[r] {
				inOpenAPI = false
				break
			}
		}
		out = append(out, Feature{
			Name:    a.Name,
			Routes:  a.Routes,
			OpenAPI: inOpenAPI,
			Client:  a.Client,
			Page:    a.Page,
			Nav:     a.Nav,
			Docs:    a.Docs,
			Perm:    a.Perm,
			Gated:   a.Gated,
		})
	}
	return out
}

// ── Route table parsing ──────────────────────────────────────────────────────

// routeRe matches one row of the declarative route table in
// go/controlplane/server/openapi_registry.go, e.g.
//
//	{Method: "GET", Path: "/api/v1/nodes", Tag: "Nodes", ...}
//
// capturing the method and path so they can be recombined into the canonical
// "METHOD /path" literal the manifest uses. The table is the single source of
// truth for both mux registration and OpenAPI generation (it replaced the
// hand-written s.mux.HandleFunc("METHOD /path", ...) block in server.go), so
// the contract harness reads route registration from it.
var routeRe = regexp.MustCompile(`\bMethod:\s*"([A-Z]+)",\s*Path:\s*"(/[^"]+)"`)

// exemptRe extends routeRe to match only rows that carry Exempt: true.
// All routeDef rows are single-line in openapi_registry.go.
var exemptRe = regexp.MustCompile(`\bMethod:\s*"([A-Z]+)",\s*Path:\s*"(/[^"]+)"[^\n]*\bExempt:\s*true`)

// minRegisteredRoutes is the floor below which RegisteredRoutes returns an
// error. Today the table has 127 rows; 50 gives comfortable margin while
// still failing loud if the regex stops matching (e.g. after a reformat).
const minRegisteredRoutes = 50

// RegisteredRoutes extracts every route registered by the control plane from
// the declarative route table (openapi_registry.go). The argument is the path
// to that file. Returns "METHOD /path" strings, sorted.
//
// Returns an error if fewer than minRegisteredRoutes routes are matched; this
// prevents a silently broken regex from making every coverage test trivially
// pass with an empty set (the false-green failure mode).
func RegisteredRoutes(routeTablePath string) ([]string, error) {
	b, err := os.ReadFile(routeTablePath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range routeRe.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1]+" "+m[2])
	}
	if len(out) < minRegisteredRoutes {
		return nil, fmt.Errorf("RegisteredRoutes: matched only %d routes from %s — expected at least %d; the file format may have changed and the regex no longer matches",
			len(out), routeTablePath, minRegisteredRoutes)
	}
	sort.Strings(out)
	return out, nil
}

// RouteExemptMap returns the set of "METHOD /path" strings that are marked
// Exempt: true in openapi_registry.go.  These routes are omitted from the
// served OpenAPI spec (Exempt:true → not in openapi.json).
func RouteExemptMap(routeTablePath string) (map[string]bool, error) {
	b, err := os.ReadFile(routeTablePath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, m := range exemptRe.FindAllStringSubmatch(string(b), -1) {
		out[m[1]+" "+m[2]] = true
	}
	return out, nil
}

// ── RBAC permission parsing ──────────────────────────────────────────────────

// RBACEntry is one row from the routePermission map in rbac_v2.go.
type RBACEntry struct {
	Method    string // HTTP method ("GET", "POST", "PUT", "DELETE")
	Path      string // route path pattern (Go 1.22 ServeMux syntax)
	ConstName string // Perm* constant name referenced (sans "registry." prefix)
}

// rbacRouteRe matches one entry in the routePermission map in rbac_v2.go, e.g.
//
//	{http.MethodGet, "/api/v1/nodes"}: registry.PermTeamMetricsView,
//
// Group 1: method title suffix (e.g. "Get", "Post", "Delete").
// Group 2: path pattern.
// Group 3: Perm* constant name (sans "registry." prefix).
var rbacRouteRe = regexp.MustCompile(`\{http\.Method(\w+),\s*"(/[^"]+)"\}:\s*registry\.(\w+)`)

// permConstRe matches a Perm* constant declaration in permissions.go, e.g.
//
//	PermTeamMetricsView = "team:metrics:view"
//
// Group 1: constant name. Group 2: string value.
var permConstRe = regexp.MustCompile(`\b(Perm\w+)\s*=\s*"([^"]+)"`)

// minRBACEntries is the floor below which ParseRBACEntries returns an error.
// Today routePermission has 26 entries; 15 gives comfortable margin.
const minRBACEntries = 15

// ParseRBACEntries extracts every route→permission entry from rbac_v2.go.
// The http.Method* suffix (e.g. "Get", "Post") is uppercased to the canonical
// HTTP method string ("GET", "POST"), matching the "METHOD /path" convention.
//
// Returns an error if fewer than minRBACEntries entries are matched; this
// prevents a silently broken regex from letting TestRoutePermsAreValidConstants
// trivially pass with zero entries to check.
func ParseRBACEntries(rbacPath string) ([]RBACEntry, error) {
	b, err := os.ReadFile(rbacPath)
	if err != nil {
		return nil, err
	}
	var out []RBACEntry
	for _, m := range rbacRouteRe.FindAllStringSubmatch(string(b), -1) {
		// http.MethodGet → "GET", http.MethodPost → "POST", etc.
		out = append(out, RBACEntry{
			Method:    strings.ToUpper(m[1]),
			Path:      m[2],
			ConstName: m[3],
		})
	}
	if len(out) < minRBACEntries {
		return nil, fmt.Errorf("ParseRBACEntries: matched only %d entries from %s — expected at least %d; the file format may have changed and the regex no longer matches",
			len(out), rbacPath, minRBACEntries)
	}
	return out, nil
}

// minPermConstants is the floor below which ParsePermConstants returns an error.
// Today permissions.go declares 22 Perm* constants; 15 gives comfortable margin.
const minPermConstants = 15

// ParsePermConstants extracts every Perm* = "value" constant from permissions.go.
// Returns a map of constant-name → permission-string.
//
// Returns an error if fewer than minPermConstants constants are matched; this
// prevents a silently broken regex from making the vocabulary-check tests
// trivially pass with an empty set.
func ParsePermConstants(permsPath string) (map[string]string, error) {
	b, err := os.ReadFile(permsPath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, m := range permConstRe.FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = m[2]
	}
	if len(out) < minPermConstants {
		return nil, fmt.Errorf("ParsePermConstants: matched only %d constants from %s — expected at least %d; the file format may have changed and the regex no longer matches",
			len(out), permsPath, minPermConstants)
	}
	return out, nil
}

// RoutePermStrings composes ParseRBACEntries and ParsePermConstants to produce
// a map of "METHOD /path" → permission-string for every route that has an RBAC
// entry.  Entries whose constant name cannot be resolved in permissions.go are
// silently omitted; callers that need to detect unresolved constants should use
// ParseRBACEntries + ParsePermConstants directly (see TestRoutePermsAreValidConstants).
func RoutePermStrings(rbacPath, permsPath string) (map[string]string, error) {
	entries, err := ParseRBACEntries(rbacPath)
	if err != nil {
		return nil, err
	}
	consts, err := ParsePermConstants(permsPath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if v, ok := consts[e.ConstName]; ok {
			out[e.Method+" "+e.Path] = v
		}
	}
	return out, nil
}
