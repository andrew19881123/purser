// Package server — auth.go holds the PKCE state store used by the OIDC
// authorization-code+PKCE flow. It is kept in a separate file to isolate the
// stateful store from the main request-routing logic in server.go.
package server

import (
	"sync"
	"time"
)

// pkceMaxEntries caps the number of in-flight PKCE states the store will hold.
// Login attempts that arrive when the store is at capacity (after expired
// entries are evicted) are silently dropped; the OAuth callback will fail and
// the user will need to retry. The limit is deliberately conservative: a
// 1000-entry burst is well above any legitimate operator workload.
const pkceMaxEntries = 1000

// pkceTTL is how long a PKCE state+verifier pair is valid after it is
// created. Authorization servers typically impose a shorter bound (60–300 s),
// so an expired entry simply means the login took too long.
const pkceTTL = 5 * time.Minute

// pkceEntry holds the verifier string and the expiry for one PKCE state.
type pkceEntry struct {
	verifier string
	exp      time.Time
}

// pkceStateStore is a bounded, thread-safe map of PKCE state → code-verifier
// entries. The map size is capped at pkceMaxEntries; when full, expired entries
// are evicted on each set call before a new entry is accepted. If the map
// remains full after eviction the new entry is silently dropped — the caller
// will receive an error at the OAuth callback and can restart the flow.
type pkceStateStore struct {
	mu      sync.Mutex
	entries map[string]pkceEntry
}

// newPKCEStateStore returns a ready-to-use pkceStateStore.
func newPKCEStateStore() *pkceStateStore {
	return &pkceStateStore{entries: make(map[string]pkceEntry)}
}

// set stores (state, verifier) with a TTL of pkceTTL. When the store is at
// pkceMaxEntries capacity, expired entries are evicted first; if still full
// after eviction the entry is silently dropped.
func (s *pkceStateStore) set(state, verifier string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= pkceMaxEntries {
		now := time.Now()
		for k, v := range s.entries {
			if now.After(v.exp) {
				delete(s.entries, k)
			}
		}
		// If still full after sweeping expired entries, refuse the new entry.
		if len(s.entries) >= pkceMaxEntries {
			return // silently drop — the login attempt will fail at callback
		}
	}
	s.entries[state] = pkceEntry{verifier: verifier, exp: time.Now().Add(pkceTTL)}
}

// get retrieves and removes the verifier for state. Returns ("", false) when
// the state is unknown or has expired.
func (s *pkceStateStore) get(state string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[state]
	if !ok {
		return "", false
	}
	delete(s.entries, state) // consume once
	if time.Now().After(e.exp) {
		return "", false
	}
	return e.verifier, true
}

// len returns the current number of unexpired (and possibly expired) entries.
// Intended for tests; not safe to call from concurrent production code without
// holding the lock.
func (s *pkceStateStore) lenUnsafe() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
