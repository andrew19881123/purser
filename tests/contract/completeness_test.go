package contract

import (
	"path/filepath"
	"strings"
	"testing"
)

// Routes that intentionally have no feature row (auth flows, internal
// gateway→CP ingest, machine-only). Each MUST carry a reason.
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
