// Package server — rbac_catalog_test.go is a WHITE-BOX test (package server)
// guarding the single most important RBAC correctness invariant:
//
//	The permission strings ENFORCED by the route→permission map MUST all be
//	part of the catalog SERVED by GET /api/v1/platform/permissions.
//
// If they diverge, an operator building a custom role from the served catalog
// picks strings that enforcement never checks, and the role grants nothing.
// See permissions/permissions.go (catalog) and permissions/engine.go (roles).
package server

import (
	"testing"

	"github.com/purser/purser/go/controlplane/permissions"
)

// catalogSet returns the set of permission keys served by GET /platform/permissions.
func catalogSet() map[string]bool {
	set := make(map[string]bool)
	for _, d := range permissions.All() {
		set[d.Key] = true
	}
	return set
}

// TestRoutePermissions_AllInServedCatalog asserts that every distinct permission
// string referenced by the route→permission map is discoverable in the served
// catalog. This is the regression guard for the divergent-vocabulary bug: before
// the fix, routes required e.g. "team:keys:create" while the catalog only served
// "team:apikeys:manage", so a role built from the catalog could never satisfy the
// route.
func TestRoutePermissions_AllInServedCatalog(t *testing.T) {
	catalog := catalogSet()
	for rk, required := range routePermission {
		if !catalog[required] {
			t.Errorf("route %s %s enforces permission %q which is NOT served by the catalog (GET /platform/permissions) — operators cannot grant it",
				rk.method, rk.path, required)
		}
	}
}
