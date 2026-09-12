package server

// openapi_gen.go — generates the OpenAPI 3.0.3 contract from the declarative
// route table (apiRoutes, openapi_registry.go) merged with the curated
// enrichment in openapi.base.json.
//
// WHY THIS EXISTS
// openapi.json used to be hand-maintained, and a hand-maintained contract
// drifts: only 22 of 127 registered routes were documented, so an SDK
// generated from the contract saw a fraction of the real API. Generating the
// document from the same table that registers the routes makes that class of
// drift impossible — every registered, non-exempt route is in the contract by
// construction, and a freshness test (openapi_gen_test.go) fails the build if
// the committed openapi.json falls out of step with the generator.
//
// FIDELITY LADDER
//   - Every non-exempt route gets an operation: correct path + method, path
//     parameters derived from {segment}s, a resource tag, a summary, and a
//     stable operationId.
//   - Routes listed in openapi.base.json's "operations" map (the 20 that were
//     hand-authored with real request/response schemas) keep those schemas
//     verbatim; the generator only ensures they carry a tag and operationId.
//   - Every other route is emitted with a GENERIC body (object,
//     additionalProperties:true) and an "x-purser-todo" marker. We deliberately
//     do NOT invent schemas: the handlers marshal anonymous inline structs and
//     map[string]any, so there is no Go type to reflect on. Honesty over
//     coverage — a generic-but-honest body beats a fabricated one.
//
//go:generate go run ../cmd/openapi-gen -out openapi.json

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// openAPIBase is the curated, hand-maintained enrichment: document metadata
// (info/servers/security), the component schemas, and the subset of operations
// that have real request/response schemas. It is NOT the served contract — the
// served contract is generated from it plus the route table.
//
//go:embed openapi.base.json
var openAPIBase []byte

// baseDoc mirrors the shape of openapi.base.json. Everything except operations
// is passed through to the output verbatim (as json.RawMessage) so no numeric
// or ordering drift is introduced on the curated parts.
type baseDoc struct {
	OpenAPI    json.RawMessage            `json:"openapi"`
	Info       json.RawMessage            `json:"info"`
	Servers    json.RawMessage            `json:"servers"`
	Security   json.RawMessage            `json:"security"`
	Components json.RawMessage            `json:"components"`
	Operations map[string]json.RawMessage `json:"operations"`
}

// specTag is one entry of the top-level tags array.
type specTag struct {
	Name string `json:"name"`
}

