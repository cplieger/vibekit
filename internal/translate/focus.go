package translate

// v3 (KAS) focus updates: the agent's self-declared title, description and status,
// carried as a session_info_update with _meta.kiro.kind == "focus_update". THREE
// writers feed the channel and no field says which one spoke — the agent's tool, KAS's
// first-prompt derivation, and KAS's LLM title — so the filters below go on shape, and
// they run BEFORE the write because adoption is a one-way latch.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// focusUpdate is the _meta.kiro.focus block of a focus_update
// session_info_update. All fields are optional partial updates.
type focusUpdate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

// handleFocusUpdate applies one focus_update: adopt the title onto the chat record
// unless a rule refuses it, and broadcast status/description as an ephemeral
// chat_status event. Parent-only by construction — HandleSessionInfoUpdate drops
// subagent frames before dispatching here.
//
// Two filters at two depths, because they take different inputs: the door rule is
// about the string alone and runs here, while the derivation filter needs the chat's
// own messages and so runs inside applyFocusTitle's Mutate.
func (t *Translator) handleFocusUpdate(ctx context.Context, chatID vibekit.ChatID, f *focusUpdate) {
	if title := SanitizeTitle(f.Title); title != "" {
		t.adoptOrRefuseTitle(ctx, chatID, title)
	}
	status := strings.TrimSpace(f.Status)
	// displayText alone, without the title's sanitize.Output wrapper: display_text.go
	// owns why a single-line surface replaces a hidden rune rather than deleting it.
	// It runs BEFORE the both-empty return because a control-only description empties
	// here, and chatStatusCache.Merge reads a both-empty payload as a clear.
	desc := strings.TrimSpace(displayText(f.Description))
	if status == "" && desc == "" {
		return
	}
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventChatStatus, chatID, vibekit.ChatStatusPayload{
		Status:      status,
		Description: desc,
	}))
}

// adoptOrRefuseTitle writes a sanitized focus title to the chat, or says why not.
// Refusing leaves whatever name the chat has, which is always the better answer: the
// local first-prompt label is a real name, and the default placeholder at least still
// accepts the real title when it arrives.
func (t *Translator) adoptOrRefuseTitle(ctx context.Context, chatID vibekit.ChatID, title string) {
	reason := TitleRefusal(title)
	switch reason {
	case "":
		t.applyFocusTitle(ctx, chatID, title)
	// A truncated title is KAS's own derivation, expected traffic at 164 of 494
	// adoptions in 30 days, so it must not bury the shapes that mean a rule misfired.
	case refusalTruncated:
		slog.Debug("focus title refused", "chat_id", chatID, "title", title, "reason", reason)
	default:
		slog.Warn("focus title refused", "chat_id", chatID, "title", title, "reason", reason)
	}
}

// applyFocusTitle writes an agent-authored title onto the chat. The
// derivation filter runs inside the Mutate closure because it needs the
// chat's messages; Mutate broadcasts chat_updated on change, which is what
// flips the tab label live.
func (t *Translator) applyFocusTitle(ctx context.Context, chatID vibekit.ChatID, title string) {
	renamed := false
	err := t.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists || c.Name == title || titleIsPromptDerived(title, c) {
			return false
		}
		c.Name = title
		renamed = true
		return true
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("focus title: persist", "chat_id", chatID, "error", err)
		return
	}
	if renamed {
		slog.Info("chat titled by agent focus update", "chat_id", chatID, "title", title)
	}
}

// titleIsPromptDerived reports whether title is KAS's first-prompt derivation rather
// than an agent-authored name, which is what implements rung 1 beating rung 2.
//
// The comparison is against kasDerivedTitle rather than the raw message because SV
// normalizes five ways before truncating: measured on the live volume, two of two
// adopted "agent focus titles" were derivations a byte-exact filter passed, both
// differing only by the prompt's lowercase first letter. A title SV had to truncate
// never reaches here, since the door refuses every "..."-suffixed title.
func titleIsPromptDerived(title string, c *vibekit.Chat) bool {
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.Role != vibekit.RoleUser {
			continue
		}
		if kasDerivedTitle(m.Content) == title {
			return true
		}
	}
	return false
}

