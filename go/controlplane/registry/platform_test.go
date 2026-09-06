package registry_test

// platform_test.go — tests for the v0.4 multi-tenant platform data layer.
//
// Test coverage:
//   - Organization CRUD (create + get by ID + get by slug)
//   - Team slug uniqueness scoped to org
//   - Org membership upsert idempotency
//   - GetAllowedNodeIDs: exclusive pool, shared pool, no pool
//   - SeedSystemRoles idempotency
//   - GetEffectivePermissions for developer and org_admin roles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func openPlatform(t *testing.T) registry.Registry {
	t.Helper()
	return openTemp(t)
}

func mustCreateOrg(t *testing.T, reg registry.Registry, id, name, slug string) *registry.Organization {
	t.Helper()
	org := &registry.Organization{ID: id, Name: name, Slug: slug}
	if err := reg.CreateOrganization(context.Background(), org); err != nil {
		t.Fatalf("CreateOrganization %q: %v", id, err)
	}
	return org
}

func mustCreateTeam(t *testing.T, reg registry.Registry, id, orgID, name, slug string) *registry.Team {
	t.Helper()
	team := &registry.Team{ID: id, OrgID: orgID, Name: name, Slug: slug}
	if err := reg.CreateTeam(context.Background(), team); err != nil {
		t.Fatalf("CreateTeam %q: %v", id, err)
	}
	return team
}

func mustCreateUser(t *testing.T, reg registry.Registry, id, email string) *registry.PlatformUser {
	t.Helper()
	u := &registry.PlatformUser{ID: id, Email: email, AuthMethod: "oidc"}
	if err := reg.UpsertPlatformUser(context.Background(), u); err != nil {
		t.Fatalf("UpsertPlatformUser %q: %v", id, err)
	}
	return u
}

func mustCreateNode(t *testing.T, reg registry.Registry, id string) {
	t.Helper()
	n := &registry.Node{
		ID:       id,
		Hostname: id + ".local",
		OS:       "linux",
		Arch:     "x86_64",
		State:    "NODE_STATE_READY",
	}
	if err := reg.CreateNode(context.Background(), n); err != nil {
		t.Fatalf("CreateNode %q: %v", id, err)
	}
}

func mustCreatePool(t *testing.T, reg registry.Registry, id, policy, ownerType, ownerID string) *registry.NodePool {
	t.Helper()
	p := &registry.NodePool{
		ID:        id,
		Name:      id,
		Policy:    policy,
		OwnerType: ownerType,
		OwnerID:   ownerID,
	}
	if err := reg.CreateNodePool(context.Background(), p); err != nil {
		t.Fatalf("CreateNodePool %q: %v", id, err)
	}
	return p
}

