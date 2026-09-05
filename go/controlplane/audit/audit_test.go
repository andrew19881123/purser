package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// appendN appends n entries to a fresh Log/MemStore and returns both the log
// and the resulting entries.
func appendN(t *testing.T, n int) (*Log, []Entry) {
	t.Helper()
	ctx := context.Background()
	lg := New(NewMemStore())
	for i := 0; i < n; i++ {
		_, err := lg.Append(ctx, fmt.Sprintf("actor-%d", i), "act", fmt.Sprintf("target-%d", i),
			map[string]string{"i": fmt.Sprintf("%d", i)})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	entries, err := lg.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("expected %d entries, got %d", n, len(entries))
	}
	return lg, entries
}

// asVerifyError asserts err is a *VerifyError and returns it.
func asVerifyError(t *testing.T, err error) *VerifyError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a verification error, got nil")
	}
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *VerifyError, got %T: %v", err, err)
	}
	return ve
}

func TestAppendChainVerifies(t *testing.T) {
	_, entries := appendN(t, 8)

	if err := Verify(entries); err != nil {
		t.Fatalf("Verify on intact chain: %v", err)
	}

	// Seq is contiguous starting at FirstSeq, and the genesis link is correct.
	for i, e := range entries {
		if want := FirstSeq + uint64(i); e.Seq != want {
			t.Errorf("index %d: Seq = %d, want %d", i, e.Seq, want)
		}
	}
	if entries[0].PrevHash != GenesisPrevHash {
		t.Errorf("genesis PrevHash = %q, want %q", entries[0].PrevHash, GenesisPrevHash)
	}
	// Every subsequent PrevHash links to the previous Hash.
	for i := 1; i < len(entries); i++ {
		if entries[i].PrevHash != entries[i-1].Hash {
			t.Errorf("index %d: PrevHash %q does not link to previous Hash %q", i, entries[i].PrevHash, entries[i-1].Hash)
		}
	}
}

func TestEmptyChainVerifies(t *testing.T) {
	if err := Verify(nil); err != nil {
		t.Errorf("Verify(nil): %v", err)
	}
	if err := Verify([]Entry{}); err != nil {
		t.Errorf("Verify(empty): %v", err)
	}
}

func TestLogVerify(t *testing.T) {
	lg, _ := appendN(t, 5)
	if err := lg.Verify(context.Background()); err != nil {
		t.Fatalf("Log.Verify: %v", err)
	}
}

func TestTamperDetection(t *testing.T) {
	// Each case corrupts an otherwise-valid 6-entry chain and asserts Verify
	// fails at the expected index with the expected kind.
	tests := []struct {
		name      string
		corrupt   func(es []Entry) []Entry
		wantIndex int
		wantKind  string
	}{
		{
			name: "mutate field in middle entry",
			corrupt: func(es []Entry) []Entry {
				es[3].Actor = "attacker"
				return es
			},
			wantIndex: 3,
			wantKind:  KindHash,
		},
		{
			name: "mutate details in middle entry",
			corrupt: func(es []Entry) []Entry {
				es[2].Details["i"] = "tampered"
				return es
			},
			wantIndex: 2,
			wantKind:  KindHash,
		},
		{
			name: "tamper hash directly",
			corrupt: func(es []Entry) []Entry {
				// Flip the stored Hash to a valid-hex but wrong value.
				es[4].Hash = GenesisPrevHash
				return es
			},
			wantIndex: 4,
			wantKind:  KindHash,
		},
		{
			name: "break prevhash link",
			corrupt: func(es []Entry) []Entry {
				es[3].PrevHash = GenesisPrevHash
				return es
			},
			wantIndex: 3,
			wantKind:  KindLink,
		},
		{
			name: "swap two entries (reorder)",
			corrupt: func(es []Entry) []Entry {
				es[2], es[3] = es[3], es[2]
				return es
			},
			// After swapping indices 2 and 3, index 2 now holds the entry whose
			// Seq is FirstSeq+3, so the contiguity check trips first at index 2.
			wantIndex: 2,
			wantKind:  KindSeq,
		},
		{
			name: "delete middle entry",
			corrupt: func(es []Entry) []Entry {
				return append(es[:2:2], es[3:]...)
			},
			// The entry now at index 2 carries Seq FirstSeq+3, breaking contiguity.
			wantIndex: 2,
			wantKind:  KindSeq,
		},
		{
			name: "insert duplicate entry",
			corrupt: func(es []Entry) []Entry {
				out := make([]Entry, 0, len(es)+1)
				out = append(out, es[:3]...)
				out = append(out, es[2]) // duplicate -> Seq repeats
				out = append(out, es[3:]...)
				return out
			},
			wantIndex: 3,
			wantKind:  KindSeq,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, entries := appendN(t, 6)
			// Sanity: the pristine chain verifies before corruption.
			if err := Verify(entries); err != nil {
				t.Fatalf("pristine chain failed to verify: %v", err)
			}
			corrupted := tc.corrupt(entries)
			ve := asVerifyError(t, Verify(corrupted))
			if ve.Index != tc.wantIndex {
				t.Errorf("Index = %d, want %d (err: %v)", ve.Index, tc.wantIndex, ve)
			}
			if ve.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q (err: %v)", ve.Kind, tc.wantKind, ve)
			}
		})
	}
}

