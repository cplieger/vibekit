// Read-only query methods on state. These have a different reason to
// change (new read patterns: Diff, Restore, conflict detection) than
// the mutation side (event kinds, state model evolution). Independently
// testable; Manager synchronizes access via m.mu.

package checkpoint

import (
	"slices"
	"strings"

	chktypes "vibekit/internal/checkpoint/types"
)

// Tag is a validated checkpoint tag. Re-exported from the types
// sub-package for backward compatibility within this package.
type Tag = chktypes.Tag

// ParseTag validates and returns a Tag.
var ParseTag = chktypes.ParseTag

// oldestTag returns the earliest tag in the log, or "" if no tags.
// O(1) thanks to the maintained orderedTags slice.
func (s *state) oldestTag() string {
	if len(s.orderedTags) == 0 {
		return ""
	}
	return s.orderedTags[0]
}

// contentAtTag finds the SHA that represents the content of `path` at
// the state just BEFORE the snapshot with `tag` was taken. That's the
// beforeSHA on the specific snapshot event at `tag` (which is exactly
// what Restore wants — the content that was about to be overwritten).
//
// Returns ("", false) if the file has no snapshot at `tag` or if the
// file didn't exist at that point (beforeSHA empty).
//
// fileHistory entries are appended in tag-ascending order (tags are
// allocated monotonically per chat), so binary search is safe and
// drops the walk from O(N) to O(log N). On pathological chats with
// 10k snapshots of the same file this takes a 10k-way loop down to
// ~14 comparisons.
func (s *state) contentAtTag(path, tag string) (string, bool) {
	history := s.fileHistory[path]
	if len(history) == 0 {
		return "", false
	}
	lo, hi := 0, len(history)
	for lo < hi {
		mid := (lo + hi) / 2
		c := compareTags(history[mid].tag, tag)
		switch {
		case c < 0:
			lo = mid + 1
		case c > 0:
			hi = mid
		default:
			if history[mid].beforeSHA == "" {
				return "", false
			}
			return history[mid].beforeSHA, true
		}
	}
	return "", false
}

// contentAtOrBeforeTag finds the content SHA for `path` at `tag` if an
// exact snapshot exists there, otherwise falls back to the afterSHA of
// the nearest snapshot BEFORE `tag`. This is the correct lookup for Diff
// and CheckoutFile: a file that wasn't snapshotted at exactly `tag` still
// has a known state from its most recent prior snapshot's afterSHA.
//
// Returns ("", false) if the file has no snapshot at or before `tag`.
func (s *state) contentAtOrBeforeTag(path, tag string) (string, bool) {
	history := s.fileHistory[path]
	if len(history) == 0 {
		return "", false
	}
	// Try exact match first (same as contentAtTag).
	lo, hi := 0, len(history)
	best := -1
	for lo < hi {
		mid := (lo + hi) / 2
		c := compareTags(history[mid].tag, tag)
		switch {
		case c < 0:
			best = mid
			lo = mid + 1
		case c > 0:
			hi = mid
		default:
			// Exact match: return beforeSHA (same as contentAtTag).
			if history[mid].beforeSHA == "" {
				return "", false
			}
			return history[mid].beforeSHA, true
		}
	}
	// No exact match: use the afterSHA of the nearest prior snapshot.
	if best >= 0 && history[best].afterSHA != "" {
		return history[best].afterSHA, true
	}
	return "", false
}

// filesTouchedBetween returns the union of every file path modified
// by snapshots strictly after `from` and up to and including `to`.
// Used by Diff so files that were never touched between from/to
// don't appear in the changed-files list.
func (s *state) filesTouchedBetween(from, to string) []string {
	return s.filesInTagRange(from, to, false)
}

// filesTouchedAtOrAfter returns files touched AT or after `from` —
// semantics Restore wants, because restoring TO tag T requires
// reverting every file with a snapshot at tag T (the beforeSHA
// there is the rollback target).
func (s *state) filesTouchedAtOrAfter(from string) []string {
	return s.filesInTagRange(from, "", true)
}

// referencesBlob reports whether any snapshot recorded this chat
// references the given blob SHA. Used by the blob-read endpoint to
// confine access to blobs the requesting chat actually owns —
// prevents chat A from probing chat B's private snapshots via raw
// SHA guessing. O(1) via the blobRefs set maintained in apply.
func (s *state) referencesBlob(sha string) bool {
	if sha == "" {
		return false
	}
	_, ok := s.blobRefs[sha]
	return ok
}

// filesInTagRange walks only the ordered tag slice in the target
// range (binary-searched endpoints) and unions tagFiles for each.
// O(k) where k is "tags in range", vs the prior O(files × history
// per file) which scaled with chat age. On chats with thousands of
// snapshots and just a few recent ones to inspect the saving is
// significant.
//
// The `to` parameter is always expected to be a tag that exists in
// state (callers — Diff, filesTouchedBetween — validate membership
// before invoking). If a future caller passes a synthetic `to`, the
// `!foundTo` branch falls through to `endIdx = ei` which gives the
// half-open upper bound of tags strictly less than `to` — the
// standard Go range convention. Kept explicit rather than deleted
// so a future caller can reason about the behaviour without
// re-deriving it.
func (s *state) filesInTagRange(from, to string, inclusive bool) []string {
	if len(s.orderedTags) == 0 {
		return nil
	}
	// Find the start position: first orderedTags index whose tag
	// qualifies under the inclusive/exclusive rule.
	startIdx, found := findSorted(s.orderedTags, from)
	if !inclusive && found {
		startIdx++ // skip exactly `from` for the exclusive case
	}
	// Find the end position: first index past the upper bound.
	endIdx := len(s.orderedTags)
	if to != "" {
		ei, foundTo := findSorted(s.orderedTags, to)
		if foundTo {
			endIdx = ei + 1 // inclusive upper
		} else {
			endIdx = ei // exclusive upper for synthetic `to`
		}
	}
	seen := map[string]struct{}{}
	var out []string
	for i := startIdx; i < endIdx; i++ {
		for _, path := range s.tagFiles[s.orderedTags[i]] {
			if _, dup := seen[path]; dup {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

// --- tag comparison ---

// compareTags orders "N" and "N.K" tags numerically in turn.tool
// order. Used by replay bookkeeping + Restore walks; package-local.
func compareTags(a, b string) int {
	at, ai := parseTag(a)
	bt, bi := parseTag(b)
	if at != bt {
		if at < bt {
			return -1
		}
		return 1
	}
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	default:
		return 0
	}
}

func parseTag(t string) (turn, tool int) {
	turnStr, toolStr, hasDot := strings.Cut(t, ".")
	if !hasDot {
		toolStr = "0"
	}
	turnN := atoiSafe(turnStr)
	toolN := atoiSafe(toolStr)
	return turnN, toolN
}

// atoiSafe parses a non-negative integer, returning 0 on any
// failure. Tag fields are always server-generated so an unparseable
// value is a bug, not user input — we tolerate it to keep Restore
// from crashing but callers shouldn't rely on the silent zero.
func atoiSafe(s string) int {
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n < 0 {
			return 0 // overflow; absurd in practice
		}
	}
	return n
}

// findSorted returns the insertion index for tag in a (turn, tool)
// ascending slice and whether it already exists there. Binary search
// via slices.BinarySearchFunc with compareTags as the ordering.
func findSorted(tags []string, tag string) (int, bool) {
	return slices.BinarySearchFunc(tags, tag, compareTags)
}
