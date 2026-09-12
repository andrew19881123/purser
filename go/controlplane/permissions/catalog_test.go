package permissions_test

import (
	"sort"
	"testing"

	"github.com/purser/purser/go/controlplane/permissions"
)

// catalogKeys returns the set of permission keys served by GET /platform/permissions.
func catalogKeys() map[string]bool {
	set := make(map[string]bool)
	for _, d := range permissions.All() {
		set[d.Key] = true
	}
	return set
}

// enforcedUniverse is every permission string the built-in roles can actually
// grant — i.e. the vocabulary that enforcement (permissions.Has) checks against.
// The single source of truth for "what enforcement recognizes".
func enforcedUniverse() map[string]bool {
	set := make(map[string]bool)
	for _, r := range permissions.SystemRoles() {
		for _, p := range r.Permissions {
			set[p] = true
		}
	}
	return set
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestCatalog_EqualsEnforcedUniverse is the core invariant: the served catalog
// and the enforced vocabulary must be exactly the same set — no orphans in
// either direction. An entry served but not enforceable is a lie to operators;
// a permission enforced but not served is un-grantable via custom roles.
func TestCatalog_EqualsEnforcedUniverse(t *testing.T) {
	catalog := catalogKeys()
	enforced := enforcedUniverse()

	// Served but not enforced (dead catalog entries an operator can never use).
	for k := range catalog {
		if !enforced[k] {
			t.Errorf("catalog serves %q but no built-in role/enforcement recognizes it (orphan served entry)", k)
		}
	}
	// Enforced but not served (permissions no operator can discover/grant).
	for k := range enforced {
		if !catalog[k] {
			t.Errorf("enforcement recognizes %q but the catalog does not serve it (orphan enforced entry)", k)
		}
	}

	if len(catalog) != len(enforced) {
		t.Fatalf("catalog (%d) and enforced universe (%d) differ in size\n  catalog:  %v\n  enforced: %v",
			len(catalog), len(enforced), sortedKeys(catalog), sortedKeys(enforced))
	}
}

// TestCatalog_IsKnown verifies the IsKnown validation helper: every served key
// is known, and an unknown/typo permission string is rejected.
func TestCatalog_IsKnown(t *testing.T) {
	for _, d := range permissions.All() {
		if !permissions.IsKnown(d.Key) {
			t.Errorf("IsKnown(%q) = false, want true (it is a served catalog entry)", d.Key)
		}
	}
	for _, bogus := range []string{
		"team:apikeys:manage", // the OLD served-only string — must now be unknown
		"team:models:delete",  // ditto
		"totally:bogus:perm",
		"",
		"team",
	} {
		if permissions.IsKnown(bogus) {
			t.Errorf("IsKnown(%q) = true, want false (unknown permission must be rejected)", bogus)
		}
	}
}

// TestCatalog_NoDuplicates ensures each permission string is served exactly once
// and carries a valid scope + non-empty description.
func TestCatalog_NoDuplicates(t *testing.T) {
	seen := make(map[string]bool)
	validScope := map[string]bool{"platform": true, "org": true, "team": true, "inference": true}
	for _, d := range permissions.All() {
		if seen[d.Key] {
			t.Errorf("duplicate catalog entry %q", d.Key)
		}
		seen[d.Key] = true
		if d.Description == "" {
			t.Errorf("catalog entry %q has empty description", d.Key)
		}
		if !validScope[d.Scope] {
			t.Errorf("catalog entry %q has invalid scope %q", d.Key, d.Scope)
		}
	}
}

// TestSystemRoles_OnlyGrantKnownPerms guards that built-in/legacy roles resolve
// exclusively to permissions the catalog serves (no drift after the change).
func TestSystemRoles_OnlyGrantKnownPerms(t *testing.T) {
	for _, r := range permissions.SystemRoles() {
		for _, p := range r.Permissions {
			if !permissions.IsKnown(p) {
				t.Errorf("built-in role %q grants %q which is not a known/served permission", r.ID, p)
			}
		}
	}
	// Legacy role mapping must also resolve to known permissions.
	for _, legacy := range []string{"admin", "viewer", "inference"} {
		ctx := permissions.FromLegacyRole("k", legacy)
		for _, p := range ctx.Permissions {
			if !permissions.IsKnown(p) {
				t.Errorf("legacy role %q resolves to unknown permission %q", legacy, p)
			}
		}
	}
}
