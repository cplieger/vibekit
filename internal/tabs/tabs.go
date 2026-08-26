// Package tabs is the open-tab set: what is open, in what order, and at what
// version.
//
// # Why this is a store and not a field
//
// The arrangement used to be a tab_order []string inside ui-state.json, a
// preferences document. It validated nothing against reality — an id for a chat
// that did not exist was stored happily — and it travelled as ONE whole-list
// document, which forced the client to compute membership by set difference and
// read "absent from the incoming list" as "closed elsewhere". That closed tabs
// nobody closed, on the live instance, on 2026-08-25. So the set is modelled
// here: one record per tab, removal stated explicitly, and one version the whole
// collection carries.
//
// This package knows nothing about HTTP or SSE. It persists a set and reports the
// version each mutation produced; the caller turns that into a response and an
// event.
//
// # The lock ordering IS the correctness argument
//
// Two mutexes, and their ORDER is the whole thing. Every mutation runs:
//
//	writeMu.Lock
//	  stateMu.Lock ; clone tabs + version ; stateMu.Unlock
//	  mutate and validate the clone
//	  persist the clone
//	  stateMu.Lock ; publish the clone ; stateMu.Unlock
//	writeMu.Unlock
//
// The writer lock is acquired FIRST, so the state a mutation derives from is read
// inside the serialized section. An earlier revision of this design said "clone
// under stateMu, release it, then mutate and persist under writeMu", and two
// independent reviewers found the same lost write in it: two opens clone the same
// state S0, the first persists S0+A, the second persists its stale S0+B, and A is
// gone from memory AND disk after returning success. Both would even report the
// same next version, so a client's gap check could not detect it.
// TestOpen_ConcurrentOpensSurviveInMemoryAndOnDisk is the test that fails if the
// order is ever reverted.
//
// Two rules hold it together:
//
//   - NO path may take stateMu and then block on writeMu. The ordering would
//     invert and deadlock. Every path here takes writeMu first or stateMu alone.
//   - The caller must broadcast the event for the version a mutation returned
//     BEFORE starting the mutation that produces the next one, or events can
//     leave in a different order from the versions they carry. See the note on
//     Store about why that obligation is the caller's and not enforced here.
//
// List takes only stateMu, so a reader never blocks behind an fsync: it sees the
// old durable state while one runs and the new state only after it succeeded.
//
// # Errors
//
// ErrBadKind, ErrBadRef, ErrTooMany and ErrOrderMismatch are package-scope
// sentinels; each function's doc names the ones it returns and every one of them
// is wrapped with the offending value, so compare with errors.Is and never with
// the message text.
package tabs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/filemode"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// FileName is the store's file, beside the chats directory in the config dir.
const FileName = "tabs.json"

// fileMode and dirMode are what the file and the config directory must carry.
// The arrangement names chat ids and absolute paths, so it is nobody else's
// business on a shared host.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// MaxTabs is the DECODE bound: the most tabs this store will read out of a file.
// It is an outer wall against a hostile or broken writer, not a product number —
// a real strip is tens of tabs, and dropping the tail of a 10,000-entry file is
// the difference between a missing tab and a blank app on the boot path.
const MaxTabs = 500

// MaxOpenTabs is the PRODUCT limit: the most tabs that may be open at once. Open
// refuses past it with ErrTooMany.
//
// A different question from MaxTabs, and it is the one with a user-visible
// consequence: AT THE LIMIT, NEW CHAT STOPS WORKING, because creating a chat
// opens a tab for it. So the number comes from a real strip rather than a guess —
// the live instance's arrangement held seven — and 48 is nearly seven times that
// while still small enough that the strip stays a strip. A client renders the
// refusal as "close a tab first", never as an error.
const MaxOpenTabs = 48

// MaxBytes caps a decoded document, and it is derived from the two bounds above
// rather than picked: MaxTabs tabs each carrying a MaxRefBytes ref plus its other
// fields is roughly 320 KB of indented JSON, so 512 KiB has headroom without
// being a number a hostile writer can grow into. Enforced on the way in (the
// read below) and on the way out (atomicfile's WithMaxBytes), so this store
// refuses to write a file its own load path would refuse to read.
const MaxBytes = 512 * 1024

// MaxRefBytes caps one subject's Ref. A ref is a chat id or an absolute path, and
// the reasoning is vibekit.MaxAttachmentPathBytes's: PATH_MAX is 4096 on Linux
// while every path this app can produce comes from its own file browser under the
// workspace root.
const MaxRefBytes = 512

// maxIDBytes bounds an id read off disk. Nothing this store mints is longer than
// 32 characters; the slack is for a hand-edited file, and the bound is what stops
// one entry from consuming the whole document budget.
const maxIDBytes = 128

