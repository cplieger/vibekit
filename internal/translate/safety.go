package translate

// v3 (KAS) Infrastructure-Safety notification handlers. These fire only when the client declares
// the infrastructureSafety capability AND an AWS governance flag is on, so never on Builder-ID.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// knownSafetyStatuses gates translation to the documented GateStatus set, so an unrecognized
// status is dropped rather than surfaced as a mystery banner.
var knownSafetyStatuses = map[vibekit.SafetyStatus]struct{}{
	vibekit.SafetyStatusIdle:        {},
	vibekit.SafetyStatusFormalizing: {},
	vibekit.SafetyStatusEvaluating:  {},
	vibekit.SafetyStatusBlocked:     {},
	vibekit.SafetyStatusError:       {},
}

// v3SafetyStatusChanged is the _kiro/safety/statusChanged wire shape. It carries no sessionId
// (KAS routes per-session on the outbound, not in params), so it is broadcast chat-scoped from
// the delivering bridge.
type v3SafetyStatusChanged struct {
	Status            string   `json:"status"`
	Detail            string   `json:"detail"`
	ToolID            string   `json:"toolId"`
	BlockedProperties []string `json:"blockedProperties"`
}

// v3SafetyPropertiesChanged is the _kiro/safety/propertiesChanged wire shape; properties[] is
// decoded tolerantly (object or bare string) by decodeSafetyProps.
type v3SafetyPropertiesChanged struct {
	SessionID  string            `json:"sessionId"`
	Reason     string            `json:"reason"`
	Properties []json.RawMessage `json:"properties"`
}

// HandleSafetyStatusChanged translates _kiro/safety/statusChanged into a chat-scoped
// safety_status SSE. status="idle" is forwarded too, so the client can clear a stale banner.
//
// This handler surfaces a refusal, it does not GATE a write: the gate is a PreToolUse hook, so
// in enforce mode KAS intercepts the tool before execution and never issues the write request
// here. The block is not tool-scoped either — the notification's toolId is a tool NAME, not a
// per-call id, so it cannot be correlated to a rendered tool card.
func (t *Translator) HandleSafetyStatusChanged(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[v3SafetyStatusChanged](msg, "safety/statusChanged")
	if !ok {
		return
	}
	status := vibekit.SafetyStatus(p.Status)
	if _, known := knownSafetyStatuses[status]; !known {
		return
	}
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventSafetyStatus, chatID, vibekit.SafetyStatusPayload{
		Status:            status,
		Detail:            p.Detail,
		ToolID:            p.ToolID,
		BlockedProperties: p.BlockedProperties,
	}))
	// The SSE above is a transient banner that clears on idle. A blocked status is the
	// ENFORCE-mode terminal outcome — a change was refused — so it must outlive the banner and
	// is persisted, the way compaction persists `compacted` beside `compaction_started`.
	if status == vibekit.SafetyStatusBlocked {
		t.persistSafetyBlock(ctx, chatID, p)
	}
}

// persistSafetyBlock records an enforce-mode block as a permanent inline event message on the
// chat, which is the right scope because statusChanged names a tool by NAME rather than by call.
// Deliberately not gated on the turn's mute, unlike HandlePlan: a refused change is a fact about
// the SESSION rather than turn content, so it survives a prime.
func (t *Translator) persistSafetyBlock(ctx context.Context, chatID vibekit.ChatID, p v3SafetyStatusChanged) {
	evt := t.newEventMessage(vibekit.EventInfraSafetyBlocked, safetyBlockContent(p))
	err := t.chats.AppendMessage(durable.Context(ctx), chatID, &evt)
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("safety: append block event", "chat_id", chatID, "error", err)
	}
}

// safetyBlockContent composes the reason carried on a blocked event message, preferring the
// violated properties (the WHY) over the gate's detail string. The client prefixes its own
// "Infrastructure Safety blocked" label around this text.
func safetyBlockContent(p v3SafetyStatusChanged) string {
	if len(p.BlockedProperties) > 0 {
		return strings.Join(p.BlockedProperties, "; ")
	}
	return p.Detail
}

// HandleSafetyPropertiesChanged translates _kiro/safety/propertiesChanged into a chat-scoped
// safety_properties SSE. A foreign-session copy is skipped so a subagent's or a step's
// formalized properties are not attributed to the parent chat.
func (t *Translator) HandleSafetyPropertiesChanged(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[v3SafetyPropertiesChanged](msg, "safety/propertiesChanged")
	if !ok {
		return
	}
	if t.foreignSession(chatID, p.SessionID) {
		return
	}
	props := decodeSafetyProps(p.Properties)
	if len(props) == 0 {
		return
	}
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventSafetyProperties, chatID, vibekit.SafetyPropertiesPayload{
		Properties: props,
		Reason:     p.Reason,
	}))
}

// decodeSafetyProps normalizes the polymorphic properties[] — KAS sends either {index,
// description, enabled} objects or bare strings — into []vibekit.SafetyProperty. A bare string
// maps to that description with Enabled=true; an empty description is dropped.
func decodeSafetyProps(raw []json.RawMessage) []vibekit.SafetyProperty {
	out := make([]vibekit.SafetyProperty, 0, len(raw))
	for _, r := range raw {
		var obj struct {
			Description string `json:"description"`
			Index       int    `json:"index"`
			Enabled     bool   `json:"enabled"`
		}
		if err := json.Unmarshal(r, &obj); err == nil && obj.Description != "" {
			out = append(out, vibekit.SafetyProperty{
				Description: obj.Description,
				Index:       obj.Index,
				Enabled:     obj.Enabled,
			})
			continue
		}
		var s string
		if err := json.Unmarshal(r, &s); err == nil && s != "" {
			out = append(out, vibekit.SafetyProperty{Description: s, Enabled: true})
		}
	}
	return out
}