// specDoc is the assembled output document. Field order here is the key order
// in the emitted JSON (struct fields marshal in declaration order).
type specDoc struct {
	OpenAPI    json.RawMessage                       `json:"openapi"`
	Info       json.RawMessage                       `json:"info"`
	Servers    json.RawMessage                       `json:"servers"`
	Security   json.RawMessage                       `json:"security"`
	Tags       []specTag                             `json:"tags"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components json.RawMessage                       `json:"components"`
}

// GenerateOpenAPISpec builds the served OpenAPI document from the route table
// and openapi.base.json. It is deterministic: the same inputs always produce
// byte-identical output (map keys marshal sorted; the tags slice is built in a
// fixed order), which is what lets the freshness test compare the committed
// openapi.json against a fresh in-memory generation.
func GenerateOpenAPISpec() ([]byte, error) {
	var base baseDoc
	if err := json.Unmarshal(openAPIBase, &base); err != nil {
		return nil, fmt.Errorf("parse openapi.base.json: %w", err)
	}

	// serverBase is the path prefix declared in servers[0].url (e.g. "/api/v1").
	// Paths in the spec are relative to it, so it is trimmed from each route.
	serverBase := firstServerURL(base.Servers)

	paths := map[string]map[string]json.RawMessage{}
	var tags []specTag
	seenTag := map[string]bool{}

	for _, rt := range apiRoutes {
		if rt.Exempt {
			continue // auth flows, internal ingest, health, the spec itself
		}
		if !seenTag[rt.Tag] {
			seenTag[rt.Tag] = true
			tags = append(tags, specTag{Name: rt.Tag})
		}

		specPath := strings.TrimPrefix(rt.Path, serverBase)
		if specPath == "" {
			specPath = "/"
		}
		method := strings.ToLower(rt.Method)

		route := rt.Method + " " + rt.Path
		var op json.RawMessage
		var err error
		if rich, ok := base.Operations[route]; ok {
			op, err = enrichOperation(rich, rt)
		} else {
			op, err = genericOperation(rt, specPath)
		}
		if err != nil {
			return nil, fmt.Errorf("build operation %s: %w", route, err)
		}

		if paths[specPath] == nil {
			paths[specPath] = map[string]json.RawMessage{}
		}
		paths[specPath][method] = op
	}

	doc := specDoc{
		OpenAPI:    base.OpenAPI,
		Info:       base.Info,
		Servers:    base.Servers,
		Security:   base.Security,
		Tags:       tags,
		Paths:      paths,
		Components: base.Components,
	}

	out, err := marshalIndent(doc)
	if err != nil {
		return nil, err
	}
	// Trailing newline so the file is POSIX-clean and diff-friendly.
	return append(out, '\n'), nil
}

// firstServerURL extracts servers[0].url from the raw servers array. Returns ""
// if the array is empty or malformed (paths are then treated as absolute).
func firstServerURL(raw json.RawMessage) string {
	var servers []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &servers); err != nil || len(servers) == 0 {
		return ""
	}
	return servers[0].URL
}

// enrichOperation takes a hand-authored operation from openapi.base.json and
// guarantees it carries a tag and an operationId, without disturbing its
// curated schemas. Numbers are decoded as json.Number so inline examples do
// not drift (e.g. 128.0 stays 128.0, not 128).
func enrichOperation(rich json.RawMessage, rt routeDef) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(rich))
	dec.UseNumber()
	var op map[string]any
	if err := dec.Decode(&op); err != nil {
		return nil, err
	}
	if _, ok := op["tags"]; !ok {
		op["tags"] = []string{rt.Tag}
	}
	if _, ok := op["operationId"]; !ok && rt.OpID != "" {
		op["operationId"] = rt.OpID
	}
	return marshalNoEscape(op)
}

// genericOperation emits an honest placeholder operation for a route that has
// no hand-authored schema: correct method/path/params/tag/summary, a generic
// request body for mutating verbs, generic responses, and an x-purser-todo
// marker recording that the body shape is unspecified.
func genericOperation(rt routeDef, specPath string) (json.RawMessage, error) {
	op := map[string]any{
		"operationId": rt.OpID,
		"summary":     rt.Summary,
		"tags":        []string{rt.Tag},
		// Mirror the document-level security: bearer OR anonymous.
		"security": []any{
			map[string]any{"BearerAuth": []any{}},
			map[string]any{},
		},
		"x-purser-todo": "Request/response body schema is not yet specified in the contract. " +
			"The route is real and served; only its body shape is undocumented. " +
			"Add a hand-authored operation to openapi.base.json to replace this placeholder.",
	}

	if params := pathParameters(specPath); len(params) > 0 {
		op["parameters"] = params
	}

	switch rt.Method {
	case "POST", "PUT", "PATCH":
		op["requestBody"] = map[string]any{
			"required":    false,
			"description": "Request body. Schema not yet specified in the contract.",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":                 "object",
						"additionalProperties": true,
					},
				},
			},
		}
	}

	op["responses"] = map[string]any{
		"200": map[string]any{
			"description": "Successful response. Body schema is not yet specified in the contract.",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":                 "object",
						"additionalProperties": true,
					},
				},
			},
		},
		"default": map[string]any{
			"description": "Error.",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"$ref": "#/components/schemas/Error",
					},
				},
			},
		},
	}

	return marshalNoEscape(op)
}

// pathParameters returns an OpenAPI parameters array (one string path param per
// {segment} in the path), in path order.
func pathParameters(specPath string) []any {
	var params []any
	for _, seg := range strings.Split(specPath, "/") {
		if len(seg) >= 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			name := seg[1 : len(seg)-1]
			params = append(params, map[string]any{
				"name":        name,
				"in":          "path",
				"required":    true,
				"description": "Path parameter: " + name + ".",
				"schema":      map[string]any{"type": "string"},
			})
		}
	}
	return params
}

// marshalNoEscape marshals v without HTML escaping (so "&", "<", ">" survive as
// themselves) and without indentation — the value is embedded as a RawMessage
// and re-indented by the top-level marshal.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder.Encode appends a newline; trim it for a clean RawMessage.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// marshalIndent renders the final document with 2-space indentation and no HTML
// escaping, matching the committed openapi.json byte-for-byte.
func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
