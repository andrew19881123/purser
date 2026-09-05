package audit

import (
	"context"
	"sync"
	"time"
)

// Log is the append-only auditor that maintains the hash chain on top of a
// [Store]. It is the only writer callers use: they supply content, and the log
// assigns Seq, timestamp, PrevHash and Hash so the chain invariants always
// hold.
//
// A Log must be the sole writer to its Store. It serializes appends with an
// internal mutex so concurrent callers receive strictly increasing, contiguous
// sequence numbers and a well-formed chain. Reads (List, Verify) go straight to
// the Store.
type Log struct {
	// mu serializes Append so the read-tail/compute/persist sequence is atomic
	// across goroutines and Seq assignment stays contiguous.
	mu    sync.Mutex
	store Store
	// now supplies timestamps; a field (rather than a direct time.Now call) so
	// tests can substitute a deterministic clock.
	now func() time.Time
}

// New returns a Log backed by store. The store may be empty or may already
// contain a chain; either way the next append continues from the current tail.
func New(store Store) *Log {
	return &Log{store: store, now: time.Now}
}

// Append records a new event with the given content, assigning Seq, timestamp,
// PrevHash and Hash, and persists it via the store. It returns the fully
// populated stored entry.
//
// details may be nil; when non-nil it is copied, so the caller may safely reuse
// or mutate the map afterward without affecting the recorded entry.
func (l *Log) Append(ctx context.Context, actor, action, target string, details map[string]string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	last, ok, err := l.store.Last(ctx)
	if err != nil {
		return Entry{}, err
	}

	e := Entry{
		Actor:        actor,
		Action:       action,
		Target:       target,
		Details:      cloneDetails(details),
		TimeUnixNano: l.now().UnixNano(),
	}
	if ok {
		e.Seq = last.Seq + 1
		e.PrevHash = last.Hash
	} else {
		e.Seq = FirstSeq
		e.PrevHash = GenesisPrevHash
	}

	hash, err := e.ComputeHash()
	if err != nil {
		return Entry{}, err
	}
	e.Hash = hash

	return l.store.Append(ctx, e)
}

// List returns every entry in ascending Seq order.
func (l *Log) List(ctx context.Context) ([]Entry, error) {
	return l.store.List(ctx)
}

// Verify lists the chain and verifies it end to end, returning a [*VerifyError]
// on the first inconsistency. See the package-level [Verify].
func (l *Log) Verify(ctx context.Context) error {
	entries, err := l.store.List(ctx)
	if err != nil {
		return err
	}
	return Verify(entries)
}
