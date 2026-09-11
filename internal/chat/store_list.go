package chat

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/singleflight"
)

// sfDo wraps singleflight.Group.Do so the result needs no type assertion at
// each call site.
func sfDo(sf *singleflight.Group, key string, fn func() listResult) listResult {
	v, _, _ := sf.Do(key, func() (any, error) { return fn(), nil })
	r, _ := v.(listResult)
	return r
}

// List returns every chat's header (no messages) sorted by UpdatedAt desc.
// Unreadable files are logged and skipped: one bad file must not hide the rest.
// Never nil, so JSON encoders emit `[]` rather than the `null` the wire decoder
// rejects.
func (s *Store) List(ctx context.Context) []vibekit.ChatHeader {
	headers, _ := s.listWithCompleteness(ctx)
	return headers
}

// listResult carries a scan and its completeness through one singleflight slot.
type listResult struct {
	headers  []vibekit.ChatHeader
	complete bool
}

// ReferencedSessionIDs returns every ACP session id in any kept chat's CHAIN,
// not just its current one, and reports whether that set is COMPLETE.
//
// It backs the orphan session sweep, which reaps any session absent from the
// set, so it FAILS CLOSED: complete is false when a chat file that exists could
// not be read. A chat that vanished mid-scan (ENOENT) is not a failure.
func (s *Store) ReferencedSessionIDs(ctx context.Context) (refs map[string]struct{}, complete bool) {
	refs = make(map[string]struct{})
	headers, complete := s.listWithCompleteness(ctx)
	for i := range headers {
		for _, id := range headers[i].SessionChain() {
			refs[id] = struct{}{}
		}
	}
	return refs, complete
}

// listWithCompleteness is List plus the read-completeness flag the sweep needs,
// coalescing concurrent refreshes into one directory scan.
//
// The shared scan drops the caller's cancellation (values are kept) because
// coalescing makes one caller's lifetime everybody's: the request that opens the
// slot is routinely aborted by a second one already waiting on its answer, and
// both then received a truncated header list.
func (s *Store) listWithCompleteness(ctx context.Context) ([]vibekit.ChatHeader, bool) {
	scanCtx := context.WithoutCancel(ctx)
	r := sfDo(&s.listSF, "list", func() listResult {
		headers, complete := s.listOnce(scanCtx)
		return listResult{headers: headers, complete: complete}
	})
	if r.headers == nil {
		return []vibekit.ChatHeader{}, r.complete
	}
	return r.headers, r.complete
}

func (s *Store) listOnce(ctx context.Context) ([]vibekit.ChatHeader, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat list", "dir", s.dir, "error", err)
		// Nothing is known about what chats exist, so never report complete.
		return []vibekit.ChatHeader{}, false
	}
	var valid []chatEntry
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, chatFileSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, chatFileSuffix)
		if !chatIDPattern(vibekit.ChatID(id)) {
			slog.Debug("chat list: skipped non-chat file",
				"name", name, "reason", "invalid chat id pattern")
			continue
		}
		valid = append(valid, chatEntry{id: id, path: filepath.Join(s.dir, name)})
	}
	if len(valid) == 0 {
		return []vibekit.ChatHeader{}, true
	}

	// No per-chat lock: reads are read-only and writes land by temp+rename, so a
	// reader always sees a complete file.
	headers, complete := readHeadersParallel(ctx, valid, s.fileCap)
	slices.SortFunc(headers, func(a, b vibekit.ChatHeader) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	if !complete {
		// Warned here because List drops the flag: downstream a truncated
		// sidebar is indistinguishable from having fewer chats.
		slog.Warn("chat list: incomplete scan; some chats that exist were not read",
			"dir", s.dir, "found", len(valid), "returned", len(headers))
	}
	slog.Debug("chat list: scan complete",
		"dir", s.dir,
		"entries", len(entries),
		"returned", len(headers),
		"complete", complete)
	return headers, complete
}