// The sentinels this package returns. Compared with errors.Is; each is wrapped
// with the value that offended it, so a caller can report WHICH id or kind was
// wrong without parsing the message.
var (
	// ErrOrderMismatch means the ids handed to Reorder do not name every open
	// tab exactly once. Nothing is applied.
	ErrOrderMismatch = errors.New("tab order does not name every open tab exactly once")
	// ErrTooMany means MaxOpenTabs tabs are already open.
	ErrTooMany = errors.New("too many open tabs")
	// ErrBadKind means the kind is not one of vibekit's eight.
	ErrBadKind = errors.New("unknown tab kind")
	// ErrBadRef means the ref does not fit its kind: missing where the kind
	// needs one, present on a singleton, or over MaxRefBytes.
	ErrBadRef = errors.New("bad tab ref")
)

// file is the on-disk document: the tabs and the version they carry, written
// together because the version describes exactly that set.
//
// There is deliberately no FORMAT version field, unlike internal/runlease's
// file. A lease governs a live run, so a record written by another build has to
// be recognised or discarded deliberately; an arrangement is re-derivable by
// opening the tabs again, and this store's answer to a document it cannot read is
// already to warn and start empty. A second version number would only give two
// things called "version" in one file.
type file struct {
	Tabs    []vibekit.TabSubject `json:"tabs"`
	Version uint64               `json:"version"`
}

// Store is the open-tab set. Safe for concurrent use by multiple goroutines. The
// ZERO VALUE IS NOT USABLE — it would persist to the empty path; construct with
// NewStore.
//
// The slice IS the order and it is the only persistent representation. Lookup
// indexes are built inside the method that needs one and thrown away, so they
// cannot desync across calls: MaxTabs is 500, small enough that a transient map
// is free and large enough that a repeated linear scan inside Reorder would be
// quadratic.
//
// # Event ordering is the caller's, deliberately
//
// A mutation returns the version it committed, and the caller must hand that
// version's event to its hub before it starts the next mutation. That obligation
// is NOT enforced here, and the two ways of enforcing it were both rejected:
//
//   - A WithinWrite(func()) seam cannot work. writeMu is a sync.Mutex, so a
//     mutation called inside a callback that already holds it deadlocks — the
//     seam could not wrap the thing it exists to order.
//   - A commit hook invoked under writeMu would work, and buys nothing yet: the
//     one caller is the membership coordinator, which already holds its own
//     operation lock across both stores for every mutation that spans them, and
//     that lock is the serialization point. A hook would also be unable to carry
//     the event's op_id, which the caller has and the store does not, so the
//     caller would still be assembling the event itself.
//
// The cost of getting it wrong is bounded rather than silent: the client's three
// version rules read a gap (v > local+1) as "re-list", so an out-of-order
// broadcast costs a redundant GET, not a lost tab.
//
// Field order is govet fieldalignment's, which puts the pointer-bearing fields
// first and therefore the mutexes below the state they guard rather than above
// it. Each says what it guards.
type Store struct {
	// path is immutable after construction, so it is read without a lock.
	path    string
	tabs    []vibekit.TabSubject // guarded by stateMu
	version uint64               // guarded by stateMu
	stateMu sync.Mutex           // guards tabs and version; held briefly, NEVER across I/O
	writeMu sync.Mutex           // serialises mutate-and-persist; the I/O lock
}

// NewStore opens (or starts) the store at <dir>/tabs.json.
//
// It ALWAYS returns a usable store; the error is diagnostic, and the caller's job
// is to WARN AND CONTINUE. That is the opposite of internal/schedule's choice and
// deliberately so: a schedule is work the user asked to happen and losing it
// silently is a real loss, while an arrangement is re-derivable by opening the
// tabs again (vibekit invariant 6), and refusing to boot would take the whole app
// down over cosmetic state with no way in to repair it.
//
// THE MODE VERDICT COMES FIRST, before anything reads the bytes, and that
// ordering is load-bearing rather than tidy. filemode.EnforceFile opens with
// O_NOFOLLOW|O_NONBLOCK and reads the mode off the DESCRIPTOR: a symlink planted
// at the name is refused instead of having its target read and parsed as the
// arrangement, and a FIFO at the name is refused instead of blocking os.ReadFile
// inside open(2) forever — on the boot path, which is how a widened mode becomes
// a container that never finishes starting. internal/secretstore learned that in
// the other order.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName)}
	if _, err := filemode.EnforceFile(s.path, fileMode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil // first run
		}
		// Deliberately no read on this branch: the name is exactly what could
		// not be verified.
		return s, fmt.Errorf("verify mode of %s (starting empty): %w", s.path, err)
	}
	data, err := readBounded(s.path)
	if err != nil {
		return s, fmt.Errorf("read %s (starting empty): %w", s.path, err)
	}
	var doc file
	if err := json.Unmarshal(data, &doc); err != nil {
		return s, fmt.Errorf("parse %s (starting empty): %w", s.path, err)
	}
	s.tabs = sanitize(doc.Tabs)
	// The version is adopted as read even when sanitize dropped an entry, so it
	// is not incremented here. Nothing would carry a bump: no event is emitted at
	// load, and a client's authority after a restart is the list endpoint, which
	// returns the sanitized set and its version in one critical section.
	s.version = doc.Version
	return s, nil
}

