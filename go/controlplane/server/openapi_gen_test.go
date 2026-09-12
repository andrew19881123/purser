package server

// openapi_gen_test.go — guards the GENERATED OpenAPI contract.
//
// These tests replace the old openapi_consistency_test.go, which policed drift
// between a hand-written openapi.yaml and a hand-written openapi.json. That
// premise is gone: openapi.json is now generated from the route table
// (apiRoutes) + openapi.base.json, and openapi.yaml has been retired. The two
// guarantees we need now are:
//
//   1. COVERAGE (the bug this whole change fixes): every non-exempt registered
//      route appears in the generated spec, with the right method and path.
//      Historically only 22 of 127 routes were documented; this test fails if
//      that regresses.
//   2. FRESHNESS (the standard `go generate` check): the committed openapi.json
//      equals what the generator produces right now, so a route added to the
//      table without regenerating fails the build instead of silently shipping
//      a stale contract.
//
// This is an internal (white-box) test package so it can read apiRoutes and
// call GenerateOpenAPISpec directly.

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// generatedSpec parses the freshly generated document once per test.
func generatedSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := GenerateOpenAPISpec()
	if err != nil {
		t.Fatalf("GenerateOpenAPISpec: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("generated spec is not valid JSON: %v", err)
	}
	return doc
}

// specServerBase returns servers[0].url (e.g. "/api/v1") from a parsed doc.
func specServerBase(doc map[string]any) string {
	servers, _ := doc["servers"].([]any)
	if len(servers) == 0 {
		return ""
	}
	s0, _ := servers[0].(map[string]any)
	url, _ := s0["url"].(string)
	return url
}

// TestOpenAPICoversAllRoutes asserts that every non-exempt route in apiRoutes
// appears in the generated spec under the correct path and method. This is the
// FLOOR deliverable: 127 registered routes, 16 exempt, so 111 must be present.
func TestOpenAPICoversAllRoutes(t *testing.T) {
	doc := generatedSpec(t)
	base := specServerBase(doc)
	paths, _ := doc["paths"].(map[string]any)
	if paths == nil {
		t.Fatal("generated spec has no paths object")
	}

	var missing []string
	nonExempt := 0
	for _, rt := range apiRoutes {
		if rt.Exempt {
			continue
		}
		nonExempt++
		specPath := strings.TrimPrefix(rt.Path, base)
		item, ok := paths[specPath].(map[string]any)
		if !ok {
			missing = append(missing, rt.Method+" "+rt.Path+" (path absent)")
			continue
		}
		if _, ok := item[strings.ToLower(rt.Method)]; !ok {
			missing = append(missing, rt.Method+" "+rt.Path+" (method absent)")
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d of %d non-exempt routes are MISSING from the generated OpenAPI spec:\n  %s\n\n"+
			"Every registered, non-exempt route must appear in the contract. Add the route "+
			"to apiRoutes (openapi_registry.go) and regenerate.",
			len(missing), nonExempt, strings.Join(missing, "\n  "))
	}

	// Sanity: at least the historical floor of coverage. Guards against an
	// accidental mass-exemption silently shrinking the contract.
	if nonExempt < 100 {
		t.Errorf("only %d non-exempt routes found; expected ~111. Did routes get "+
			"wrongly marked Exempt?", nonExempt)
	}
}

// TestOpenAPIExemptRoutesAreAbsent asserts the flip side: routes flagged Exempt
// (auth flows, internal ingest, health, the spec endpoint itself) are NOT
// emitted into the contract. Exemption is meaningful only if it is honoured.
func TestOpenAPIExemptRoutesAreAbsent(t *testing.T) {
	doc := generatedSpec(t)
	base := specServerBase(doc)
	paths, _ := doc["paths"].(map[string]any)

	for _, rt := range apiRoutes {
		if !rt.Exempt {
			continue
		}
		specPath := strings.TrimPrefix(rt.Path, base)
		item, ok := paths[specPath].(map[string]any)
		if !ok {
			continue // whole path absent — fine
		}
		if _, present := item[strings.ToLower(rt.Method)]; present {
			t.Errorf("exempt route %s %s is present in the generated spec but must not be",
				rt.Method, rt.Path)
		}
	}
}

// TestOpenAPISpecIsFresh is the standard generated-file freshness guard: the
// committed openapi.json must equal what the generator produces now. If this
// fails, run `go generate ./server/...` and commit the result.
func TestOpenAPISpecIsFresh(t *testing.T) {
	want, err := GenerateOpenAPISpec()
	if err != nil {
		t.Fatalf("GenerateOpenAPISpec: %v", err)
	}
	got, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatalf("read committed openapi.json: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("committed openapi.json is STALE — it does not match the generator output.\n"+
			"Regenerate and commit:\n\n"+
			"    go generate ./server/...\n\n"+
			"(committed %d bytes, generator produces %d bytes)", len(got), len(want))
	}
}

// TestOpenAPIGeneratedIsValid does light structural validation of the generated
// document: version, required top-level keys, and that every operation has a
// unique operationId and a responses object.
func TestOpenAPIGeneratedIsValid(t *testing.T) {
	doc := generatedSpec(t)

	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.") {
		t.Errorf("openapi version = %q, want 3.x", v)
	}
	for _, k := range []string{"info", "servers", "paths", "components"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("generated spec missing top-level %q", k)
		}
	}

	paths, _ := doc["paths"].(map[string]any)
	methods := map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true}
	seenOpID := map[string]string{}
	for p, itemAny := range paths {
		item, _ := itemAny.(map[string]any)
		for method, opAny := range item {
			if !methods[method] {
				continue
			}
			op, _ := opAny.(map[string]any)
			if _, ok := op["responses"]; !ok {
				t.Errorf("%s %s: no responses object", strings.ToUpper(method), p)
			}
			oid, _ := op["operationId"].(string)
			if oid == "" {
				t.Errorf("%s %s: no operationId", strings.ToUpper(method), p)
				continue
			}
			if prev, dup := seenOpID[oid]; dup {
				t.Errorf("duplicate operationId %q: %s and %s %s", oid, prev, strings.ToUpper(method), p)
			}
			seenOpID[oid] = strings.ToUpper(method) + " " + p
		}
	}
}
