package translate

// v3 (KAS) _kiro/governance/state handler.
//
// KAS pushes the account/workspace feature-flag policy as a notification on
// every session/new and session/load (newSession / hydrateSessionForLoad in the
// acp-server bundle), and re-pushes it during a prompt when
// governance.refreshIfChanged() detects a change. It is an A→C notification
// (outbound.extNotification) vibekit just receives — there is no request/reply.
//
// Wire shape (verified against the KAS 2.12 acp-server bundle + a live probe):
//
//	{ "sessionId", "isEnterprise", "features": { … 7 bools … }, "disabledReason"? }
//
//	features = {
//	  mcpEnabled, webToolsEnabled, usageAnalytics, contentCollection,
//	  promptLogging, codeReferenceTracker, autonomousAgents
//	}
//
// Actual values on an individual / Builder-ID login (verified live): isEnterprise
// false; mcp/webTools/autonomousAgents/contentCollection true; usageAnalytics/
// promptLogging/codeReferenceTracker false; no disabledReason. Infrastructure-
// Safety (infraSafety*) is NOT here — it is a separate isFeatureEnabled channel.
//
// The state is account-GLOBAL (identical across a connection's sessions), so the
// SSE is broadcast with an empty chat id (every client, including one on Settings
// with no active chat, receives it) and the latest is cached hub-side
// (deps.SetGovernance) so GET /api/governance can serve it on a fresh page load.

import (
	"context"
	"encoding/json"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// v3GovernanceState is the _kiro/governance/state notification payload.
type v3GovernanceState struct {
	SessionID      string               `json:"sessionId"`
	DisabledReason string               `json:"disabledReason"`
	Features       v3GovernanceFeatures `json:"features"`
	IsEnterprise   bool                 `json:"isEnterprise"`
}

// v3GovernanceFeatures mirrors KAS's GovernanceFeatures (getFeatures()).
type v3GovernanceFeatures struct {
	MCPEnabled           bool `json:"mcpEnabled"`
	WebToolsEnabled      bool `json:"webToolsEnabled"`
	UsageAnalytics       bool `json:"usageAnalytics"`
	ContentCollection    bool `json:"contentCollection"`
	PromptLogging        bool `json:"promptLogging"`
	CodeReferenceTracker bool `json:"codeReferenceTracker"`
	AutonomousAgents     bool `json:"autonomousAgents"`
}

// payload converts the wire shape into the domain SSE/REST payload, stamping
// Known=true (this only runs on a real notification). SessionID is intentionally
// dropped — governance is account-global, not session-scoped.
func (w v3GovernanceState) payload() vibekit.GovernanceStatePayload {
	return vibekit.GovernanceStatePayload{
		Known:          true,
		IsEnterprise:   w.IsEnterprise,
		DisabledReason: w.DisabledReason,
		Features: vibekit.GovernanceFeatures{
			MCPEnabled:           w.Features.MCPEnabled,
			WebToolsEnabled:      w.Features.WebToolsEnabled,
			UsageAnalytics:       w.Features.UsageAnalytics,
			ContentCollection:    w.Features.ContentCollection,
			PromptLogging:        w.Features.PromptLogging,
			CodeReferenceTracker: w.Features.CodeReferenceTracker,
			AutonomousAgents:     w.Features.AutonomousAgents,
		},
	}
}

// DecodeGovernanceState decodes a raw _kiro/governance/state params object into
// the domain payload. Exported so the hub can reuse it for the copy the utility
// bridge receives (whose notifications don't flow through this dispatcher) —
// keeping one wire→domain conversion. Returns false on empty/invalid params.
func DecodeGovernanceState(raw json.RawMessage) (vibekit.GovernanceStatePayload, bool) {
	if len(raw) == 0 {
		return vibekit.GovernanceStatePayload{}, false
	}
	var w v3GovernanceState
	if err := json.Unmarshal(raw, &w); err != nil {
		return vibekit.GovernanceStatePayload{}, false
	}
	return w.payload(), true
}

// HandleGovernanceState translates _kiro/governance/state into a
// governance_state SSE and caches the latest state hub-side. The SSE is
// broadcast with an empty chat id because the policy is account-global (a
// client on Settings with no active chat must still receive it). A
// subagent-session copy is skipped (KAS may re-emit per session; the parent
// copy carries the identical account-global flags) — the same dedup guard
// safety.go / code_references.go use.
func (t *Translator) HandleGovernanceState(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[v3GovernanceState](msg, "governance/state")
	if !ok {
		return
	}
	if t.foreignSession(chatID, p.SessionID) {
		return
	}
	payload := p.payload()
	t.governance.SetGovernance(payload)
	t.streaming.Broadcast(ctx, vibekit.NewEvent(vibekit.EventGovernanceState, "", payload))
}