func mustSeed(t *testing.T, reg registry.Registry) {
	t.Helper()
	if err := reg.SeedSystemRoles(context.Background()); err != nil {
		t.Fatalf("SeedSystemRoles: %v", err)
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestCreateOrganization_Happy creates an org, reads it back by ID and by slug.
func TestCreateOrganization_Happy(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme Corp", "acme")

	got, err := reg.GetOrganization(ctx, "org-1")
	if err != nil {
		t.Fatalf("GetOrganization: %v", err)
	}
	if got.Name != "Acme Corp" || got.Slug != "acme" {
		t.Errorf("unexpected org: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated")
	}

	bySlug, err := reg.GetOrganizationBySlug(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrganizationBySlug: %v", err)
	}
	if bySlug.ID != "org-1" {
		t.Errorf("GetOrganizationBySlug returned wrong org: %q", bySlug.ID)
	}
}

// TestGetOrganization_NotFound expects ErrNotFound for a missing ID.
func TestGetOrganization_NotFound(t *testing.T) {
	reg := openPlatform(t)
	_, err := reg.GetOrganization(context.Background(), "no-such-org")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestCreateTeam_SameSlugDifferentOrg_Allowed verifies that slug uniqueness
// is scoped to org: two teams with the same slug in different orgs are valid.
func TestCreateTeam_SameSlugDifferentOrg_Allowed(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-a", "Alpha", "alpha")
	mustCreateOrg(t, reg, "org-b", "Beta", "beta")

	mustCreateTeam(t, reg, "team-a", "org-a", "Engineering", "eng")
	mustCreateTeam(t, reg, "team-b", "org-b", "Engineering", "eng") // same slug, different org

	ta, err := reg.GetTeamBySlug(ctx, "org-a", "eng")
	if err != nil {
		t.Fatalf("GetTeamBySlug org-a/eng: %v", err)
	}
	tb, err := reg.GetTeamBySlug(ctx, "org-b", "eng")
	if err != nil {
		t.Fatalf("GetTeamBySlug org-b/eng: %v", err)
	}
	if ta.ID == tb.ID {
		t.Errorf("expected distinct teams, got same ID %q", ta.ID)
	}
}

// TestCreateTeam_DuplicateSlugSameOrg_Rejected verifies the UNIQUE(org_id,slug) constraint.
func TestCreateTeam_DuplicateSlugSameOrg_Rejected(t *testing.T) {
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "t1", "org-1", "Eng", "eng")

	err := reg.CreateTeam(context.Background(), &registry.Team{
		ID: "t2", OrgID: "org-1", Name: "Engineering2", Slug: "eng",
	})
	if err == nil {
		t.Fatal("expected error for duplicate slug, got nil")
	}
}

// TestAddOrgMember_DuplicateIgnored verifies the upsert-or-ignore (idempotent) semantics.
func TestAddOrgMember_DuplicateIgnored(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateUser(t, reg, "user-1", "alice@example.com")

	m := &registry.OrgMember{OrgID: "org-1", UserID: "user-1", Role: "member"}
	if err := reg.AddOrgMember(ctx, m); err != nil {
		t.Fatalf("first AddOrgMember: %v", err)
	}
	// Second call with same (org, user) must not fail.
	if err := reg.AddOrgMember(ctx, m); err != nil {
		t.Fatalf("second AddOrgMember (idempotent): %v", err)
	}

	members, err := reg.ListOrgMembers(ctx, "org-1")
	if err != nil {
		t.Fatalf("ListOrgMembers: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}
}

// TestGetAllowedNodeIDs_ExclusivePool: team owns an exclusive pool → only those nodes returned.
func TestGetAllowedNodeIDs_ExclusivePool(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "team-1", "org-1", "Eng", "eng")
	mustCreateNode(t, reg, "node-a")
	mustCreateNode(t, reg, "node-b")
	mustCreateNode(t, reg, "node-c")

	mustCreatePool(t, reg, "pool-exclusive", "exclusive", "team", "team-1")
	mustCreatePool(t, reg, "pool-other", "exclusive", "team", "team-99")

	if err := reg.AddNodeToPool(ctx, "node-a", "pool-exclusive"); err != nil {
		t.Fatalf("AddNodeToPool node-a: %v", err)
	}
	if err := reg.AddNodeToPool(ctx, "node-b", "pool-exclusive"); err != nil {
		t.Fatalf("AddNodeToPool node-b: %v", err)
	}
	if err := reg.AddNodeToPool(ctx, "node-c", "pool-other"); err != nil {
		t.Fatalf("AddNodeToPool node-c: %v", err)
	}

	ids, err := reg.GetAllowedNodeIDs(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetAllowedNodeIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 allowed nodes, got %d: %v", len(ids), ids)
	}
	want := map[string]bool{"node-a": true, "node-b": true}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected node %q in allowed list", id)
		}
	}
}

// TestGetAllowedNodeIDs_SharedPool: team has a quota on a shared pool → those nodes included.
func TestGetAllowedNodeIDs_SharedPool(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "team-1", "org-1", "Eng", "eng")
	mustCreateNode(t, reg, "node-shared-1")
	mustCreateNode(t, reg, "node-shared-2")
	mustCreateNode(t, reg, "node-own-1")

	// Shared platform pool
	mustCreatePool(t, reg, "pool-shared", "shared", "platform", "")
	// Exclusive pool owned by this team
	mustCreatePool(t, reg, "pool-own", "exclusive", "team", "team-1")

	if err := reg.AddNodeToPool(ctx, "node-shared-1", "pool-shared"); err != nil {
		t.Fatalf("AddNodeToPool: %v", err)
	}
	if err := reg.AddNodeToPool(ctx, "node-shared-2", "pool-shared"); err != nil {
		t.Fatalf("AddNodeToPool: %v", err)
	}
	if err := reg.AddNodeToPool(ctx, "node-own-1", "pool-own"); err != nil {
		t.Fatalf("AddNodeToPool: %v", err)
	}

	// Give team-1 a quota on the shared pool
	q := &registry.PoolTeamQuota{
		PoolID: "pool-shared", TeamID: "team-1",
		MaxDeployments: 5, MaxGPUNodes: 10, Priority: 50,
	}
	if err := reg.UpsertPoolTeamQuota(ctx, q); err != nil {
		t.Fatalf("UpsertPoolTeamQuota: %v", err)
	}

	ids, err := reg.GetAllowedNodeIDs(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetAllowedNodeIDs: %v", err)
	}
	// Expects 3 nodes: 2 from shared + 1 from own exclusive
	if len(ids) != 3 {
		t.Fatalf("expected 3 allowed nodes, got %d: %v", len(ids), ids)
	}
}

