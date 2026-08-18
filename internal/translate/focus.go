package translate

// v3 (KAS) focus updates: the agent's self-declared session title, activity
// description, and status, carried as a session_info_update with
// _meta.kiro.kind == "focus_update".
//
// Two writers feed that channel upstream (probe-verified on the live
// 2.12.1 wire, see kiro-cli-research.md):
//
//   - The model calling its update_session_information tool mid-turn.
//     Titles are short and IDE-grade ("Photo organizer CLI setup"), often
//     with a description and a status. These are worth adopting: they are
//     refreshed when the chat's focus shifts and paid for by tokens the
//     main turn already spends. This is the TOP of the naming precedence.
//   - KAS's first-prompt title derivation: a dumb trim+truncate of the
//     raw prompt text (80 chars, "..." ellipsis), emitted title-only.
//     These must NOT clobber the agent-authored title above, and they add
//     nothing over the identical label the first prompt already set
//     locally. titleIsPromptDerived filters them by shape: a derived
//     title is the prompt text itself, or its "..."-suffixed truncation,
//     and on a freshly primed session it is a truncation of the priming
//     preamble.
//
// Both arrive on the SAME channel, which is why the filter exists rather
// than a source check. Full precedence: agent focus title > local
// first-prompt label > KAS's stored session title (adoptKASTitle in
// hub/bridge_coord.go, which only ever names an unnamed chat).
//
// Titles land on the chat record (Mutate broadcasts chat_updated).
// Status + description broadcast
// as an ephemeral chat_status SSE — deliberately unpersisted, so a server
// restart or reconnect gap resets tabs to neutral instead of replaying a
// stale "in_progress".

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/api"
)

// PrimePreambleSwitch is the fixed prefix of the invisible priming prompt the
// bridge coordinator sends on a fresh session after a model-switch fallback
// (hub bridge_coord.go builds the prime as preamble + history). Exported so the
// coordinator and the focus filter share one definition — KAS derives a
// first-prompt title from whatever prompt text it sees first, and on a primed
// session that is this text.
//
// There was a second preamble for a degraded rewind. It went with the fork: a
// rewind reverts the session it is in, so no fresh session ever needs the
// pre-rewind history injected into it.
const PrimePreambleSwitch = "The context was just switched (new agent, new model, " +
	"or both). Below is the full conversation history. Read it " +
	"silently and reply with a single short line confirming " +
	"you're caught up.\n\n"

// PrimePreambleReload is the counterpart for a fresh session created because
// `session/load` FAILED. Same instruction, different first sentence, and the
// difference matters: telling the model its context was "just switched" when
// nothing was switched is a small lie in the one message it reads before
// everything else.
//
// Before this existed, that fallback primed NOTHING — primeReason had a single
// member and the prime switch's `default` arm returns — so a chat whose reload
// failed came back with its transcript on screen and an agent that had never
// seen it.
const PrimePreambleReload = "The previous session could not be reloaded, so this " +
	"is a fresh one. Below is the full conversation history. Read it " +
	"silently and reply with a single short line confirming " +
	"you're caught up.\n\n"

// PrimePreambleTangent is the TANGENT's fallback preamble: a tangent whose
// `session/fork` was refused, so the parent's context could not be inherited and
// has to be injected instead (see command/fork.go).
//
// Its first sentence differs from the other two for the reason PrimePreambleReload
// exists: nothing was switched and nothing failed to reload, and telling the model
// either would be a small lie in the one message it reads before everything else.
// What it does say is that the history belongs to a conversation this one branched
// FROM — the only preamble here whose transcript is another chat's.
const PrimePreambleTangent = "This conversation is a tangent branched off another " +
	"one. Below is the full history of the conversation it came from. Read it " +
	"silently and reply with a single short line confirming " +
	"you're caught up.\n\n"

// primePreambles is every preamble the coordinator can send. The focus filter
// walks all of them: KAS derives a first-prompt title from whatever text it sees
// first, so a preamble missing from this list becomes a chat title.
var primePreambles = []string{PrimePreambleSwitch, PrimePreambleReload, PrimePreambleTangent}

// derivedTitleEllipsis matches KAS's SESSION_TITLE_ELLIPSIS.
const derivedTitleEllipsis = "..."

// maxFocusTitleRunes caps an adopted focus title. KAS itself caps at 80;
// the clamp here is a guard against a malformed frame, not a formatter.
const maxFocusTitleRunes = 80

// focusUpdate is the _meta.kiro.focus block of a focus_update
// session_info_update. All fields are optional partial updates.
type focusUpdate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

// handleFocusUpdate applies one focus_update: adopt the title onto the
// chat record (unless it is a first-prompt derivation) and broadcast
// status/description as an ephemeral chat_status event. Parent-only by
// construction — HandleSessionInfoUpdate drops subagent frames before
// dispatching here.
func (t *Translator) handleFocusUpdate(ctx context.Context, chatID api.ChatID, f *focusUpdate) {
	if title := strings.TrimSpace(f.Title); title != "" && utf8.RuneCountInString(title) <= maxFocusTitleRunes {
		t.applyFocusTitle(ctx, chatID, title)
	}
	status := strings.TrimSpace(f.Status)
	desc := strings.TrimSpace(f.Description)
	if status == "" && desc == "" {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventChatStatus, chatID, api.ChatStatusPayload{
		Status:      status,
		Description: desc,
	}))
}

// applyFocusTitle writes an agent-authored title onto the chat. The
// derivation filter runs inside the Mutate closure because it needs the
// chat's messages; Mutate broadcasts chat_updated on change, which is what
// flips the tab label live.
func (t *Translator) applyFocusTitle(ctx context.Context, chatID api.ChatID, title string) {
	renamed := false
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists || c.Name == title || titleIsPromptDerived(title, c) {
			return false
		}
		c.Name = title
		renamed = true
		return true
	}); err != nil {
		slog.Error("focus title: persist", "chat_id", chatID, "error", err)
		return
	}
	if renamed {
		slog.Info("chat titled by agent focus update", "chat_id", chatID, "title", title)
	}
}

// titleIsPrimeDerived reports whether an ellipsized title is the head of any
// priming preamble. Split out so titleIsPromptDerived stays inside the
// complexity budget as preambles are added.
func titleIsPrimeDerived(stripped string) bool {
	for _, preamble := range primePreambles {
		if strings.HasPrefix(preamble, stripped) {
			return true
		}
	}
	return false
}

// titleIsPromptDerived reports whether title is KAS's first-prompt
// derivation rather than an agent-authored name. A derived title is the
// trimmed prompt text verbatim (short prompt) or its "..."-suffixed
// truncation (long prompt); the prompt KAS saw is either one of the
// chat's user messages or, on a freshly primed session, the priming
// preamble + transcript.
func titleIsPromptDerived(title string, c *api.Chat) bool {
	stripped, ellipsized := strings.CutSuffix(title, derivedTitleEllipsis)
	// The prime is always far longer than the title cap, so a prime-derived
	// title is always ellipsized — only check there.
	if ellipsized && titleIsPrimeDerived(stripped) {
		return true
	}
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.Role != api.RoleUser {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if ellipsized {
			if strings.HasPrefix(text, stripped) {
				return true
			}
			continue
		}
		if text == title {
			return true
		}
	}
	return false
}
