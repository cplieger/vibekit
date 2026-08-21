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

// sfDo is a typed wrapper around singleflight.Group.Do that eliminates
// the need for a type assertion on the result. The closure always
// returns a listResult, so the assertion is guaranteed by construction.
func sfDo(sf *singleflight.Group, key string, fn func() listResult) listResult {
	v, _, _ := sf.Do(key, func() (any, error) { return fn(), nil })
	r, _ := v.(listResult)
	return r
}

// List returns every chat's header (no messages) sorted by UpdatedAt desc.
// Files that fail to parse or read are logged and skipped — one bad file
// must not hide the rest from the sidebar. Always returns a non-nil
// slice so JSON encoders emit `[]` for an empty registry rather than
// `null` (which the wire decoder rejects as a type error).
func (s *Store) List(ctx context.Context) []vibekit.ChatHeader {
	headers, _ := s.listWithCompleteness(ctx)
	return headers
}

// listResult pairs a header scan with whether it read every chat that
// exists, so both travel through one singleflight slot.
type listResult struct {
	headers  []vibekit.ChatHeader
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
func (s *Store) listWithCompleteness(ctx context.Context) ([]vibekit.ChatHeader, bool) {
	r := sfDo(&s.listSF, "list", func() listResult {
		headers, complete := s.listOnce(ctx)
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
		// The directory itself is unreadable, so nothing is known about what
		// chats exist. Never report that as a complete keep-list.
		return []vibekit.ChatHeader{}, false
	}
	// Collect valid filenames first.
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

	// Bounded-parallel header reads. Workers read from a shared index;
	// no per-chat lock needed because readChatHeader is read-only and
	// writes use atomic temp+rename (readers always see a complete file).
	headers, complete := readHeadersParallel(ctx, valid)
	slices.SortFunc(headers, func(a, b vibekit.ChatHeader) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	slog.Debug("chat list: scan complete",
		"dir", s.dir,
		"entries", len(entries),
		"returned", len(headers),
		"complete", complete)
	return headers, complete
}

// primeHistoryCap bounds the transcript BuildHistory returns.
//
// The prime is the DEGRADED path: it runs only when a model switch fell back to
// a new session or a reload could not `session/load`, so the alternative to a
// good prime is a model that has forgotten the conversation entirely. That
// argues for generosity. Against it: the prime is itself a prompt, so every byte
// spent re-narrating history is a byte the resumed conversation cannot use, and
// an unbounded transcript can exceed the window outright, which fails upstream
// with an opaque error instead of a shortened memory.
//
// 64 KiB is roughly 16k tokens: enough to carry a long working session's recent
// arc, and a small fraction of any window vibekit's models offer. Bytes rather
// than tokens because vibekit cannot count tokens (the context ring reads usage
// from KAS's `usage_update`, which does not exist yet at prime time), and a byte
// budget is the same proxy `MaxInlineTurnBytes` and `pushBodyCap` already use.
const primeHistoryCap = 64 << 10

// primeOmissionNotice tells the model its own input was clipped. Without it a
// truncated prime is indistinguishable from a short conversation, so the model
// answers confidently about a history it was never given.
const primeOmissionNotice = "[%d earlier message(s) omitted to fit the priming budget]\n"

// BuildHistory returns a plain-text transcript used for prime priming, bounded
// to primeHistoryCap. Returns "" if the chat is missing or empty.
//
// Trimming drops WHOLE MESSAGES, oldest first, which is the only honest unit
// here: cutting mid-message would hand the model half a sentence and no way to
// know it. The newest messages are the ones kept, because a resumed
// conversation continues from its end and anything from the start that still
// matters has usually been restated since.
//
// The last message always survives. If it alone exceeds the budget its content
// is truncated with a marker charged INSIDE the cap (the same rule
// push.fitToCap follows), because a prime with no final turn cannot resume
// anything.
func (s *Store) BuildHistory(ctx context.Context, chatID vibekit.ChatID) string {
	c, ok := s.Get(ctx, chatID)
	if !ok || len(c.Messages) == 0 {
		return ""
	}

	// Render first, measure second. Each message's rendered form is what costs
	// budget, so selecting on the raw fields would mis-count the role prefixes
	// and the tool-call lines.
	rendered := make([]string, len(c.Messages))
	for i := range c.Messages {
		rendered[i] = renderPrimeMessage(&c.Messages[i], chatID)
	}

	// Reserve the omission notice's bytes BEFORE selecting, so the notice can
	// never push an already-admitted message over the cap. Computed exactly
	// (the count is bounded by the message total) rather than guessed.
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
		// The single-message overflow case, reachable only for the last message:
		// every earlier one was admitted under `budget`, which already excluded
		// the notice.
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
// returning the index of the oldest one kept and their total size. `first ==
// len(rendered)` means nothing was renderable.
//
// The last message is admitted unconditionally, which is what guarantees a prime
// always carries the turn the conversation resumes from; the caller truncates it
// if it alone busts the cap.
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

// countRenderable counts the messages that would have produced output, so the
// omission notice reports messages the model lost rather than array slots.
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
