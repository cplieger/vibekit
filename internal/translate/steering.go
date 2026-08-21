package translate

// Mid-turn steering, the inbound half.
//
// KAS multiplexes three steering signals through session_info_update, and
// vibekit forwards all three as distinct SSE events rather than collapsing them.
// The distinction is the feature: a steer that has been BUFFERED and a steer the
// model has actually READ look identical to a user otherwise, and that is the
// one thing somebody correcting a live turn wants to know.
//
//	steering_queued   → EventSteerQueued    the buffer has it
//	steering_injected → EventSteerInjected  the model has read it
//	steering_cleared  → EventSteerCleared   the boundary dropped it unread
//
// A FOURTH event leaves here, off the queued sub-kind: KAS delivers an agent's
// own progress notice through the same buffer (it is the only inbound channel
// into a live turn), distinguishable only by the severity it carries. That
// becomes EventAgentNotice, because the user's outbound messages and the agent's
// notices belong on different surfaces. See handleSteeringUpdate.
//
// Unlike focus / summarization / contextUsage, these carry no sub-block to key
// off: KAS's buildSessionInfoUpdate spreads the update flat into _meta.kiro and
// its legacyFields() returns {} for all three. So this is the one place the
// cascade dispatches on the kind STRING, and the reason is a measured property
// of the wire rather than a preference.

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// Steering sub-kind names as they appear in `_meta.kiro.kind`.
const (
	kindSteeringQueued   = "steering_queued"
	kindSteeringInjected = "steering_injected"
	kindSteeringCleared  = "steering_cleared"
)

// handleSteeringUpdate forwards a steering sub-kind and reports whether it
// consumed the frame.
//
// Returning a bool rather than being another silent cascade arm keeps the caller
// honest: a steering frame must not fall through to the usage/unknown-kind tail,
// where it would be logged as "carries nothing vibekit consumes" — which is the
// opposite of true now.
//
// A frame whose kind is one of the three but whose ids are empty is dropped
// rather than broadcast. It is still consumed (the kind was recognised), because
// forwarding an event with no id would put a chip on screen that nothing can
// ever resolve or clear.
func (t *Translator) handleSteeringUpdate(ctx context.Context, chatID vibekit.ChatID, u *sessionInfoUpdate) bool {
	k := &u.Meta.Kiro
	switch k.Kind {
	case kindSteeringQueued:
		if k.MessageID == "" {
			return true
		}
		// KAS multiplexes two different authors onto this one sub-kind, and the
		// severity is the only thing that separates them: it is set when KAS
		// sniffed a `[notification/<severity>]` prefix, which vibekit refuses to
		// send (command/steer.go), so a severity here means a workflow step or a
		// subagent is reporting into this chat rather than the user speaking.
		//
		// They leave as different events because their surfaces are different.
		// A steer belongs on the composer's chip row, which is about messages the
		// user is waiting for the agent to read; a notice belongs on the
		// ephemeral stack, because nobody is waiting on it and nobody can
		// discard it. Forwarding both as one event pushed the deciding onto every
		// consumer, and the client got it wrong: an agent's own progress line
		// rendered inside the message box as something the user had typed.
		if k.NotificationSeverity != "" {
			t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventAgentNotice, chatID, vibekit.AgentNoticePayload{
				Severity: k.NotificationSeverity,
				Text:     k.Content,
			}))
			return true
		}
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventSteerQueued, chatID, vibekit.SteerQueuedPayload{
			SteerID: k.MessageID,
			Text:    k.Content,
		}))
		return true

	case kindSteeringInjected:
		if k.MessageID == "" {
			return true
		}
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventSteerInjected, chatID, vibekit.SteerInjectedPayload{
			SteerID: k.MessageID,
			Text:    k.Content,
		}))
		return true

	case kindSteeringCleared:
		if len(k.MessageIDs) == 0 {
			// KAS clears at EVERY turn boundary, so an empty list is the normal
			// case on the vast majority of turns — no steer was outstanding.
			// Broadcasting it would put one dead event on the wire per turn.
			return true
		}
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventSteerCleared, chatID, vibekit.SteerClearedPayload{
			SteerIDs: k.MessageIDs,
		}))
		return true
	}
	return false
}
