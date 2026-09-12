package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParserFloorGuards proves that each parser returns an error (not empty
// success) when the source file contains no matching lines.
//
// This is the red-first proof for the false-green fix (I-1): without the floor
// guard, a regex that silently matches nothing would return ([]items, nil) and
// all downstream tests would trivially pass with zero items to check.
//
// Each sub-test writes a temp file with deliberately non-matching content, calls
// the parser, and asserts an error is returned.

func TestRegisterRouteFloorGuardRejectsEmptyFile(t *testing.T) {
	f := writeTempFile(t, "// package server — no route table here\nvar x = 1\n")
	_, err := RegisteredRoutes(f)
	if err == nil {
		t.Fatal("RegisteredRoutes: expected an error for a file with no route rows, got nil")
	}
	if !strings.Contains(err.Error(), "matched only") {
		t.Errorf("RegisteredRoutes: error should mention 'matched only', got: %v", err)
	}
}

func TestParseRBACEntriesFloorGuardRejectsEmptyFile(t *testing.T) {
	f := writeTempFile(t, "// package server — no routePermission map here\nvar x = 1\n")
	_, err := ParseRBACEntries(f)
	if err == nil {
		t.Fatal("ParseRBACEntries: expected an error for a file with no RBAC entries, got nil")
	}
	if !strings.Contains(err.Error(), "matched only") {
		t.Errorf("ParseRBACEntries: error should mention 'matched only', got: %v", err)
	}
}

func TestParsePermConstantsFloorGuardRejectsEmptyFile(t *testing.T) {
	f := writeTempFile(t, "// package permissions — no Perm* constants here\nvar x = 1\n")
	_, err := ParsePermConstants(f)
	if err == nil {
		t.Fatal("ParsePermConstants: expected an error for a file with no Perm* constants, got nil")
	}
	if !strings.Contains(err.Error(), "matched only") {
		t.Errorf("ParsePermConstants: error should mention 'matched only', got: %v", err)
	}
}

// TestParserFloorGuardsPassOnRealFiles confirms the floor guards do not
// false-positive on the real source files (sanity check: real files >> floors).
func TestParserFloorGuardsPassOnRealFiles(t *testing.T) {
	routes, err := RegisteredRoutes(routeTableFile)
	if err != nil {
		t.Errorf("RegisteredRoutes on real file: unexpected error: %v", err)
	}
	if len(routes) < minRegisteredRoutes {
		t.Errorf("RegisteredRoutes: got %d routes, want >= %d", len(routes), minRegisteredRoutes)
	}

	entries, err := ParseRBACEntries(rbacFile)
	if err != nil {
		t.Errorf("ParseRBACEntries on real file: unexpected error: %v", err)
	}
	if len(entries) < minRBACEntries {
		t.Errorf("ParseRBACEntries: got %d entries, want >= %d", len(entries), minRBACEntries)
	}

	consts, err := ParsePermConstants(permsFile)
	if err != nil {
		t.Errorf("ParsePermConstants on real file: unexpected error: %v", err)
	}
	if len(consts) < minPermConstants {
		t.Errorf("ParsePermConstants: got %d constants, want >= %d", len(consts), minPermConstants)
	}
}

// writeTempFile creates a temporary file with the given content and returns its
// path. The file is removed when the test finishes.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake_source.go")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	return path
}
