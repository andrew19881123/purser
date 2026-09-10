package server_test

// openapi_consistency_test.go — keeps openapi.yaml and openapi.json in step.
//
// WHY THIS TEST EXISTS
// openapi.json is //go:embed-ed into the binary (server.go) and served verbatim
// at GET /api/v1/openapi.json, so the JSON — not the YAML — is the contract that
// clients and SDK generators actually read. openapi.yaml is the file humans edit.
// Nothing generates one from the other: server.go's handleOpenAPISpec comment
// calls the JSON "generated from openapi.yaml", but no Makefile target, script
// or CI step performs that generation, so the two files drift with nothing
// failing. This test is what makes the drift fail. It was written after finding
// that GET /models/{id} had been added to the YAML and never mirrored into the
// JSON, leaving a real, routed endpoint absent from the published contract.
//
// DIRECTION: openapi.yaml is the SOURCE OF TRUTH and openapi.json is derived,
// so a failure is normally fixed by bringing the JSON up to the YAML — never by
// trimming the YAML to match the JSON. Three reasons:
//  1. Stated intent — server.go already declares the JSON generated from the
//     YAML. No generator exists, but the intended direction is unambiguous.
//  2. Observed authorship — drift has run YAML-ahead: edits land in the YAML and
//     the JSON is forgotten. The YAML is where humans work.
//  3. Shape — the YAML is authored rather than emitted. It uses folded block
//     scalars and comments, which no JSON-to-YAML generator produces.
//
// THE EXCEPTION, AND WHY IT IS HERE
// The YAML is authoritative only where it describes routes the server really
// serves. If this test fails for a path the server does not register, then the
// YAML is the wrong side: delete the phantom from the YAML instead of copying it
// into the JSON. Check the route table in server.go before syncing either way.
//
// This is not defensive boilerplate. "The fuller file is the correct one" is an
// assumption, and acting on it blindly would publish whatever the YAML happens
// to claim. For the divergence that prompted this test, GET /models/{id} was
// confirmed to be genuinely routed (server.go registers it to handleGetModel)
// *before* the JSON was corrected. Had it not been routed, the correct fix would
// have been the opposite one, and syncing without checking would have written a
// phantom endpoint into the contract the server serves.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPIServedJSONMatchesYAML asserts that the spec the server actually
// serves is structurally identical to the authored openapi.yaml.
func TestOpenAPIServedJSONMatchesYAML(t *testing.T) {
	var servedDoc map[string]any
	if err := json.Unmarshal(servedOpenAPISpec(t), &servedDoc); err != nil {
		t.Fatalf("served openapi.json is not valid JSON: %v", err)
	}
	authoredDoc := loadYAMLWithJSONTypes(t, "openapi.yaml")

	var diffs []string
	walkSpec(authoredDoc, servedDoc, nil, &diffs)
	if len(diffs) == 0 {
		return
	}
	sort.Strings(diffs)
	t.Errorf("openapi.yaml and the served openapi.json disagree in %d place(s):\n%s\n\n"+
		"openapi.yaml is the source of truth — bring openapi.json up to it, UNLESS the\n"+
		"YAML describes a path the server does not route, in which case the YAML is\n"+
		"wrong and the phantom should be deleted from it. Check server.go's route\n"+
		"registrations before syncing. See the comment at the top of this file.",
		len(diffs), strings.Join(diffs, "\n"))
}

// servedOpenAPISpec returns the exact bytes the server hands out at
// GET /api/v1/openapi.json, i.e. the //go:embed-ed openapi.json. It goes through
// the handler rather than reading the file so the test stays honest about what
// ships: a corrected file on disk paired with a stale binary would otherwise
// pass. (openAPISpec itself is unexported, so this external test package reaches
// the embedded bytes through the endpoint that serves them.)
func servedOpenAPISpec(t *testing.T) []byte {
	t.Helper()
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/openapi.json: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("served openapi.json is empty")
	}
	return body
}

