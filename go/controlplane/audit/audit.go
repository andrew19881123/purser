// Package audit implements a tamper-evident, hash-chained, append-only audit
// log for the Purser control plane.
//
// # Model
//
// Every administrative event ("who did what, when, to which target") is
// recorded as an [Entry]. Entries form a singly-linked hash chain: each entry
// carries the SHA-256 hash of the previous entry in PrevHash and its own hash
// in Hash. Because Hash covers both the previous hash and the entry's own
// content, changing any earlier entry — content, order or membership —
// invalidates every hash that follows, which [Verify] detects.
//
// # Canonical serialization
//
// Hashing must be over a byte encoding that is fully deterministic: the same
// logical content must always produce the same bytes, independent of Go map
// iteration order, struct field order or Go version. We therefore do NOT rely
// on encoding/json (whose escaping and formatting rules can drift); instead
// [Entry.CanonicalBytes] emits an explicit, length-prefixed encoding of the
// content fields in a fixed order:
//
//	canonicalVersion            (length-prefixed)
//	Seq                         (unsigned varint)
//	TimeUnixNano                (signed varint)
//	Actor                       (length-prefixed)
//	Action                      (length-prefixed)
//	Target                      (length-prefixed)
//	len(Details)                (unsigned varint)
//	for each Details key, sorted ascending:
//	    key                     (length-prefixed)
//	    value                   (length-prefixed)
//
// Every variable-length field is prefixed with its byte length so that no two
// distinct field assignments can serialize to the same bytes (e.g. Actor="ab",
// Action="c" cannot collide with Actor="a", Action="bc"). Details keys are
// sorted, so a map populated in any order hashes identically. A nil Details map
// and an empty one hash identically (both encode a length of zero). Note that
// the content encoding deliberately excludes PrevHash and Hash; PrevHash enters
// the digest via the chain rule below, and Hash is the output.
//
// # Chain rule
//
//	Hash = hex( SHA-256( rawBytes(PrevHash) || CanonicalBytes(content) ) )
//
// where rawBytes(PrevHash) is the 32-byte digest that the hex PrevHash string
// decodes to. The genesis (first) entry uses [GenesisPrevHash] — 64 hex zeros,
// i.e. 32 zero bytes — as its PrevHash, and [FirstSeq] as its Seq. Each
// subsequent entry sets PrevHash to the previous entry's Hash and increments
// Seq by one.
//
// # Tamper detection and its limit
//
// [Verify] recomputes the whole chain from genesis and reports the first index
// that does not check out. It detects content mutation, reordering, deletion,
// insertion and broken PrevHash links (see the function's documentation). The
// one mutation a self-contained chain cannot detect is rewriting the most
// recent entry AND recomputing its hash: the result is internally consistent.
// To close that gap, callers persist or publish the head Hash to an external
// trusted anchor and compare it against the last entry's Hash — anchoring is
// out of scope for this pure engine.
package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

// GenesisPrevHash is the PrevHash of the first entry in any chain: 64 hex zeros
// (a 32-byte all-zero SHA-256 placeholder). It is a well-defined constant so
// that an independent verifier can reconstruct the chain from genesis without
// any out-of-band state.
const GenesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// FirstSeq is the Seq assigned to the genesis entry. Sequence numbers are
// contiguous and increase by one, so the entry at slice index i must have
// Seq == FirstSeq+i.
const FirstSeq uint64 = 1

// canonicalVersion is a domain-separation tag mixed into every canonical
// encoding. Bumping it deliberately changes all hashes, which lets the encoding
// evolve in a later revision without silently colliding with v1 digests.
const canonicalVersion = "purser.audit.v1"

// Entry is one immutable record in the audit chain.
//
// Callers of [Log.Append] supply only the content fields — Actor, Action,
// Target and Details. The chain fields — Seq, TimeUnixNano, PrevHash and Hash —
// are assigned by the log so that the chain invariants always hold.
type Entry struct {
	// Seq is the 1-based position of the entry in the chain (see [FirstSeq]).
	Seq uint64 `json:"seq"`
	// TimeUnixNano is the wall-clock time the entry was recorded, as Unix
	// nanoseconds. An integer form is stored (rather than a time.Time) so the
	// canonical encoding is exact and free of timezone or formatting ambiguity.
	TimeUnixNano int64 `json:"time_unix_nano"`
	// Actor identifies who performed the action (e.g. an API-key ID or user).
	Actor string `json:"actor"`
	// Action is the verb that was performed (e.g. "node.delete").
	Action string `json:"action"`
	// Target identifies what the action was performed on (e.g. a node ID).
	Target string `json:"target"`
	// Details carries optional structured context. Keys are sorted during
	// canonical encoding, so iteration order never affects the hash. May be nil.
	Details map[string]string `json:"details,omitempty"`
	// PrevHash is the hex SHA-256 Hash of the preceding entry, or
	// [GenesisPrevHash] for the first entry.
	PrevHash string `json:"prev_hash"`
	// Hash is the hex SHA-256 digest defined by the chain rule (see the package
	// documentation). It is the only field not covered by [Entry.CanonicalBytes].
	Hash string `json:"hash"`
}