// TestGetAllowedNodeIDs_NoPool: team with no pool → empty slice.
func TestGetAllowedNodeIDs_NoPool(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "team-1", "org-1", "Eng", "eng")
	mustCreateNode(t, reg, "node-1")
	mustCreatePool(t, reg, "pool-other", "exclusive", "team", "team-other")
	if err := reg.AddNodeToPool(ctx, "node-1", "pool-other"); err != nil {
		t.Fatalf("AddNodeToPool: %v", err)
	}

	ids, err := reg.GetAllowedNodeIDs(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetAllowedNodeIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 allowed nodes for team with no pool, got %d: %v", len(ids), ids)
	}
}

// TestSeedSystemRoles_Idempotent: calling SeedSystemRoles twice must not error or duplicate.
func TestSeedSystemRoles_Idempotent(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	if err := reg.SeedSystemRoles(ctx); err != nil {
		t.Fatalf("first SeedSystemRoles: %v", err)
	}
	if err := reg.SeedSystemRoles(ctx); err != nil {
		t.Fatalf("second SeedSystemRoles: %v", err)
	}

	// Verify exactly the expected built-in roles exist (platform scope: org_id IS NULL)
	roles, err := reg.ListCustomRoles(ctx, "")
	if err != nil {
		t.Fatalf("ListCustomRoles: %v", err)
	}
	names := make(map[string]bool)
	for _, r := range roles {
		names[r.ID] = true
		if !r.IsSystem {
			t.Errorf("expected is_system=true for built-in role %q", r.ID)
		}
	}
	for _, expected := range []string{"platform_admin", "org_admin", "team_admin", "developer", "viewer", "inference_only"} {
		if !names[expected] {
			t.Errorf("expected built-in role %q not found", expected)
		}
	}
}

