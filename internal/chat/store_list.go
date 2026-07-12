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
	// Coalesce concurrent sidebar refreshes into a single directory scan.
	headers := sfDo(&s.listSF, "list", func() []api.ChatHeader {
		return s.listOnce(ctx)
	})
	if headers == nil {
		return []api.ChatHeader{}
	}
	return headers
}

// ReferencedSessionIDs returns the set of ACP session ids still referenced
// by a chat vibekit keeps — both active and archived. It backs the orphan
// session sweep (internal/kirosession): any on-disk KAS session NOT in this
// set is safe to reap. Headers carry acp_session_id, so this reuses the
// cached List/ListArchived reads without loading full chats.
func (s *Store) ReferencedSessionIDs(ctx context.Context) map[string]struct{} {
	refs := make(map[string]struct{})
	active := s.List(ctx)
	for i := range active {
		if active[i].ACPSessionID != "" {
			refs[active[i].ACPSessionID] = struct{}{}
		}
	}
	archived := s.ListArchived(ctx)
	for i := range archived {
		if archived[i].ACPSessionID != "" {
			refs[archived[i].ACPSessionID] = struct{}{}
		}
	}
	return refs
}

func (s *Store) listOnce(ctx context.Context) []api.ChatHeader {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat list", "dir", s.dir, "error", err)
		return []api.ChatHeader{}
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
		return []api.ChatHeader{}
	}

	// Bounded-parallel header reads. Workers read from a shared index;
	// no per-chat lock needed because readChatHeader is read-only and
	// writes use atomic temp+rename (readers always see a complete file).
	headers := readHeadersParallel(ctx, valid, "chat", s.oldestCheckpoint)
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].UpdatedAt > headers[j].UpdatedAt
	})
	slog.Debug("chat list: scan complete",
		"dir", s.dir,
		"entries", len(entries),
		"returned", len(headers))
	return headers
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