// CanonicalBytes returns the deterministic byte encoding of the entry's content
// fields (everything except PrevHash and Hash), as described in the package
// documentation. Two entries with identical content always produce identical
// bytes regardless of Details map iteration order.
func (e Entry) CanonicalBytes() []byte {
	var buf bytes.Buffer
	writeBytesField(&buf, []byte(canonicalVersion))
	writeUvarint(&buf, e.Seq)
	writeVarint(&buf, e.TimeUnixNano)
	writeBytesField(&buf, []byte(e.Actor))
	writeBytesField(&buf, []byte(e.Action))
	writeBytesField(&buf, []byte(e.Target))

	// Sort the Details keys so the encoding is independent of map ordering.
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writeUvarint(&buf, uint64(len(keys)))
	for _, k := range keys {
		writeBytesField(&buf, []byte(k))
		writeBytesField(&buf, []byte(e.Details[k]))
	}
	return buf.Bytes()
}

// ComputeHash returns the hex SHA-256 chain hash for the entry given its
// PrevHash and content, per the chain rule in the package documentation. It
// does not read or trust the entry's existing Hash field; callers compare the
// result against Hash to detect tampering. An error is returned only when
// PrevHash is not valid hex.
func (e Entry) ComputeHash() (string, error) {
	prev, err := hex.DecodeString(e.PrevHash)
	if err != nil {
		return "", fmt.Errorf("audit: invalid prev hash %q: %w", e.PrevHash, err)
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(e.CanonicalBytes())
	return hex.EncodeToString(h.Sum(nil)), nil
}

// clone returns a deep copy of the entry so that mutations to a returned
// entry's Details map cannot reach into stored state.
func (e Entry) clone() Entry {
	e.Details = cloneDetails(e.Details)
	return e
}

// cloneDetails returns a shallow copy of a details map (nil-preserving).
func cloneDetails(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Verify-failure kinds, exposed on [VerifyError.Kind] for programmatic checks.
const (
	// KindSeq means the entry's Seq is not the expected contiguous value —
	// the signature of a reordered, deleted or inserted entry.
	KindSeq = "seq"
	// KindLink means the entry's PrevHash does not equal the previous entry's
	// Hash — a broken chain link.
	KindLink = "link"
	// KindHash means the entry's stored Hash does not match the hash recomputed
	// from its content — content or hash tampering.
	KindHash = "hash"
)

// VerifyError describes the first point at which a chain fails verification.
type VerifyError struct {
	// Index is the 0-based slice position of the offending entry.
	Index int
	// Seq is the Seq the offending entry carries (useful when Index and Seq
	// disagree, which itself signals reordering or deletion).
	Seq uint64
	// Kind classifies the failure; see [KindSeq], [KindLink] and [KindHash].
	Kind string
	// Msg is a human-readable explanation.
	Msg string
}

// Error implements the error interface.
func (e *VerifyError) Error() string {
	return fmt.Sprintf("audit: chain broken at index %d (seq %d): %s: %s", e.Index, e.Seq, e.Kind, e.Msg)
}

// Verify recomputes the entire chain from genesis and returns nil if it is
// intact, or a [*VerifyError] identifying the first index that fails. An empty
// slice is trivially valid.
//
// For each entry at index i it checks, in order:
//
//   - Seq == FirstSeq+i. A mismatch means the slice was reordered, or an entry
//     was deleted or inserted (all of which perturb the contiguous numbering).
//   - PrevHash equals the previous entry's Hash (or [GenesisPrevHash] at i==0).
//     A mismatch is a broken chain link.
//   - Hash equals the value recomputed by [Entry.ComputeHash]. A mismatch means
//     the content or the stored Hash was altered.
//
// Between them these checks detect content tampering, reordering, deletion,
// insertion and link breakage. See the package documentation for the single
// case a self-contained chain cannot catch (rewriting the tail entry and
// recomputing its hash), which requires an external anchor.
func Verify(entries []Entry) error {
	prev := GenesisPrevHash
	for i, e := range entries {
		if wantSeq := FirstSeq + uint64(i); e.Seq != wantSeq {
			return &VerifyError{
				Index: i,
				Seq:   e.Seq,
				Kind:  KindSeq,
				Msg:   fmt.Sprintf("expected seq %d, got %d", wantSeq, e.Seq),
			}
		}
		if e.PrevHash != prev {
			return &VerifyError{
				Index: i,
				Seq:   e.Seq,
				Kind:  KindLink,
				Msg:   fmt.Sprintf("prev hash %q does not link to preceding hash %q", e.PrevHash, prev),
			}
		}
		want, err := e.ComputeHash()
		if err != nil {
			return &VerifyError{
				Index: i,
				Seq:   e.Seq,
				Kind:  KindHash,
				Msg:   err.Error(),
			}
		}
		if e.Hash != want {
			return &VerifyError{
				Index: i,
				Seq:   e.Seq,
				Kind:  KindHash,
				Msg:   fmt.Sprintf("stored hash %q does not match recomputed hash %q", e.Hash, want),
			}
		}
		prev = e.Hash
	}
	return nil
}

// writeBytesField appends a length-prefixed byte field to buf.
func writeBytesField(buf *bytes.Buffer, b []byte) {
	writeUvarint(buf, uint64(len(b)))
	buf.Write(b)
}

// writeUvarint appends an unsigned varint to buf.
func writeUvarint(buf *bytes.Buffer, v uint64) {
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(scratch[:], v)
	buf.Write(scratch[:n])
}

// writeVarint appends a signed varint to buf.
func writeVarint(buf *bytes.Buffer, v int64) {
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutVarint(scratch[:], v)
	buf.Write(scratch[:n])
}
