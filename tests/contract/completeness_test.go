package contract

import (
	"strings"
	"testing"
)

// exemptRoutes lists routes that intentionally have no feature annotation
// (auth flows, internal gateway→CP ingest, machine-only probes).
// Each entry MUST carry a human-readable reason.
// These routes are Exempt:true in openapi_registry.go AND excluded from the
// feature grouping requirement.
var exemptRoutes = map[string]string{
	"GET /auth/login":                                 "OIDC login redirect, not a feature surface",
	"GET /auth/callback":                              "OIDC callback",
	"GET /auth/logout":                                "OIDC logout",
	"POST /auth/backchannel-logout":                   "OIDC backchannel",
	"POST /auth/token":                                "OIDC token exchange",
	"GET /auth/ldap-login":                            "LDAP login redirect",
	"POST /auth/ldap-login":                           "LDAP login",
	"POST /api/v1/usage":                              "internal gateway→CP usage ingest",
	"POST /api/v1/inference-events":                   "internal gateway→CP audit ingest",
	"GET /api/v1/openapi.json":                        "the contract document itself",
	"POST /api/v1/enrollment/renew":                   "internal agent→CP certificate renewal, not a UI feature",
	"POST /api/v1/platform/dataplanes/{id}/heartbeat": "internal data-plane→CP heartbeat, not a UI feature",
	"GET /api/v1/cluster/health":                      "K8s liveness probe, not a UI feature surface",
	"GET /api/v1/platform/health":                     "K8s-compatible liveness probe, unauthenticated",
	"GET /api/v1/platform/status":                     "admin-only platform summary, not surfaced as a UI feature",
	"POST /api/v1/ldap/test":                          "LDAP connectivity test, CLI/admin-only, no UI client method",
}

// TestManifestCoversEveryRegisteredRoute is the anti-rot completeness gate.
//
// It reads every non-exempt route from openapi_registry.go (the authoritative
// route table) and verifies each one is assigned to a feature in
// features.annotations.json.  A route with NO grouping entry causes this test
// to fail with an actionable message, which is the correct response when a new
// route is added to apiRoutes: the developer adds ONE annotation line and the
// test goes green.  No generated file is ever clobbered by this process.
//
// To add a new route without a feature:
//   - If it genuinely has no UI surface (internal, probe), add it to exemptRoutes above.
//   - Otherwise, assign it to a feature in tests/contract/features.annotations.json.
func TestManifestCoversEveryRegisteredRoute(t *testing.T) {
	annotations, err := LoadAnnotations(annotationsFile)
	if err != nil {
		t.Fatalf("load annotations: %v\n  Hint: create tests/contract/features.annotations.json to fix.", err)
	}
	inAnnotations := map[string]bool{}
	for _, a := range annotations {
		for _, r := range a.Routes {
			inAnnotations[r] = true
		}
	}
	routes, err := RegisteredRoutes(routeTableFile)
	if err != nil {
		t.Fatalf("extract routes from openapi_registry.go: %v", err)
	}
	var missing []string
	for _, r := range routes {
		if inAnnotations[r] || exemptRoutes[r] != "" {
			continue
		}
		missing = append(missing, r)
	}
	if len(missing) > 0 {
		t.Errorf("%d registered route(s) are not assigned to any feature and not listed in exemptRoutes:\n  %s\n\n"+
			"For each route above, either:\n"+
			"  • Add it to a feature in tests/contract/features.annotations.json, or\n"+
			"  • Add it to exemptRoutes in completeness_test.go with a reason.",
			len(missing), strings.Join(missing, "\n  "))
	}
}
