package registry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/registry"
)

func openTestRegistry(t *testing.T) *registry.SQLiteRegistry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := reg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg
}

// hashToken returns the hex-encoded SHA-256 of token, matching the format
// used by handleCreateAPIKey and GetAPIKeyByHash.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TestGetAPIKeyByHash_Found verifies that an enabled key can be retrieved by
// its token hash.
func TestGetAPIKeyByHash_Found(t *testing.T) {
	reg := openTestRegistry(t)
	ctx := context.Background()

	const token = "psk_test_findme"
	key := &registry.APIKey{
		ID:      "key-findme",
		Name:    "find-me",
		KeyHash: hashToken(token),
		Tenant:  "tenant-a",
		Role:    "admin",
		Enabled: true,
	}
	if err := reg.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := reg.GetAPIKeyByHash(ctx, hashToken(token))
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: unexpected error: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("ID = %q, want %q", got.ID, key.ID)
	}
	if got.Role != "admin" {
		t.Errorf("Role = %q, want admin", got.Role)
	}
}

// TestGetAPIKeyByHash_NotFound verifies that looking up a hash that doesn't
// match any enabled key returns ErrNotFound.
func TestGetAPIKeyByHash_NotFound(t *testing.T) {
	reg := openTestRegistry(t)
	ctx := context.Background()

	_, err := reg.GetAPIKeyByHash(ctx, hashToken("psk_does_not_exist"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err != registry.ErrNotFound {
		t.Fatalf("err = %v, want registry.ErrNotFound", err)
	}
}

// TestGetAPIKeyByHash_DisabledKeyNotFound verifies that a disabled key is not
// returned by GetAPIKeyByHash (the WHERE clause requires enabled = 1).
func TestGetAPIKeyByHash_DisabledKeyNotFound(t *testing.T) {
	reg := openTestRegistry(t)
	ctx := context.Background()

	const token = "psk_test_disabled"
	key := &registry.APIKey{
		ID:      "key-disabled",
		Name:    "disabled-key",
		KeyHash: hashToken(token),
		Tenant:  "tenant-b",
		Role:    "viewer",
		Enabled: false,
	}
	if err := reg.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := reg.GetAPIKeyByHash(ctx, hashToken(token))
	if err != registry.ErrNotFound {
		t.Fatalf("disabled key: err = %v, want ErrNotFound", err)
	}
}
