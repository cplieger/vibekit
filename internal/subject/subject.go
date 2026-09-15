// Package subject is the version registry behind the SSE digest: one opaque
// version string per (kind, ref) subject, minted by the store that owns the
// subject and read by the resolver and the REST envelopes.
//
// Versions holds its own mutex and no store lock. A writer bumps a version
// INSIDE its own critical section, so the version and the state it
// certifies come out of one section; the registry only records the result.
package subject

import (
	"strconv"
	"sync"
)

// Kind names a digest subject family. The constants are the one spelling both
// the Go emitters and the TypeScript action table read.
type Kind string

// The subject kinds. A workspace-wide kind carries an empty ref.
const (
	KindChats    Kind = "chats"
	KindChat     Kind = "chat"
	KindLiveTurn Kind = "live_turn"
	KindPending  Kind = "pending"
	KindRuns     Kind = "runs"
	KindTabs     Kind = "tabs"
	KindCatalog  Kind = "catalog"
	KindStatus   Kind = "status"
)

// Unminted is the version a counter reports before its first bump this
// process. A REST envelope stamps the same value for the same case, so a
// subject first seen at "0" on both sides compares equal instead of reading
// changed on every digest until its first mint.
const Unminted = "0"

type key struct {
	kind Kind
	ref  string
}

// Versions is the registry. The zero value is ready to use and safe for
// concurrent use.
type Versions struct {
	counters map[key]uint64
	mu       sync.Mutex
}

// BumpCounter increments the counter behind (kind, ref) and returns the new
// value as a decimal string. Successive calls on one key return strictly
// increasing values.
func (v *Versions) BumpCounter(kind Kind, ref string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.counters == nil {
		v.counters = make(map[key]uint64)
	}
	k := key{kind, ref}
	v.counters[k]++
	return strconv.FormatUint(v.counters[k], 10)
}

// Current reports the version of (kind, ref) and whether one has been recorded
// this process. A never-minted counter answers (Unminted, false).
func (v *Versions) Current(kind Kind, ref string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if n, ok := v.counters[key{kind, ref}]; ok {
		return strconv.FormatUint(n, 10), true
	}
	return Unminted, false
}
