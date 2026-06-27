// Diff logic: per-file change computation between two tags. Extracted
// from manager_snapshot.go because diff presentation has a different
// reason to change than the snapshot-write path.

package checkpoint

import (
	"context"
	"fmt"

	chktypes "github.com/cplieger/vibekit/internal/checkpoint/types"
	"golang.org/x/sync/errgroup"
)

// FileStatus is a typed string for diff result statuses. Re-exported
// from the types sub-package for backward compatibility.
type FileStatus = chktypes.FileStatus

// FileAdded and the following constants re-export the FileStatus values from the types sub-package for backward compatibility.
const (
	FileAdded    = chktypes.FileAdded
	FileModified = chktypes.FileModified
	FileDeleted  = chktypes.FileDeleted
)

// FileChange is one entry returned by Diff. Re-exported from the
// types sub-package for backward compatibility.
type FileChange = chktypes.FileChange

// diffEntry is one file's pre-snapshotted (fromSHA, toSHA) tuple plus
// its existence at each endpoint, captured under m.mu so the blob
// reads and LCS computation can run without the lock.
type diffEntry struct {
	path      string
	fromSHA   string
	toSHA     string
	fromExist bool
	toExist   bool
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
	entries := m.collectDiffEntriesLocked(string(from), string(to))
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
			out[i] = m.computeFileChange(gctx, e)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return filterNonEmptyChanges(out), nil
}

// collectDiffEntriesLocked snapshots the (path, fromSHA, toSHA) tuples
// for every file touched between from and to, skipping files unchanged
// at both endpoints. Callers must hold m.mu; the state fields it reads
// (tags, fileHistory, tagFiles) are append-only after replay so the
// point-in-time read stays consistent once the lock is released.
//
// Normalizes to ascending (from < to) so line counts are computed
// consistently. Note this flips semantic "from"/"to": a reverse query
// (to < from) returns the same Added/Removed counts but labels Status
// as if walking forward (Status "A" = "exists at normalized to, not at
// normalized from"), not as "undo of the forward diff". Callers that
// need direction-sensitive Status must order the tags themselves before
// calling Diff.
func (m *Manager) collectDiffEntriesLocked(from, to string) []diffEntry {
	fromStr, toStr := from, to
	if compareTags(fromStr, toStr) > 0 {
		fromStr, toStr = toStr, fromStr
	}
	paths := m.state.filesTouchedBetween(fromStr, toStr)
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
	return entries
}

// computeFileChange reads the two endpoint blobs for one entry and
// returns its FileChange (status + line delta). Runs without m.mu held;
// the blob store is safe for concurrent reads.
func (m *Manager) computeFileChange(ctx context.Context, e diffEntry) FileChange {
	fc := FileChange{Path: e.path}
	switch {
	case !e.fromExist && e.toExist:
		fc.Status = FileAdded
	case e.fromExist && !e.toExist:
		fc.Status = FileDeleted
	default:
		fc.Status = FileModified
	}
	fromData := m.safeGetBlob(ctx, e.fromSHA)
	toData := m.safeGetBlob(ctx, e.toSHA)
	fc.LinesAdded, fc.LinesRemoved = countLineDelta(ctx, fromData, toData)
	return fc
}

// filterNonEmptyChanges drops zero-value entries (a goroutine that
// short-circuited on a cancelled context leaves an empty slot).
func filterNonEmptyChanges(changes []FileChange) []FileChange {
	result := make([]FileChange, 0, len(changes))
	for _, fc := range changes {
		if fc.Path != "" {
			result = append(result, fc)
		}
	}
	return result
}
