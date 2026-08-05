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
// Unlike focus / summarization / contextUsage, these carry no sub-block to key
// off: KAS's buildSessionInfoUpdate spreads the update flat into _meta.kiro and
// its legacyFields() returns {} for all three. So this is the one place the
// cascade dispatches on the kind STRING, and the reason is a measured property
// of the wire rather than a preference.

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
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
func (t *Translator) handleSteeringUpdate(ctx context.Context, chatID api.ChatID, u *sessionInfoUpdate) bool {
	k := &u.Meta.Kiro
	switch k.Kind {
	case kindSteeringQueued:
		if k.MessageID == "" {
			return true
		}
		t.deps.Broadcast(ctx, api.NewEvent(api.EventSteerQueued, chatID, api.SteerQueuedPayload{
			SteerID: k.MessageID,
			Text:    k.Content,
			// Set only for a notification KAS classified by sniffing the text.
			// vibekit refuses to SEND one of those, so a severity arriving here
			// means the message came from a workflow step or a subagent rather
			// than from the user — the client styles it as a notice, not as
			// something the user said.
			Severity: k.NotificationSeverity,
		}))
		return true

	case kindSteeringInjected:
		if k.MessageID == "" {
			return true
		}
		t.deps.Broadcast(ctx, api.NewEvent(api.EventSteerInjected, chatID, api.SteerInjectedPayload{
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
		t.deps.Broadcast(ctx, api.NewEvent(api.EventSteerCleared, chatID, api.SteerClearedPayload{
			SteerIDs: k.MessageIDs,
		}))
		return true
	}
	return false
}