// TestGetEffectivePermissions_Developer: developer has team:models:deploy but NOT org:teams:create.
func TestGetEffectivePermissions_Developer(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)
	mustSeed(t, reg)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "team-1", "org-1", "Eng", "eng")
	mustCreateUser(t, reg, "user-dev", "dev@example.com")

	// Add user as regular member (not org_admin)
	if err := reg.AddOrgMember(ctx, &registry.OrgMember{
		OrgID: "org-1", UserID: "user-dev", Role: "member",
	}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	// Assign developer role in the team
	if err := reg.AddTeamMember(ctx, &registry.TeamMember{
		TeamID: "team-1", UserID: "user-dev", RoleID: "developer",
	}); err != nil {
		t.Fatalf("AddTeamMember: %v", err)
	}

	ep, err := reg.GetEffectivePermissions(ctx, "user-dev", "team-1")
	if err != nil {
		t.Fatalf("GetEffectivePermissions: %v", err)
	}
	if ep.IsOrgAdmin {
		t.Error("developer should not be org_admin")
	}

	permMap := make(map[string]bool)
	for _, p := range ep.Permissions {
		permMap[p] = true
	}
	if !permMap[registry.PermTeamModelsDeploy] {
		t.Errorf("developer should have %q", registry.PermTeamModelsDeploy)
	}
	if permMap[registry.PermOrgTeamsCreate] {
		t.Errorf("developer should NOT have %q", registry.PermOrgTeamsCreate)
	}
	if !permMap[registry.PermInferenceCall] {
		t.Errorf("developer should have %q", registry.PermInferenceCall)
	}
}

// TestGetEffectivePermissions_OrgAdmin: org_admin has both org: and team: permissions.
func TestGetEffectivePermissions_OrgAdmin(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)
	mustSeed(t, reg)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "team-1", "org-1", "Eng", "eng")
	mustCreateUser(t, reg, "user-admin", "admin@example.com")

	if err := reg.AddOrgMember(ctx, &registry.OrgMember{
		OrgID: "org-1", UserID: "user-admin", Role: "org_admin",
	}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	// No explicit team role — org_admin inherits everything
	ep, err := reg.GetEffectivePermissions(ctx, "user-admin", "team-1")
	if err != nil {
		t.Fatalf("GetEffectivePermissions: %v", err)
	}
	if !ep.IsOrgAdmin {
		t.Error("user should be org_admin")
	}

	permMap := make(map[string]bool)
	for _, p := range ep.Permissions {
		permMap[p] = true
	}
	if !permMap[registry.PermOrgTeamsCreate] {
		t.Errorf("org_admin should have %q", registry.PermOrgTeamsCreate)
	}
	if !permMap[registry.PermTeamModelsDeploy] {
		t.Errorf("org_admin should have %q", registry.PermTeamModelsDeploy)
	}
}

// TestUpsertPlatformUser_UpdatesOnConflict verifies that UpsertPlatformUser
// updates the display_name when called with an existing user ID.
func TestUpsertPlatformUser_UpdatesOnConflict(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	u := &registry.PlatformUser{ID: "u-1", Email: "a@x.com", DisplayName: "Alice", AuthMethod: "oidc"}
	if err := reg.UpsertPlatformUser(ctx, u); err != nil {
		t.Fatalf("first UpsertPlatformUser: %v", err)
	}
	u.DisplayName = "Alice Updated"
	if err := reg.UpsertPlatformUser(ctx, u); err != nil {
		t.Fatalf("second UpsertPlatformUser: %v", err)
	}

	got, err := reg.GetPlatformUser(ctx, "u-1")
	if err != nil {
		t.Fatalf("GetPlatformUser: %v", err)
	}
	if got.DisplayName != "Alice Updated" {
		t.Errorf("expected DisplayName %q, got %q", "Alice Updated", got.DisplayName)
	}
}

// TestUpdatePlatformUserLastSeen verifies that last_seen_at is persisted.
func TestUpdatePlatformUserLastSeen(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateUser(t, reg, "u-1", "b@x.com")
	at := time.Now().UTC().Truncate(time.Second)
	if err := reg.UpdatePlatformUserLastSeen(ctx, "u-1", at); err != nil {
		t.Fatalf("UpdatePlatformUserLastSeen: %v", err)
	}
	got, err := reg.GetPlatformUser(ctx, "u-1")
	if err != nil {
		t.Fatalf("GetPlatformUser: %v", err)
	}
	if got.LastSeenAt == nil {
		t.Fatal("LastSeenAt should be non-nil after update")
	}
	if !got.LastSeenAt.Equal(at) {
		t.Errorf("LastSeenAt mismatch: got %v want %v", got.LastSeenAt, at)
	}
}

