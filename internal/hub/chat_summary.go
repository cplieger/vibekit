// Chat archive summary: after a chat is archived, the shared utility
// bridge (same long-lived kiro-cli subprocess auto-rename / commit
// message / PR description use, picking the cheapest model from the
// models service) generates a one-line description that helps the
// user decide whether to restore the chat. The summary lives on the
// archived chat JSON's Summary field and is picked up by the History
// tab.

package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"vibekit/internal/api"
)

// summaryMaxChars caps the summary length so the History tab doesn't
// get a wall of text under the title. Matches the "max 80 chars"
// instruction in the summarizer prompt.
const summaryMaxChars = 80

// summaryMinExchanges gates generation: very short chats (1 turn,
// maybe two) don't need a separate summary; the title already tells
// the story.
const summaryMinExchanges = 2

// summaryContextTurns is how many of the chat's final assistant
// messages feed into the summarization prompt. Keeps the prompt short
// even for long sessions.
const summaryContextTurns = 6

// summaryTimeout bounds how long we wait for the utility bridge.
// Archiving happens on tab-close, and we're running fully async —
// failure means the History row just shows the title, which is fine.
const summaryTimeout = 30 * time.Second

// summarizeOnArchive runs as a goroutine after every successful
// Archive. Reads the archived chat file, asks the utility bridge for
// a one-line summary, and writes the result back via
// Store.UpdateArchivedSummary. Every failure is logged and swallowed;
// the feature must never block archiving.
//
// Model selection is delegated to the shared utility bridge, which
// uses CheapestModel() from the models service (see
// utility_bridge.go). No separate bridge or model pick here.
func (h *Hub) summarizeOnArchive(ctx context.Context, chatID api.ChatID) {
	if len(h.Models()) == 0 {
		// Without a live models catalog the utility bridge cannot
		// pick the cheapest model. Silent no-op; the history row
		// falls back to just the title.
		return
	}

	// Apply a timeout so the full operation (load + prompt + persist)
	// is bounded. The passed ctx already carries done-cancellation
	// from the hub, so shutdown propagates immediately.
	ctx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()

	archived, err := h.chatStore.LoadArchived(ctx, chatID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("summary: read archived chat", "chat_id", chatID, "error", err)
		}
		return
	}

	// Skip short chats: the 2-3 word title is sufficient when only
	// one exchange occurred.
	if countAssistantMessages(archived) < summaryMinExchanges {
		return
	}

	prompt := buildSummaryPrompt(archived)
	if prompt == "" {
		return
	}

	result, err := h.UtilityPrompt(ctx, prompt)
	if err != nil {
		slog.Warn("summary: utility bridge", "chat_id", chatID, "error", err)
		return
	}
	summary := postprocessSummary(result)
	if summary == "" {
		return
	}
	if err := h.chatStore.UpdateArchivedSummary(ctx, chatID, summary); err != nil {
		slog.Warn("summary: persist", "chat_id", chatID, "error", err)
		return
	}
	slog.Info("chat archive summarised", "chat_id", chatID, "summary", summary)
}

// buildSummaryPrompt assembles the utility-bridge prompt. Uses the
// chat's title and the last few assistant messages for context; that
// keeps the prompt under 4KB for even the longest sessions.
func buildSummaryPrompt(c *api.Chat) string {
	var trail []string
	for i := len(c.Messages) - 1; i >= 0 && len(trail) < summaryContextTurns; i-- {
		m := &c.Messages[i]
		if m.Role != api.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		trail = append([]string{text}, trail...)
	}
	if len(trail) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Summarize this chat in one sentence (max %d chars). ", summaryMaxChars)
	b.WriteString("Focus on what was accomplished, not what was discussed. Return ONLY the summary.\n\nTitle: ")
	b.WriteString(c.Name)
	b.WriteString("\n\nRecent assistant messages:\n")
	for i, text := range trail {
		fmt.Fprintf(&b, "\n-- message %d --\n%s\n", i+1, truncateRunes(text, 800))
	}
	return b.String()
}

// postprocessSummary trims the utility bridge's response so it fits
// the cap and doesn't carry stray markdown or quote characters. Mimics
// the cleanup asyncRenameChat applies to generated titles.
func postprocessSummary(raw string) string {
	s := strings.TrimSpace(raw)
	// Truncate at first newline before quote stripping to ensure
	// idempotency: the visible content is settled before we strip.
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	// Drop wrapping quotes models sometimes add. Loop, trimming between layers
	// so interior spaces cannot halt stripping; keeps it idempotent.
	for {
		s = strings.TrimSpace(s)
		if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
			s = s[1 : len(s)-1]
		} else {
			break
		}
	}
	if utf8.RuneCountInString(s) > summaryMaxChars {
		s = truncateRunes(s, summaryMaxChars-1) + "…"
	}
	return s
}

func countAssistantMessages(c *api.Chat) int {
	n := 0
	for i := range c.Messages {
		if c.Messages[i].Role == api.RoleAssistant {
			n++
		}
	}
	return n
}

// truncateRunes returns s limited to at most n runes. Operates on runes
// so multi-byte characters aren't split mid-codepoint. The stdlib-only
// form walks the string once without allocating a rune slice when s
// already fits.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i < len(s) {
		if count == n {
			return s[:i]
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s
}