func TestCanonicalizationDeterminism(t *testing.T) {
	// Two entries with identical content but Details maps built in different
	// insertion orders must produce identical canonical bytes and hashes.
	base := Entry{
		Seq:          7,
		TimeUnixNano: 1_700_000_000_000_000_000,
		Actor:        "alice",
		Action:       "model.deploy",
		Target:       "model-42",
		PrevHash:     GenesisPrevHash,
	}

	a := base
	a.Details = map[string]string{}
	for _, k := range []string{"zeta", "alpha", "mu"} {
		a.Details[k] = "v-" + k
	}

	b := base
	b.Details = map[string]string{}
	for _, k := range []string{"mu", "zeta", "alpha"} {
		b.Details[k] = "v-" + k
	}

	if string(a.CanonicalBytes()) != string(b.CanonicalBytes()) {
		t.Fatal("canonical bytes differ for identical content in different key orders")
	}
	ha, err := a.ComputeHash()
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hb, err := b.ComputeHash()
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if ha != hb {
		t.Fatalf("hashes differ: %q vs %q", ha, hb)
	}

	// A nil Details map hashes identically to an empty one.
	empty := base
	empty.Details = map[string]string{}
	nilD := base
	nilD.Details = nil
	he, _ := empty.ComputeHash()
	hn, _ := nilD.ComputeHash()
	if he != hn {
		t.Errorf("empty vs nil Details hash mismatch: %q vs %q", he, hn)
	}

	// Changing any content field changes the hash.
	changed := base
	changed.Details = a.Details
	changed.Actor = "bob"
	hc, _ := changed.ComputeHash()
	if hc == ha {
		t.Error("hash unchanged after mutating Actor")
	}
}

func TestLengthPrefixPreventsFieldCollision(t *testing.T) {
	// Actor="ab",Action="c" must not hash the same as Actor="a",Action="bc".
	x := Entry{Seq: 1, PrevHash: GenesisPrevHash, Actor: "ab", Action: "c"}
	y := Entry{Seq: 1, PrevHash: GenesisPrevHash, Actor: "a", Action: "bc"}
	hx, _ := x.ComputeHash()
	hy, _ := y.ComputeHash()
	if hx == hy {
		t.Fatal("field boundary collision: distinct fields produced identical hashes")
	}
}

func TestComputeHashInvalidPrevHash(t *testing.T) {
	e := Entry{Seq: 1, PrevHash: "not-hex"}
	if _, err := e.ComputeHash(); err == nil {
		t.Fatal("expected error for non-hex PrevHash")
	}
}

