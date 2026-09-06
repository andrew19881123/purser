package permissions_test

import (
	"testing"

	"github.com/purser/purser/go/controlplane/permissions"
)

// ---------------------------------------------------------------------------
// Has
// ---------------------------------------------------------------------------

func TestHas_ExactMatch(t *testing.T) {
	perms := []string{"team:models:deploy", "inference:call"}
	if !permissions.Has(perms, "team:models:deploy") {
		t.Error("expected Has to return true for exact match")
	}
	if !permissions.Has(perms, "inference:call") {
		t.Error("expected Has to return true for exact match (inference:call)")
	}
}

func TestHas_NoMatch(t *testing.T) {
	perms := []string{"team:models:deploy", "inference:call"}
	if permissions.Has(perms, "org:members:invite") {
		t.Error("expected Has to return false for absent permission")
	}
}

func TestHas_WildcardMatch(t *testing.T) {
	// "team:*" should match any permission that starts with "team:"
	perms := []string{"team:*"}
	cases := []string{
		"team:models:deploy",
		"team:models:undeploy",
		"team:keys:create",
		"team:keys:revoke",
		"team:members:view",
		"team:metrics:view",
	}
	for _, c := range cases {
		if !permissions.Has(perms, c) {
			t.Errorf("wildcard 'team:*' should match %q but did not", c)
		}
	}
}

func TestHas_WildcardNoMatch(t *testing.T) {
	// "team:*" must NOT match permissions outside the "team:" namespace.
	perms := []string{"team:*"}
	nonTeam := []string{
		"org:teams:create",
		"platform:orgs:create",
		"inference:call",
	}
	for _, c := range nonTeam {
		if permissions.Has(perms, c) {
			t.Errorf("wildcard 'team:*' should NOT match %q but did", c)
		}
	}
}

func TestHas_OrgWildcardMatch(t *testing.T) {
	perms := []string{"org:*"}
	cases := []string{
		"org:teams:create",
		"org:teams:delete",
		"org:members:invite",
		"org:roles:create",
	}
	for _, c := range cases {
		if !permissions.Has(perms, c) {
			t.Errorf("wildcard 'org:*' should match %q but did not", c)
		}
	}
}

func TestHas_InferenceWildcard(t *testing.T) {
	// "inference:*" matches "inference:call"
	perms := []string{"inference:*"}
	if !permissions.Has(perms, "inference:call") {
		t.Error("expected 'inference:*' to match 'inference:call'")
	}
	// But not unrelated permissions
	if permissions.Has(perms, "team:models:deploy") {
		t.Error("'inference:*' should NOT match 'team:models:deploy'")
	}
}

func TestHas_EmptyEffective(t *testing.T) {
	if permissions.Has(nil, "inference:call") {
		t.Error("Has on nil effective set should return false")
	}
	if permissions.Has([]string{}, "inference:call") {
		t.Error("Has on empty effective set should return false")
	}
}

// ---------------------------------------------------------------------------
// HasAll
// ---------------------------------------------------------------------------

func TestHasAll_AllPresent(t *testing.T) {
	perms := []string{"team:models:deploy", "inference:call", "team:metrics:view"}
	if !permissions.HasAll(perms, "team:models:deploy", "inference:call") {
		t.Error("HasAll should return true when all required permissions are present")
	}
}

func TestHasAll_OneMissing_ReturnsFalse(t *testing.T) {
	perms := []string{"team:models:deploy", "inference:call"}
	if permissions.HasAll(perms, "team:models:deploy", "org:members:invite") {
		t.Error("HasAll should return false when at least one permission is missing")
	}
}

func TestHasAll_NoRequired_ReturnsTrue(t *testing.T) {
	// Vacuously true — all zero required permissions are present.
	if !permissions.HasAll([]string{"inference:call"}) {
		t.Error("HasAll with no required permissions should return true")
	}
}

// ---------------------------------------------------------------------------
// HasAny
// ---------------------------------------------------------------------------

func TestHasAny_OnePresent_ReturnsTrue(t *testing.T) {
	perms := []string{"inference:call"}
	if !permissions.HasAny(perms, "team:models:deploy", "inference:call") {
		t.Error("HasAny should return true when at least one required permission is present")
	}
}

func TestHasAny_NonePresent_ReturnsFalse(t *testing.T) {
	perms := []string{"inference:call"}
	if permissions.HasAny(perms, "team:models:deploy", "org:members:invite") {
		t.Error("HasAny should return false when no required permission is present")
	}
}

func TestHasAny_WildcardSatisfies(t *testing.T) {
	perms := []string{"team:*"}
	if !permissions.HasAny(perms, "team:keys:create", "org:members:invite") {
		t.Error("HasAny with wildcard should match team:keys:create")
	}
}

// ---------------------------------------------------------------------------
// SystemRoles
// ---------------------------------------------------------------------------

func TestSystemRoles_NotEmpty(t *testing.T) {
	roles := permissions.SystemRoles()
	if len(roles) == 0 {
		t.Error("SystemRoles() should return at least one role")
	}
}

func TestSystemRoles_PlatformAdminHasAll(t *testing.T) {
	roles := permissions.SystemRoles()
	var pAdmin *permissions.BuiltinRole
	for i := range roles {
		if roles[i].ID == "platform_admin" {
			pAdmin = &roles[i]
			break
		}
	}
	if pAdmin == nil {
		t.Fatal("platform_admin role not found in SystemRoles()")
	}

	required := []string{
		"platform:orgs:create",
		"org:teams:create",
		"team:models:deploy",
		"inference:call",
	}
	for _, r := range required {
		if !permissions.Has(pAdmin.Permissions, r) {
			t.Errorf("platform_admin missing expected permission %q", r)
		}
	}
}

