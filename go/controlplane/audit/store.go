package audit

import (
	"context"
	"fmt"
	"sync"
)

// Store is the persistence abstraction for the audit chain. It is intentionally
// backend-neutral so the in-memory [MemStore] used for tests and small
// deployments can be swapped for a durable implementation (SQLite, an
// append-only file, an object store) without touching [Log].
//
// A Store persists entries that are already fully formed — Seq, PrevHash and
// Hash assigned by the [Log]. Implementations must enforce the append-only,
// contiguous-chain contract (see [MemStore.Append]) and must be safe for
// concurrent use.
type Store interface {
	// Append durably records e, which must extend the chain: its Seq must be
	// the next contiguous value and its PrevHash must equal the current tail's
	// Hash. Implementations return the stored entry (a copy) on success.
	Append(ctx context.Context, e Entry) (Entry, error)
	// List returns every entry in ascending Seq order. The result is a copy;
	// mutating it must not affect stored state.
	List(ctx context.Context) ([]Entry, error)
	// Last returns the most recent entry and true, or the zero Entry and false
	// when the log is empty.
	Last(ctx context.Context) (Entry, bool, error)
}

// MemStore is an in-memory [Store] backed by a slice, safe for concurrent use.
// It is the reference implementation and the store used by the test suite; it
// keeps no state beyond the entries themselves and loses them on process exit.
type MemStore struct {
	mu      sync.Mutex
	entries []Entry
}

// compile-time assertion that *MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{}
}

// Append records e after validating that it extends the chain: its Seq must be
// the next contiguous value (FirstSeq for an empty store) and its PrevHash must
// equal the current tail's Hash (GenesisPrevHash for an empty store). This
// defensive check makes the store an independent guardian of the append-only
// invariant rather than trusting the caller. The stored entry is deep-copied so
// later mutation of the caller's Details map cannot reach into the chain.
func (m *MemStore) Append(ctx context.Context, e Entry) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	wantSeq := FirstSeq + uint64(len(m.entries))
	if e.Seq != wantSeq {
		return Entry{}, fmt.Errorf("audit: non-contiguous seq: got %d, want %d", e.Seq, wantSeq)
	}
	wantPrev := GenesisPrevHash
	if len(m.entries) > 0 {
		wantPrev = m.entries[len(m.entries)-1].Hash
	}
	if e.PrevHash != wantPrev {
		return Entry{}, fmt.Errorf("audit: prev hash %q does not link to tail hash %q", e.PrevHash, wantPrev)
	}

	m.entries = append(m.entries, e.clone())
	return e.clone(), nil
}

// List returns a deep copy of all entries in ascending Seq order.
func (m *MemStore) List(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Entry, len(m.entries))
	for i := range m.entries {
		out[i] = m.entries[i].clone()
	}
	return out, nil
}

// Last returns a copy of the most recent entry, or false when empty.
func (m *MemStore) Last(ctx context.Context) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.entries) == 0 {
		return Entry{}, false, nil
	}
	return m.entries[len(m.entries)-1].clone(), true, nil
}