// kasFillerPhrases mirrors KAS's utc alternation, IN ITS ORIGINAL ORDER: a
// JavaScript regex alternation is leftmost-first, so "help me to" must be
// tried before "help me" or the shorter phrase wins and this mirror strips
// less than KAS did. The `'?`-optional spellings are expanded in place.
var kasFillerPhrases = []string{
	"hi", "hey", "hello", "please", "pls", "plz",
	"can you", "could you", "would you", "will you", "can u", "could u",
	"i want to", "i wanna", "i'd like to", "id like to",
	"i would like to", "i need to", "i need you to",
	"i'm trying to", "im trying to", "i am trying to",
	"help me to", "help me", "let's", "lets", "let me",
	"we need to", "we should",
}

// kasMaxFillerStrips is KAS's ltc: dtc strips at most six leading fillers.
const kasMaxFillerStrips = 6

// kasDerivedTitle returns the title KAS's SV would derive from text, read off the
// pinned bundle (2.21.2-f6262ea4…, `function SV(e)`) — where to re-read it after a
// kiro-cli bump. Its five steps are the five named helpers below.
//
// It stops short of SV's own 80-rune truncation: the door refuses every truncated
// title, so no truncated derivation reaches the comparison this feeds. Every other
// case-mapping or length divergence from SV is a MISS rather than an over-filter — it
// can only adopt KAS's derivation where vibekit's own label would have gone.
func kasDerivedTitle(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	line := firstNonBlankLine(trimmed)
	stripped := trimLeadingMarkup(line)
	out := stripFillerPhrases(stripped)
	// SV's own fallbacks: a prompt that is nothing BUT fillers keeps the
	// pre-strip text rather than deriving an empty title.
	if out == "" {
		if stripped != "" {
			out = stripped
		} else {
			out = line
		}
	}
	out = trimTrailingPunctuation(out)
	if out == "" {
		out = line
	}
	return upperFirstRune(out)
}

// firstNonBlankLine mirrors SV's line selection: split on \r?\n and take the
// first line with non-space content, trimmed.
func firstNonBlankLine(s string) string {
	for line := range strings.Lines(s) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// isKASMarkup reports membership of SV's leading-markup class.
func isKASMarkup(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(">#*`\"'", r)
}

// isKASTrailingPunctuation reports membership of SV's trailing class.
func isKASTrailingPunctuation(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(".,;:!?", r)
}

func trimLeadingMarkup(s string) string { return strings.TrimLeftFunc(s, isKASMarkup) }

func trimTrailingPunctuation(s string) string {
	return strings.TrimRightFunc(s, isKASTrailingPunctuation)
}

// stripFillerPhrases mirrors KAS's dtc: up to six passes, each removing one
// leading filler plus the punctuation and whitespace behind it, then
// re-stripping leading markup. It stops early when a pass changes nothing.
func stripFillerPhrases(s string) string {
	out := s
	for range kasMaxFillerStrips {
		next := stripOneFillerPhrase(out)
		if next == out {
			break
		}
		out = trimLeadingMarkup(next)
	}
	return out
}

// stripOneFillerPhrase removes the first matching filler, or returns s
// unchanged. The lookahead KAS spells `(?=[.,;:!?]*(?:\s|$))` is what stops
// "hi" eating the head of "hidden bug": a filler only counts when the next
// thing after its optional punctuation is whitespace or the end of the line.
func stripOneFillerPhrase(s string) string {
	lower := strings.ToLower(s)
	for _, phrase := range kasFillerPhrases {
		if !strings.HasPrefix(lower, phrase) {
			continue
		}
		rest := s[len(phrase):]
		if !fillerBoundaryFollows(rest) {
			continue
		}
		return strings.TrimLeftFunc(rest, isKASTrailingPunctuation)
	}
	return s
}

// fillerBoundaryFollows reports whether rest opens with the lookahead's
// `[.,;:!?]*` run followed by whitespace or end of string.
func fillerBoundaryFollows(rest string) bool {
	for _, r := range rest {
		if strings.ContainsRune(".,;:!?", r) {
			continue
		}
		return unicode.IsSpace(r)
	}
	return true
}

// upperFirstRune mirrors SV's final `charAt(0).toUpperCase()`.
func upperFirstRune(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+utf8.RuneLen(r):]
	}
	return s
}