// readBounded reads the document with the same refusals filemode.EnforceFile
// made, and with the size bound applied to the READ rather than after it.
//
// Hand-rolled over atomicfile.ReadBounded for one reason its own doc states: that
// helper uses os.Open, which FOLLOWS symlinks. Using it immediately after
// EnforceFile refused a symlink would give the refusal away again, so the flags
// are repeated here. io.LimitReader is what makes the bound real — a length check
// after os.ReadFile has already allocated the hostile file.
func readBounded(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("over the %d byte decode bound", MaxBytes)
	}
	return data, nil
}

// subjectKey is the (Kind, Ref) pair uniqueness is keyed on. A named type rather
// than a TabSubject with two fields set, because a half-populated record used as a
// key reads like a tab and is not one.
type subjectKey struct {
	kind vibekit.TabKind
	ref  string
}

// sanitize drops every entry that could not have come from this store and
// truncates to MaxTabs.
//
// Individually rather than by failing the document, because the arrangement's
// failure mode should be a missing tab rather than a blank app. It checks only
// INTRINSIC validity — the fields of one subject, plus the two uniqueness rules
// the store's own invariants rest on. Referential integrity (a chat that no
// longer exists, a parent that is gone) is Prune's, because only the caller knows
// what still resolves.
func sanitize(in []vibekit.TabSubject) []vibekit.TabSubject {
	if len(in) == 0 {
		return nil
	}
	out := make([]vibekit.TabSubject, 0, min(len(in), MaxTabs))
	ids := make(map[string]struct{}, min(len(in), MaxTabs))
	subjects := make(map[subjectKey]struct{}, min(len(in), MaxTabs))
	for _, t := range in {
		if t.ID == "" || len(t.ID) > maxIDBytes {
			continue
		}
		if checkSubject(t.Kind, t.Ref) != nil {
			continue
		}
		if _, dup := ids[t.ID]; dup {
			continue
		}
		// A duplicate (Kind, Ref) is representable in a hand-edited file and is
		// not representable through Open, so the second one is dropped here:
		// every state this store publishes has at most one tab per subject, and
		// an idempotent Open depends on that being true.
		key := subjectKey{kind: t.Kind, ref: t.Ref}
		if _, dup := subjects[key]; dup {
			continue
		}
		ids[t.ID] = struct{}{}
		subjects[key] = struct{}{}
		out = append(out, t)
		if len(out) == MaxTabs {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// persist writes the whole document atomically.
//
// A full rewrite per mutation rather than an incremental edit, the same reasoning
// as internal/schedule's and internal/runlease's stores: the document is a few
// kilobytes and a rewrite is one publication either way.
//
// THE 0600 IS VERIFIED BY THIS WRITE, not by a pass afterwards.
// atomicfile.WriteFile runs its mode enforcement on the OPEN TEMP DESCRIPTOR
// (fchmod then fstat, one handle) and fails with ErrModeNotStored rather than
// publishing a wider file, and the rename publishes that same verified inode. Do
// not add a filemode.EnforceFile call after this: it would re-verify by NAME,
// which is the weaker check. The load path enforces as well, and that covers the
// one thing a write cannot see — a mode widened by something else between two
// writes.
func (s *Store) persist(ctx context.Context, st *state) error {
	data, err := json.MarshalIndent(file{Tabs: st.tabs, Version: st.version}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tabs: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(MaxBytes)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}

// newID mints a tab id: the hex of 16 bytes, the same shape as
// vibekit.NewChatID's body.
//
// crypto/rand rather than math/rand/v2, per the rulebook's rule for anything
// that must be unguessable, throwaway included: this id is an ADDRESS a client
// sends back in close_tab, pin_tab and reorder_tabs, and the cost of an
// unguessable one is nothing.
//
// Unexported because the store is the only thing entitled to mint one — a tab id
// exists to name a row in this collection — and rand.Read has been documented
// since Go 1.24 never to fail, so there is no error to return and no branch a
// caller could act on.
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// checkSubject reports whether (kind, ref) names something this store can hold.
// Shared by Open (which refuses at the door) and sanitize (which drops), so the
// rule cannot differ between the wire and the file.
//
// Returns ErrBadKind or ErrBadRef.
func checkSubject(kind vibekit.TabKind, ref string) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadKind, kind)
	}
	switch {
	case kind.Singleton() && ref != "":
		return fmt.Errorf("%w: kind %q is a singleton and takes no ref, got %q", ErrBadRef, kind, ref)
	case !kind.Singleton() && ref == "":
		return fmt.Errorf("%w: kind %q needs a ref", ErrBadRef, kind)
	case len(ref) > MaxRefBytes:
		return fmt.Errorf("%w: ref is %d bytes, max %d", ErrBadRef, len(ref), MaxRefBytes)
	}
	return nil
}
