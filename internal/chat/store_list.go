package chat

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/runesafe/v2"
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

// primeHistoryCap bounds the transcript BuildHistory returns. Bytes rather than
// tokens because no token count exists at prime time: usage arrives later, from
// KAS's usage_update.
const primeHistoryCap = 64 << 10

// primeOmissionNotice tells the model its own input was clipped; without it a
// truncated prime reads as a short conversation.
const primeOmissionNotice = "[%d earlier message(s) omitted to fit the priming budget]\n"

// BuildHistory returns a plain-text transcript for priming, bounded to
// primeHistoryCap, or "" if the chat is missing or empty. Trimming drops WHOLE
// messages, oldest first, so the model is never handed half a sentence. The last
// message always survives; if it alone busts the budget its content is truncated
// with a marker charged INSIDE the cap.
func (s *Store) BuildHistory(ctx context.Context, chatID vibekit.ChatID) string {
	c, ok := s.Get(ctx, chatID)
	if !ok || len(c.Messages) == 0 {
		return ""
	}

	// Render before measuring: role prefixes and tool-call lines cost budget too.
	rendered := make([]string, len(c.Messages))
	for i := range c.Messages {
		rendered[i] = renderPrimeMessage(&c.Messages[i], chatID)
	}

	// Reserved before selecting, so the notice can never push an admitted
	// message over the cap.
	budget := primeHistoryCap - len(fmt.Sprintf(primeOmissionNotice, len(rendered)))
	first, total := selectPrimeWindow(rendered, budget)
	if first == len(rendered) {
		return "" // every message was an unknown role
	}

	var b strings.Builder
	b.Grow(min(total, primeHistoryCap))
	if omitted := countRenderable(rendered[:first]); omitted > 0 {
		slog.Info("chat build_history: transcript trimmed to the priming budget",
			"chat_id", chatID, "omitted", omitted, "kept", len(rendered)-first, "cap", primeHistoryCap)
		fmt.Fprintf(&b, primeOmissionNotice, omitted)
	}
	for _, line := range rendered[first:] {
		if line == "" {
			continue
		}
		// Reachable only for the last message: every earlier one was admitted
		// under budget, which already excluded the notice.
		if b.Len()+len(line) > primeHistoryCap {
			capped, _ := runesafe.SanitizeCapped(line, max(primeHistoryCap-b.Len(), 0), "...")
			b.WriteString(capped)
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// selectPrimeWindow picks the newest run of rendered messages that fits budget,
// returning the index of the oldest one kept and their total size;
// first == len(rendered) means nothing was renderable. The last message is
// admitted unconditionally, so the caller truncates it if it alone busts the cap.
func selectPrimeWindow(rendered []string, budget int) (first, total int) {
	first = len(rendered)
	last := len(rendered) - 1
	for i, line := range slices.Backward(rendered) {
		if line == "" {
			continue // unknown role, already warned
		}
		if total+len(line) > budget && i != last {
			break
		}
		total += len(line)
		first = i
	}
	return first, total
}

// countRenderable counts messages that produced output, so the omission notice
// reports what the model lost rather than array slots.
func countRenderable(rendered []string) int {
	n := 0
	for _, r := range rendered {
		if r != "" {
			n++
		}
	}
	return n
}

// renderPrimeMessage renders one message for the priming transcript, or "" for a
// role this projection does not know how to narrate.
func renderPrimeMessage(m *vibekit.Message, chatID vibekit.ChatID) string {
	var b strings.Builder
	switch m.Role {
	case vibekit.RoleUser:
		b.WriteString("User: ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	case vibekit.RoleAssistant:
		b.WriteString("Assistant: ")
		b.WriteString(m.Content)
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			fmt.Fprintf(&b, "\n  [tool: %s status=%s]", tc.Title, tc.Status)
		}
		b.WriteByte('\n')
	case vibekit.RoleEvent:
		b.WriteString("[")
		b.WriteString(string(m.EventKind))
		b.WriteString("] ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	default:
		slog.Warn("chat build_history: unknown message role, skipped",
			"chat_id", chatID, "msg_id", m.ID, "role", string(m.Role))
		return ""
	}
	return b.String()
}
