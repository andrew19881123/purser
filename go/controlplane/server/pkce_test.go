// Package server — pkce_test.go tests the bounded PKCE state store.
package server

import (
	"fmt"
	"testing"
)

// TestPKCEStore_BoundedEntries verifies that the store never exceeds
// pkceMaxEntries entries when set() is called beyond the capacity.
func TestPKCEStore_BoundedEntries(t *testing.T) {
	s := newPKCEStateStore()

	// Fill the store to one more than the maximum.
	for i := 0; i < pkceMaxEntries+1; i++ {
		s.set(fmt.Sprintf("state-%d", i), fmt.Sprintf("verifier-%d", i))
	}

	// The store must not exceed the cap regardless of how many entries were
	// submitted. The last entry may have been silently dropped.
	got := s.lenUnsafe()
	if got > pkceMaxEntries {
		t.Errorf("store size = %d, want <= %d", got, pkceMaxEntries)
	}
}

// TestPKCEStore_SetAndGet verifies the basic round-trip and single-use semantics.
func TestPKCEStore_SetAndGet(t *testing.T) {
	s := newPKCEStateStore()

	const state = "my-state"
	const verifier = "my-verifier"
	s.set(state, verifier)

	got, ok := s.get(state)
	if !ok {
		t.Fatal("get: expected ok=true, got false")
	}
	if got != verifier {
		t.Errorf("verifier = %q, want %q", got, verifier)
	}

	// Second get must return nothing (single-use).
	_, ok2 := s.get(state)
	if ok2 {
		t.Error("second get: expected ok=false (single-use), got true")
	}
}

// TestPKCEStore_UnknownState verifies that looking up an unknown state returns
// the zero value and false.
func TestPKCEStore_UnknownState(t *testing.T) {
	s := newPKCEStateStore()
	_, ok := s.get("no-such-state")
	if ok {
		t.Error("unknown state: expected ok=false, got true")
	}
}