func TestSystemRoles_InferenceOnlyHasOnlyInference(t *testing.T) {
	roles := permissions.SystemRoles()
	var inferenceOnly *permissions.BuiltinRole
	for i := range roles {
		if roles[i].ID == "inference_only" {
			inferenceOnly = &roles[i]
			break
		}
	}
	if inferenceOnly == nil {
		t.Fatal("inference_only role not found in SystemRoles()")
	}
	if len(inferenceOnly.Permissions) != 1 {
		t.Errorf("inference_only should have exactly 1 permission, got %d: %v",
			len(inferenceOnly.Permissions), inferenceOnly.Permissions)
	}
	if inferenceOnly.Permissions[0] != "inference:call" {
		t.Errorf("inference_only should have 'inference:call', got %q",
			inferenceOnly.Permissions[0])
	}
}

func TestSystemRoles_AllRolesHaveIDAndName(t *testing.T) {
	for _, r := range permissions.SystemRoles() {
		if r.ID == "" {
			t.Errorf("role missing ID: %+v", r)
		}
		if r.Name == "" {
			t.Errorf("role %q missing Name", r.ID)
		}
		if len(r.Permissions) == 0 {
			t.Errorf("role %q has no permissions", r.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// FromLegacyRole
// ---------------------------------------------------------------------------

func TestFromLegacyRole_Admin_HasDeploy(t *testing.T) {
	ctx := permissions.FromLegacyRole("key-1", "admin")
	if !ctx.IsPlatformAdmin {
		t.Error("legacy admin role should set IsPlatformAdmin=true")
	}
	if !permissions.Has(ctx.Permissions, "team:models:deploy") {
		t.Error("legacy admin should have team:models:deploy permission")
	}
	if !permissions.Has(ctx.Permissions, "platform:orgs:create") {
		t.Error("legacy admin should have platform:orgs:create permission")
	}
}

func TestFromLegacyRole_Viewer_CannotDeploy(t *testing.T) {
	ctx := permissions.FromLegacyRole("key-2", "viewer")
	if ctx.IsPlatformAdmin {
		t.Error("legacy viewer role should NOT set IsPlatformAdmin")
	}
	if permissions.Has(ctx.Permissions, "team:models:deploy") {
		t.Error("legacy viewer should NOT have team:models:deploy permission")
	}
	// viewer should still be able to view metrics
	if !permissions.Has(ctx.Permissions, "team:metrics:view") {
		t.Error("legacy viewer should have team:metrics:view permission")
	}
}

func TestFromLegacyRole_Inference_OnlyInference(t *testing.T) {
	ctx := permissions.FromLegacyRole("key-3", "inference")
	if ctx.IsPlatformAdmin {
		t.Error("legacy inference role should NOT set IsPlatformAdmin")
	}
	if !permissions.Has(ctx.Permissions, "inference:call") {
		t.Error("legacy inference should have inference:call permission")
	}
	if permissions.Has(ctx.Permissions, "team:models:deploy") {
		t.Error("legacy inference should NOT have team:models:deploy permission")
	}
	if len(ctx.Permissions) != 1 {
		t.Errorf("legacy inference should have exactly 1 permission, got %d: %v",
			len(ctx.Permissions), ctx.Permissions)
	}
}

func TestFromLegacyRole_Unknown_NilPerms(t *testing.T) {
	ctx := permissions.FromLegacyRole("key-4", "unknown_role")
	if len(ctx.Permissions) != 0 {
		t.Errorf("unknown legacy role should produce empty permissions, got %v", ctx.Permissions)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

func TestMerge_Deduplicates(t *testing.T) {
	a := []string{"inference:call", "team:metrics:view"}
	b := []string{"team:metrics:view", "team:models:deploy"}
	merged := permissions.Merge(a, b)

	seen := make(map[string]int)
	for _, p := range merged {
		seen[p]++
	}
	for p, count := range seen {
		if count > 1 {
			t.Errorf("permission %q appears %d times in merged result, expected 1", p, count)
		}
	}

	// all three distinct values must be present
	for _, want := range []string{"inference:call", "team:metrics:view", "team:models:deploy"} {
		if seen[want] == 0 {
			t.Errorf("expected %q in merged result but not found", want)
		}
	}
}

func TestMerge_Sorted(t *testing.T) {
	a := []string{"team:models:deploy", "inference:call"}
	b := []string{"org:members:invite"}
	merged := permissions.Merge(a, b)

	for i := 1; i < len(merged); i++ {
		if merged[i-1] > merged[i] {
			t.Errorf("Merge result is not sorted at index %d: %q > %q",
				i, merged[i-1], merged[i])
		}
	}
}

func TestMerge_EmptyInputs(t *testing.T) {
	merged := permissions.Merge(nil, []string{}, nil)
	if len(merged) != 0 {
		t.Errorf("Merge of empty inputs should return empty slice, got %v", merged)
	}
}

func TestMerge_SingleSet(t *testing.T) {
	perms := []string{"b", "a", "c"}
	merged := permissions.Merge(perms)
	if len(merged) != 3 {
		t.Errorf("expected 3 permissions, got %d", len(merged))
	}
	// should be sorted
	if merged[0] != "a" || merged[1] != "b" || merged[2] != "c" {
		t.Errorf("expected sorted [a b c], got %v", merged)
	}
}