func TestMemStoreRejectsNonContiguous(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	genesis := Entry{Seq: FirstSeq, PrevHash: GenesisPrevHash, Actor: "a"}
	genesis.Hash, _ = genesis.ComputeHash()
	if _, err := m.Append(ctx, genesis); err != nil {
		t.Fatalf("append genesis: %v", err)
	}

	// Wrong Seq is rejected.
	bad := Entry{Seq: FirstSeq + 5, PrevHash: genesis.Hash}
	bad.Hash, _ = bad.ComputeHash()
	if _, err := m.Append(ctx, bad); err == nil {
		t.Error("expected non-contiguous seq to be rejected")
	}

	// Correct Seq but wrong PrevHash is rejected.
	badLink := Entry{Seq: FirstSeq + 1, PrevHash: GenesisPrevHash}
	badLink.Hash, _ = badLink.ComputeHash()
	if _, err := m.Append(ctx, badLink); err == nil {
		t.Error("expected broken prev-hash link to be rejected")
	}
}

func TestStoreReturnsCopies(t *testing.T) {
	// Mutating a Details map returned by List must not corrupt stored state.
	ctx := context.Background()
	lg := New(NewMemStore())
	if _, err := lg.Append(ctx, "a", "act", "t", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	entries, _ := lg.List(ctx)
	entries[0].Details["k"] = "mutated"
	entries[0].Actor = "mutated"

	again, _ := lg.List(ctx)
	if again[0].Details["k"] != "v" {
		t.Errorf("stored Details was mutated through returned copy: %q", again[0].Details["k"])
	}
	if again[0].Actor != "a" {
		t.Errorf("stored Actor was mutated through returned copy: %q", again[0].Actor)
	}
	if err := Verify(again); err != nil {
		t.Errorf("chain broken after external mutation of a copy: %v", err)
	}
}

func TestCallerMapMutationDoesNotAffectEntry(t *testing.T) {
	ctx := context.Background()
	lg := New(NewMemStore())
	d := map[string]string{"k": "v"}
	if _, err := lg.Append(ctx, "a", "act", "t", d); err != nil {
		t.Fatalf("append: %v", err)
	}
	d["k"] = "changed" // mutate the caller's map after appending

	entries, _ := lg.List(ctx)
	if entries[0].Details["k"] != "v" {
		t.Errorf("caller's post-append mutation leaked into the entry: %q", entries[0].Details["k"])
	}
	if err := Verify(entries); err != nil {
		t.Errorf("Verify after caller mutation: %v", err)
	}
}

// TestConcurrentAppends stresses Log.Append from many goroutines. The race
// detector requires CGO, which this module builds without (CGO_ENABLED=0), so
// -race is unavailable here; correctness instead rests on Log's mutex plus this
// high-contention stress loop, which asserts the final chain is well-formed:
// unique, contiguous Seqs and an intact hash chain.
func TestConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	lg := New(NewMemStore())

	const (
		goroutines = 16
		perG       = 64
	)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines*perG)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_, err := lg.Append(ctx, fmt.Sprintf("g%d", g), "act", fmt.Sprintf("t%d-%d", g, i),
					map[string]string{"g": fmt.Sprintf("%d", g), "i": fmt.Sprintf("%d", i)})
				if err != nil {
					errCh <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent append: %v", err)
	}

	entries, err := lg.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := goroutines * perG; len(entries) != want {
		t.Fatalf("got %d entries, want %d", len(entries), want)
	}
	if err := Verify(entries); err != nil {
		t.Fatalf("Verify after concurrent appends: %v", err)
	}
	// Seqs are unique and contiguous from FirstSeq.
	seen := make(map[uint64]bool, len(entries))
	for i, e := range entries {
		if want := FirstSeq + uint64(i); e.Seq != want {
			t.Fatalf("index %d: Seq = %d, want %d", i, e.Seq, want)
		}
		if seen[e.Seq] {
			t.Fatalf("duplicate Seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
}
