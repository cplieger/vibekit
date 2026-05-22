// Diff logic: per-file change computation between two tags. Extracted
// from manager_snapshot.go because diff presentation has a different
// reason to change than the snapshot-write path.

package checkpoint

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// FileStatus is a typed string for diff result statuses. Prevents
// typos like "a" or "Modified" from compiling — the client switches
// on these exact string values.
type FileStatus string

const (
	FileAdded    FileStatus = "A"
	FileModified FileStatus = "M"
	FileDeleted  FileStatus = "D"
)

// FileChange is one entry returned by Diff. Mirrors the shape the
// client already consumes — the diff endpoint response format is
// preserved across the rewrite.
type FileChange struct {
	Status       FileStatus `json:"status"` // "A" added, "M" modified, "D" deleted
	Path         string     `json:"path"`
	LinesAdded   int        `json:"lines_added"`
	LinesRemoved int        `json:"lines_removed"`
}

// Diff returns per-file changes between two tags. Walks the event
// log for every file touched in (from, to], compares the stored
// blobs at the two endpoints, and returns a line-delta summary.
func (m *Manager) Diff(ctx context.Context, from, to Tag) ([]FileChange, error) {
	m.mu.Lock()
	if err := m.ensureLoaded(ctx); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if _, ok := m.state.tags[string(from)]; !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: from=%q", ErrTagNotFound, from)
	}
	if _, ok := m.state.tags[string(to)]; !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: to=%q", ErrTagNotFound, to)
	}
	// Normalize to ascending (from < to) so line counts are
	// computed consistently. Note this flips semantic
	// "from"/"to": a reverse query (to < from) returns the same
	// Added/Removed counts but labels Status as if walking
	// forward (Status "A" = "exists at normalized to, not at
	// normalized from"), not as "undo of the forward diff".
	// Callers that need direction-sensitive Status must order
	// the tags themselves before calling Diff.
	fromStr, toStr := string(from), string(to)
	if compareTags(fromStr, toStr) > 0 {
		fromStr, toStr = toStr, fromStr
	}
	paths := m.state.filesTouchedBetween(fromStr, toStr)

	// Snapshot the (path, fromSHA, toSHA) tuples under the lock,
	// then release it before performing blob reads and LCS
	// computation. The state fields Diff reads (tags, fileHistory,
	// tagFiles) are append-only after replay and never mutated by
	// concurrent operations in a way that invalidates a point-in-
	// time read.
	type diffEntry struct {
		path      string
		fromSHA   string
		toSHA     string
		fromExist bool
		toExist   bool
	}
	entries := make([]diffEntry, 0, len(paths))
	for _, p := range paths {
		fromSHA, fromExisted := m.state.contentAtOrBeforeTag(p, fromStr)
		toSHA, toExisted := m.state.contentAtTag(p, toStr)
		if fromSHA == toSHA && fromExisted == toExisted {
			continue
		}
		entries = append(entries, diffEntry{
			path:      p,
			fromSHA:   fromSHA,
			toSHA:     toSHA,
			fromExist: fromExisted,
			toExist:   toExisted,
		})
	}
	m.mu.Unlock()

	// Blob reads and LCS computation proceed without holding m.mu.
	// Bounded-concurrency fan-out: each goroutine reads its two blobs
	// and computes the line delta independently. The blob store is safe
	// for concurrent reads (content-addressed, immutable after write).
	out := make([]FileChange, len(entries))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, e := range entries {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			fc := FileChange{Path: e.path}
			switch {
			case !e.fromExist && e.toExist:
				fc.Status = FileAdded
			case e.fromExist && !e.toExist:
				fc.Status = FileDeleted
			default:
				fc.Status = FileModified
			}
			fromData := m.safeGetBlob(gctx, e.fromSHA)
			toData := m.safeGetBlob(gctx, e.toSHA)
			added, removed := countLineDelta(fromData, toData)
			fc.LinesAdded = added
			fc.LinesRemoved = removed
			out[i] = fc
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	// Filter out zero-value entries (shouldn't happen but defensive).
	result := make([]FileChange, 0, len(out))
	for _, fc := range out {
		if fc.Path != "" {
			result = append(result, fc)
		}
	}
	return result, nil
}
