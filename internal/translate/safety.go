package translate

// v3 (KAS) Infrastructure-Safety notification handlers:
// _kiro/safety/statusChanged and _kiro/safety/propertiesChanged.
//
// Defensive / forward-looking (mirrors code_references.go). The gate only
// installs — and these only fire — when both the client declares the
// infrastructureSafety capability AND an AWS governance flag is enabled
// (off by default on individual / Builder-ID accounts). So on a normal
// account these never fire and getProperties returns {properties:[]}.
// Authoring is entirely out-of-band: properties are formalized remotely;
// there is no client RPC to create/set/toggle one.
//
// Wire shapes:
//
//	statusChanged     { status, detail?, blockedProperties?, toolId? }   (no sessionId)
//	propertiesChanged { sessionId, properties, reason }
//	  properties[]    either { index, description, enabled } objects OR bare strings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// knownSafetyStatuses gates statusChanged translation to the documented
// GateStatus set, so an unrecognized/garbage status is dropped rather than
// surfaced as a mystery banner.
var knownSafetyStatuses = map[vibekit.SafetyStatus]struct{}{
	vibekit.SafetyStatusIdle:        {},
	vibekit.SafetyStatusFormalizing: {},
	vibekit.SafetyStatusEvaluating:  {},
	vibekit.SafetyStatusBlocked:     {},
	vibekit.SafetyStatusError:       {},
}

// v3SafetyStatusChanged is the _kiro/safety/statusChanged wire shape. The
// notification carries no sessionId (KAS routes it per-session via the outbound,
// not in params), so it is broadcast chat-scoped from the delivering bridge.
type v3SafetyStatusChanged struct {
	Status            string   `json:"status"`
	Detail            string   `json:"detail"`
	ToolID            string   `json:"toolId"`
	BlockedProperties []string `json:"blockedProperties"`
}

// v3SafetyPropertiesChanged is the _kiro/safety/propertiesChanged wire shape.
// properties[] is decoded tolerantly (object or bare string) by decodeSafetyProps.
type v3SafetyPropertiesChanged struct {
	SessionID  string            `json:"sessionId"`
	Reason     string            `json:"reason"`
	Properties []json.RawMessage `json:"properties"`
}

// HandleSafetyStatusChanged translates _kiro/safety/statusChanged into a
// safety_status SSE (chat-scoped). status="idle" is forwarded too — the
// client uses it to clear a stale banner.
//
// Enforcement model: the gate is a PreToolUse hook wrapping the tool
// executor. In enforce mode a blocked write/shell tool is intercepted
// before execution — KAS returns the block as the tool's own result and
// never issues the fs/write_text_file request to us. This handler's job
// is to surface the refusal, not to gate a write. A block is
// non-tool-scoped here: the notification's toolId is the tool NAME, not
// the per-call tool_call id, so it cannot be correlated to a specific
// rendered tool card.
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
	// The SSE above drives a transient status banner — live feedback while the
	// gate runs (formalizing/evaluating) that clears on idle/turn-end. A
	// blocked status is different: it is the ENFORCE-mode terminal outcome, a
	// permanent fact (a change was refused) that must outlive the banner. Persist
	// it as an inline event message so it survives reload and is visible across
	// devices (server is the source of truth), the same way compaction persists a
	// `compacted` event alongside its transient `compaction_started` SSE.
	if status == vibekit.SafetyStatusBlocked {
		t.persistSafetyBlock(ctx, chatID, p)
	}
}

// persistSafetyBlock records an enforce-mode Infrastructure-Safety block
// as a permanent inline event message on the chat. Chat-scoped:
// statusChanged carries no sessionId and its toolId is a tool name, so
// the refusal annotates the chat, not a specific tool card.
func (t *Translator) persistSafetyBlock(ctx context.Context, chatID vibekit.ChatID, p v3SafetyStatusChanged) {
	evt := t.newEventMessage(vibekit.EventInfraSafetyBlocked, safetyBlockContent(p))
	err := t.chats.AppendMessage(ctx, chatID, &evt)
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("safety: append block event", "chat_id", chatID, "error", err)
	}
}

// safetyBlockContent composes the human-readable reason carried on a blocked
// event message. It prefers the violated safety properties (the WHY the gate
// blocked); when the notification carries none it falls back to the gate's
// detail string. The client (messages-events.ts) prefixes an "Infrastructure
// Safety blocked" label around this text.
func safetyBlockContent(p v3SafetyStatusChanged) string {
	if len(p.BlockedProperties) > 0 {
		return strings.Join(p.BlockedProperties, "; ")
	}
	return p.Detail
}

// HandleSafetyPropertiesChanged translates _kiro/safety/propertiesChanged into a
// safety_properties SSE (chat-scoped). A subagent-session copy is skipped so a
// subagent's formalized properties aren't attributed to the parent chat (mirrors
// code_references' subagent guard).
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

// decodeSafetyProps normalizes the polymorphic properties[] — KAS sends either
// {index, description, enabled} objects (post-tool-use formalization) or bare
// strings (getProperties path) — into []vibekit.SafetyProperty. A bare string maps
// to a property with that description and Enabled=true; an empty description is
// dropped.
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