// TestNodePool_RoundTrip verifies create → get → list → update → delete.
func TestNodePool_RoundTrip(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	pool := mustCreatePool(t, reg, "pool-1", "shared", "platform", "")

	got, err := reg.GetNodePool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetNodePool: %v", err)
	}
	if got.Policy != "shared" {
		t.Errorf("policy mismatch: %q", got.Policy)
	}

	got.Name = "renamed"
	if err := reg.UpdateNodePool(ctx, got); err != nil {
		t.Fatalf("UpdateNodePool: %v", err)
	}

	list, err := reg.ListNodePools(ctx)
	if err != nil {
		t.Fatalf("ListNodePools: %v", err)
	}
	if len(list) != 1 || list[0].Name != "renamed" {
		t.Errorf("unexpected list result: %v", list)
	}
}

// TestUpsertPoolTeamQuota_Idempotent verifies that upserting twice updates and
// does not create a second row.
func TestUpsertPoolTeamQuota_Idempotent(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")
	mustCreateTeam(t, reg, "team-1", "org-1", "Eng", "eng")
	mustCreatePool(t, reg, "pool-1", "shared", "platform", "")

	q := &registry.PoolTeamQuota{
		PoolID: "pool-1", TeamID: "team-1", MaxDeployments: 3, Priority: 50,
	}
	if err := reg.UpsertPoolTeamQuota(ctx, q); err != nil {
		t.Fatalf("first UpsertPoolTeamQuota: %v", err)
	}
	q.MaxDeployments = 10
	if err := reg.UpsertPoolTeamQuota(ctx, q); err != nil {
		t.Fatalf("second UpsertPoolTeamQuota: %v", err)
	}

	got, err := reg.GetPoolTeamQuota(ctx, "pool-1", "team-1")
	if err != nil {
		t.Fatalf("GetPoolTeamQuota: %v", err)
	}
	if got.MaxDeployments != 10 {
		t.Errorf("MaxDeployments should be 10 after upsert, got %d", got.MaxDeployments)
	}

	list, err := reg.ListPoolTeamQuotas(ctx, "pool-1")
	if err != nil {
		t.Fatalf("ListPoolTeamQuotas: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 quota row, got %d", len(list))
	}
}

// TestDeleteCustomRole_SystemRoleBlocked verifies system roles cannot be deleted.
func TestDeleteCustomRole_SystemRoleBlocked(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)
	mustSeed(t, reg)

	err := reg.DeleteCustomRole(ctx, "developer")
	if err == nil {
		t.Fatal("expected error deleting system role, got nil")
	}
}

// TestCustomRole_OrgScoped tests create + list for an org-scoped custom role.
func TestCustomRole_OrgScoped(t *testing.T) {
	ctx := context.Background()
	reg := openPlatform(t)
	mustSeed(t, reg)

	mustCreateOrg(t, reg, "org-1", "Acme", "acme")

	role := &registry.CustomRole{
		ID:          "custom-analyst",
		OrgID:       "org-1",
		Name:        "Analyst",
		Permissions: []string{registry.PermTeamMetricsView},
	}
	if err := reg.CreateCustomRole(ctx, role); err != nil {
		t.Fatalf("CreateCustomRole: %v", err)
	}

	roles, err := reg.ListCustomRoles(ctx, "org-1")
	if err != nil {
		t.Fatalf("ListCustomRoles: %v", err)
	}
	found := false
	for _, r := range roles {
		if r.ID == "custom-analyst" {
			found = true
			if len(r.Permissions) == 0 || r.Permissions[0] != registry.PermTeamMetricsView {
				t.Errorf("unexpected permissions: %v", r.Permissions)
			}
		}
	}
	if !found {
		t.Error("custom-analyst role not found in ListCustomRoles")
	}
}
