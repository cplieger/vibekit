package translate

// v3 (KAS) Infrastructure-Safety notification handlers:
// _kiro/safety/statusChanged and _kiro/safety/propertiesChanged.
//
// DEFENSIVE / FORWARD-LOOKING (mirrors code_references.go). KAS's
// Infrastructure-Safety gate evaluates infrastructure-as-code tool calls
// (Terraform / CloudFormation / CDK / Docker / k8s / …) against safety
// properties "formalized" by a remote MCP endpoint (runtime.us-east-1.kiro.dev),
// and emits these notifications as it runs. But the gate only installs — and so
// these only fire — when BOTH:
//   - the client declares the infrastructureSafety capability in initialize
//     (vibekit does; see internal/bridge/bridge.go), AND
//   - an AWS governance flag (infraSafetyMonitor|infraSafetyEnforce) is enabled
//     (modelConfigProvider.isFeatureEnabled — off by default on individual /
//     Builder-ID accounts, verified absent from _kiro/governance/state on a live
//     probe).
//
// So on a normal account these never fire and getProperties returns
// {properties:[]}. These handlers exist so the state surfaces IF an enterprise
// account has the gate enabled — the same basis on which code_references was
// wired. Authoring is entirely out-of-band: properties are formalized remotely;
// there is no client RPC to create/set/toggle one (verified: 0 setter methods
// on the acp surface). This is Kiro's infra guardrail, distinct from vibekit's
// autopilot-backed supervised mode (vibekit-acp.md).
//
// Wire shapes (verified against the KAS 2.12 acp-server bundle):
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
// safety_status SSE (chat-scoped). status="idle" is forwarded too — the client
// uses it to clear a stale banner.
//
// Enforcement model (verified against the KAS 2.12 acp-server bundle): the gate
// is a PreToolUse hook wrapping the tool executor. In ENFORCE mode a blocked
// write/shell tool is INTERCEPTED before execution — KAS returns the block as
// the tool's own result ("The tool was NOT executed") and never calls the inner
// executor, so it never issues the fs/write_text_file A→C request to us. Vibekit
// therefore cannot circumvent an enforced block: its fs write handler
// (agent.respondFSWrite) only ever writes in response to a KAS request, and a
// blocked tool produces no request. This handler's job is to SURFACE the
// refusal, not to gate a write (there is no write to gate). A block is
// non-tool-scoped here: the notification's toolId is the tool NAME (fs_write /
// str_replace / …), not the per-call tool_call id we round-trip, so it cannot
// be correlated to a specific rendered tool card.
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

// persistSafetyBlock records an ENFORCE-mode Infrastructure-Safety block as a
// permanent inline event message on the chat. Chat-scoped: statusChanged
// carries no sessionId (KAS routes it per-session via the outbound, not in
// params) and its toolId is a tool name, so the refusal annotates the chat, not
// a specific tool card. Persist failures are logged, not fatal — a missed
// breadcrumb never blocks the stream.
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