// loadYAMLWithJSONTypes parses a YAML file and re-decodes it through
// encoding/json so both documents end up in the same Go types. Without the
// round-trip every numeric example diverges spuriously: YAML decodes 1000 to an
// int, encoding/json decodes it to float64.
func loadYAMLWithJSONTypes(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var viaYAML map[string]any
	if err := yaml.Unmarshal(raw, &viaYAML); err != nil {
		t.Fatalf("%s is not valid YAML: %v", path, err)
	}
	bridge, err := json.Marshal(viaYAML)
	if err != nil {
		t.Fatalf("re-encode %s as JSON: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(bridge, &out); err != nil {
		t.Fatalf("re-decode %s: %v", path, err)
	}
	return out
}

const (
	authored = "openapi.yaml"
	served   = "the served openapi.json"
)

// walkSpec appends a description of every place the two documents disagree.
// It reports all of them rather than stopping at the first: by the time anyone
// looks, silent drift is usually several edits deep, and surfacing one
// divergence per run would make it tedious to unpick.
func walkSpec(want, got any, path []string, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %s has an object, %s has %s",
				describe(path), authored, served, goType(got)))
			return
		}
		for _, k := range sortedUnion(w, g) {
			_, inAuthored := w[k]
			_, inServed := g[k]
			switch {
			case !inServed:
				*out = append(*out, missingFrom(child(path, k), authored, served))
			case !inAuthored:
				*out = append(*out, missingFrom(child(path, k), served, authored))
			default:
				walkSpec(w[k], g[k], child(path, k), out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %s has a list, %s has %s",
				describe(path), authored, served, goType(got)))
			return
		}
		if len(w) != len(g) {
			*out = append(*out, fmt.Sprintf("%s: %s has %d item(s), %s has %d",
				describe(path), authored, len(w), served, len(g)))
			return
		}
		for i := range w {
			walkSpec(w[i], g[i], child(path, fmt.Sprintf("[%d]", i)), out)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			*out = append(*out, fmt.Sprintf("%s: %s has %s, %s has %s",
				describe(path), authored, scalar(want), served, scalar(got)))
		}
	}
}

// missingFrom phrases an absent key. Divergences inside "paths" are named as the
// HTTP operation an SDK generator would fail to emit, because that is the
// actionable form: "operation GET /models/{id} is missing from the served spec"
// says what is broken, where "paths./models/{id}.get is absent" does not.
func missingFrom(path []string, presentIn, absentFrom string) string {
	if len(path) == 3 && path[0] == "paths" && isHTTPMethod(path[2]) {
		return fmt.Sprintf("operation %s %s: declared in %s, MISSING from %s",
			strings.ToUpper(path[2]), path[1], presentIn, absentFrom)
	}
	if len(path) == 2 && path[0] == "paths" {
		return fmt.Sprintf("path %s: declared in %s, entirely MISSING from %s",
			path[1], presentIn, absentFrom)
	}
	return fmt.Sprintf("%s: present in %s, MISSING from %s", describe(path), presentIn, absentFrom)
}

// child returns path + seg in a fresh slice. Appending to the caller's slice
// would let sibling recursions share a backing array and overwrite each other.
func child(path []string, seg string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = seg
	return out
}

// describe renders a location as a dotted trail, keeping list indices attached
// to the key they index ("security[0]", not "security.[0]").
func describe(path []string) string {
	if len(path) == 0 {
		return "(document root)"
	}
	var b strings.Builder
	for i, seg := range path {
		if i > 0 && !strings.HasPrefix(seg, "[") {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

// scalar renders a leaf value compactly, trimming long strings so a description
// mismatch reports the divergence rather than two walls of prose.
func scalar(v any) string {
	if s, ok := v.(string); ok {
		if len(s) > 60 {
			return fmt.Sprintf("%q (truncated, %d bytes)", s[:60], len(s))
		}
		return fmt.Sprintf("%q", s)
	}
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%v", v)
}

func goType(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func sortedUnion(a, b map[string]any) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isHTTPMethod(s string) bool {
	switch s {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	}
	return false
}
