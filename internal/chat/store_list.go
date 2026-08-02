package chat

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/singleflight"
)

// sfDo is a typed wrapper around singleflight.Group.Do that eliminates
// the need for a type assertion on the result. The closure always
// returns the concrete type T, so the assertion is guaranteed by
// construction.
func sfDo[T any](sf *singleflight.Group, key string, fn func() T) T {
	v, _, _ := sf.Do(key, func() (any, error) { return fn(), nil })
	t, _ := v.(T)
	return t
}

// List returns every chat's header (no messages) sorted by UpdatedAt desc.
// Files that fail to parse or read are logged and skipped — one bad file
// must not hide the rest from the sidebar. Always returns a non-nil
// slice so JSON encoders emit `[]` for an empty registry rather than
// `null` (which the wire decoder rejects as a type error).
func (s *Store) List(ctx context.Context) []api.ChatHeader {
	headers, _ := s.listWithCompleteness(ctx)
	return headers
}

// listResult pairs a header scan with whether it read every chat that
// exists, so both travel through one singleflight slot.
type listResult struct {
	headers  []api.ChatHeader
	complete bool
}

// ReferencedSessionIDs returns every ACP session id still referenced by a chat
// vibekit keeps, and reports whether that set is COMPLETE.
//
// One list, because chats no longer move: there is no archive directory to
// union in. That collapse is the point — the old two-list form ANDed two
// completeness flags, so an unreadable file in either location suppressed the
// whole sweep.
//
// It backs the orphan session sweep (internal/kirosession): any on-disk KAS
// session not in this set is treated as reapable, which makes an incomplete
// answer here a data-loss bug rather than a stale read. Two properties are
// therefore load-bearing:
//
// Every id in a chat's CHAIN counts, not just its current one. A chat
// routinely changes session (a failed session/load, a model-switch fallback)
// and each abandoned session directory still holds that period's transcript
// and pre-images.
//
// It FAILS CLOSED. `complete` is false when any chat file that exists could
// not be read, because a chat dropped from the list takes its sessions'
// keep-entries with it and the next sweep deletes them. A chat that vanished
// mid-scan (ENOENT — a concurrent delete) is not a failure: it genuinely has
// no sessions to keep, and treating it as one would wedge the sweep forever.
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

// listWithCompleteness is List plus the read-completeness flag the sweep
// needs. List itself stays best-effort for the UI, where showing most of the
// sidebar beats showing none of it.
//
// Coalesces concurrent sidebar refreshes into a single directory scan.
func (s *Store) listWithCompleteness(ctx context.Context) ([]api.ChatHeader, bool) {
	r := sfDo(&s.listSF, "list", func() listResult {
		headers, complete := s.listOnce(ctx)
		return listResult{headers: headers, complete: complete}
	})
	if r.headers == nil {
		return []api.ChatHeader{}, r.complete
	}
	return r.headers, r.complete
}

func (s *Store) listOnce(ctx context.Context) ([]api.ChatHeader, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat list", "dir", s.dir, "error", err)
		// The directory itself is unreadable, so nothing is known about what
		// chats exist. Never report that as a complete keep-list.
		return []api.ChatHeader{}, false
	}
	// Collect valid filenames first.
	var valid []chatEntry
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, chatFileSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, chatFileSuffix)
		if !chatIDPattern(api.ChatID(id)) {
			slog.Debug("chat list: skipped non-chat file",
				"name", name, "reason", "invalid chat id pattern")
			continue
		}
		valid = append(valid, chatEntry{id: id, path: filepath.Join(s.dir, name)})
	}
	if len(valid) == 0 {
		return []api.ChatHeader{}, true
	}

	// Bounded-parallel header reads. Workers read from a shared index;
	// no per-chat lock needed because readChatHeader is read-only and
	// writes use atomic temp+rename (readers always see a complete file).
	headers, complete := readHeadersParallel(ctx, valid, "chat")
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].UpdatedAt > headers[j].UpdatedAt
	})
	slog.Debug("chat list: scan complete",
		"dir", s.dir,
		"entries", len(entries),
		"returned", len(headers),
		"complete", complete)
	return headers, complete
}

// ChildrenOf returns the IDs of chats whose ParentChatID equals parentID.
// Scans headers without loading messages — fast for the common case of
// zero children.
func (s *Store) ChildrenOf(ctx context.Context, parentID api.ChatID) []api.ChatID {
	headers := s.List(ctx)
	var children []api.ChatID
	for i := range headers {
		if headers[i].ParentChatID == parentID {
			children = append(children, api.ChatID(headers[i].ID))
		}
	}
	return children
}

// BuildHistory returns the chat as a plain-text transcript for compression
// priming. Returns empty string if the chat does not exist or has no
// messages.
func (s *Store) BuildHistory(ctx context.Context, chatID api.ChatID) string {
	c, ok := s.Get(ctx, chatID)
	if !ok || len(c.Messages) == 0 {
		return ""
	}
	var b strings.Builder
	for i := range c.Messages {
		m := &c.Messages[i]
		switch m.Role {
		case api.RoleUser:
			b.WriteString("User: ")
			b.WriteString(m.Content)
			b.WriteByte('\n')
		case api.RoleAssistant:
			b.WriteString("Assistant: ")
			b.WriteString(m.Content)
			for j := range m.ToolCalls {
				tc := &m.ToolCalls[j]
				fmt.Fprintf(&b, "\n  [tool: %s status=%s]", tc.Title, tc.Status)
			}
			b.WriteByte('\n')
		case api.RoleEvent:
			b.WriteString("[")
			b.WriteString(string(m.EventKind))
			b.WriteString("] ")
			b.WriteString(m.Content)
			b.WriteByte('\n')
		default:
			slog.Warn("chat build_history: unknown message role, skipped",
				"chat_id", chatID, "msg_id", m.ID, "role", string(m.Role))
		}
	}
	return b.String()
}
