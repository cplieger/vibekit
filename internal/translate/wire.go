// Package translate handles ACP → domain event translation.
//
// The Translator receives raw ACP notifications from kiro-cli bridges
// and converts them into domain events (SSE broadcasts, chat-store
// mutations). Hub remains the coordinator; this package owns the
// protocol-specific decode + dispatch logic.
package translate

import (
	"encoding/json"

	"github.com/cplieger/vibekit/internal/api"
)

// Shared ACP wire-format decode types for the translate layer. These
// replace per-handler anonymous structs so the ACP protocol surface is
// explicit, greppable, and maintained in one place. When kiro-cli adds
// a field, it lands here once rather than in N handler-local structs.

// ACPChunkWire is the wire shape for agent_message_chunk and
// agent_thought_chunk session updates. On v3 (KAS) a nested subagent's
// chunks ride the parent session id and carry _meta.kiro.agentSubtaskId
// identifying which agent-subtask tool call they belong to.
type ACPChunkWire struct {
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Meta ACPKiroMeta `json:"_meta"`
}

// ACPToolCallContentBlock is one element in a tool_call or
// tool_call_update's content array.
type ACPToolCallContentBlock struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// ACPKiroMeta is the top-level `_meta.kiro` block carried on v3 (KAS)
// tool_call / tool_call_update session updates. When Kind=="agent-subtask"
// the tool call is a subagent card (the model invoked invoke_sub_agent);
// AgentSubtaskID is the stable id that links the card to its nested
// agent_message_chunk / agent_thought_chunk deltas (which carry the same
// id under their own _meta.kiro).
//
// HookAsk is present (non-empty) only on the synthetic tool call KAS emits
// to surface a pre-tool-use hook's ask-permission gate: KAS sends a
// kind:"other" tool_call/tool_call_update tagged
// _meta.kiro.hookAsk={kind:"pre-tool-use",toolName,reason[,decision]} —
// NOT a ToolKind "hook" (v3's zToolKind has no "hook"). Its presence is
// the signal HandleToolCall uses to suppress hook cards when the
// hooks.showStatus setting is off; the contents are opaque here.
type ACPKiroMeta struct {
	Kiro struct {
		// Refusal is present only on the agent_message_chunk carrying a
		// model-refusal explanation (kiro-cli 2.13+, modelStopReason
		// "content_filtered"). KAS calls it a progressive-enhancement
		// marker: plain clients render the text, capable clients key off
		// it for a distinct refusal affordance. The turn then ends with
		// core stopReason "refusal".
		Refusal        *ACPRefusalMeta `json:"refusal"`
		Kind           string          `json:"kind"`
		AgentSubtaskID string          `json:"agentSubtaskId"`
		HookAsk        json.RawMessage `json:"hookAsk,omitempty"`
	} `json:"kiro"`
}

// ACPRefusalMeta is the _meta.kiro.refusal block on a refusal explanation
// chunk. Explanation duplicates the chunk text (KAS falls back to a canned
// message when absent), so only Category / RecommendedModel flow into the
// domain api.RefusalInfo.
type ACPRefusalMeta struct {
	Category         string `json:"category"`
	Explanation      string `json:"explanation"`
	RecommendedModel string `json:"recommendedModel"`
}

// ACPToolCallWire is the wire shape for tool_call session updates.
type ACPToolCallWire struct {
	Meta       ACPKiroMeta               `json:"_meta"`
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       api.ToolKind              `json:"kind"`
	Status     api.ToolStatus            `json:"status"`
	RawInput   json.RawMessage           `json:"rawInput"`
	Locations  []api.ToolLocation        `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
}

// ACPToolCallUpdateWire is the wire shape for tool_call_update session
// updates. KAS's zToolCallUpdate also carries optional title/kind (a
// mid-flight card refinement) and rawOutput; we decode title/kind so the
// card can be relabelled, but leave rawOutput undecoded — the tool's
// textual output already arrives through the `content` blocks below and
// the domain ToolCall has no structured-output field.
type ACPToolCallUpdateWire struct {
	Meta       ACPKiroMeta               `json:"_meta"`
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       api.ToolKind              `json:"kind"`
	Status     api.ToolStatus            `json:"status"`
	Locations  []api.ToolLocation        `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
}

// ACPPlanWire is the wire shape for plan session updates.
type ACPPlanWire struct {
	Entries []api.PlanEntry `json:"entries"`
}

// ACPModeUpdateWire is the wire shape for the current_mode_update
// session/update sub-kind. KAS keys the new mode on `currentModeId`
// (the bundle's zCurrentModeUpdate object), NOT `modeId` — `modeId` is
// the field name on the outbound session/set_mode REQUEST (command/mode.go),
// a different message. Reading the wrong key here left ModeID empty, so
// HandleModeUpdate never persisted agent-initiated mode changes.
type ACPModeUpdateWire struct {
	ModeID string `json:"currentModeId"`
}

// ACPSessionUpdateEnvelope is the outer envelope for session/update
// notifications.
type ACPSessionUpdateEnvelope struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ACPSessionUpdateBase extracts the sessionUpdate kind discriminator.
type ACPSessionUpdateBase struct {
	Kind api.ACPUpdateKind `json:"sessionUpdate"`
}

// JSON field name constants — the wire protocol uses these strings
// in many places; constants keep them in one place and silence
// goconst warnings.

// ContentTypeContent is the ACP content-block type discriminator value "content".
// Distinct from jsonFieldContent which is the JSON field *name* "content".
const ContentTypeContent = "content"

// ContentTypeDiff is the ACP content-block type for file-change diffs
// in tool_call and tool_call_update payloads.
const ContentTypeDiff = "diff"
