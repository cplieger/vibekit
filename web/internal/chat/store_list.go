package chat

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"vibekit/internal/api"
)

// List returns every chat's header (no messages) sorted by UpdatedAt desc.
// Files that fail to parse or read are logged and skipped — one bad file
// must not hide the rest from the sidebar. Always returns a non-nil
// slice so JSON encoders emit `[]` for an empty registry rather than
// `null` (which the wire decoder rejects as a type error).
func (s *Store) List(ctx context.Context) []api.ChatHeader {
	// Coalesce concurrent sidebar refreshes into a single directory scan.
	// listOnce always returns (any, nil), so err is informational only;
	// if a future closure change introduces a non-nil error path, surface
	// it at Warn instead of silently dropping the result.
	v, err, _ := s.listSF.Do("list", func() (any, error) {
		return s.listOnce(ctx), nil
	})
	if err != nil {
		slog.Warn("chat list: singleflight returned error", "error", err)
		return []api.ChatHeader{}
	}
	headers, ok := v.([]api.ChatHeader)
	if !ok {
		slog.Error("chat list: singleflight returned unexpected type",
			"type", fmt.Sprintf("%T", v))
		return []api.ChatHeader{}
	}
	if headers == nil {
		return []api.ChatHeader{}
	}
	return headers
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
