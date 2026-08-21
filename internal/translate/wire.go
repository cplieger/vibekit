// Package translate handles ACP → domain event translation.
//
// The Translator receives raw ACP notifications from kiro-cli bridges
// and converts them into domain events (SSE broadcasts, chat-store
// mutations). Runtime remains the coordinator; this package owns the
// protocol-specific decode + dispatch logic.
package translate

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
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
	// TerminalID is set on a type:"terminal" block, ACP's way of saying "this
	// tool call's output is that terminal's stream". It is the link that makes
	// the tool CARD the terminal's rendering surface.
	TerminalID string `json:"terminalId"`
	Content    struct {
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
	Kiro ACPKiroBlock `json:"kiro"`
}

// ACPKiroBlock is the `kiro` object inside an `_meta`. A NAMED type rather than
// an anonymous struct so it can carry the wire census below; every field access
// site is unchanged.
type ACPKiroBlock struct {
	// Refusal is present only on the agent_message_chunk carrying a
	// model-refusal explanation (kiro-cli 2.13+, modelStopReason
	// "content_filtered"). KAS calls it a progressive-enhancement
	// marker: plain clients render the text, capable clients key off
	// it for a distinct refusal affordance. The turn then ends with
	// core stopReason "refusal".
	Refusal *ACPRefusalMeta `json:"refusal"`
	// Checkpoint is KAS's snapshot mapping for a file-writing tool
	// call. Probed 2026-08-02 (kiro-cli 2.16.0): it arrives ONLY on
	// the tool_call_update whose status is "completed" — the initial
	// tool_call and every in_progress/pending update carry none, which
	// is why the value is folded in on update rather than set once.
	//
	// A sibling _meta.kiro.preview on that same frame repeats these
	// URIs AND carries originalContent/modifiedContent in full. That
	// is deliberately NOT decoded: persisting whole file bodies per
	// tool call would bloat every chat file, and the snapshot URIs
	// address the same bytes on demand.
	// DisclosedContext identifies the skill or steering document a
	// `disclose_context` call loaded. KAS persists it (its own comment: "the
	// resolved skill/steering document. Persisted so the loaded item's type
	// and source file survive a session reload"), which is what makes it
	// worth decoding rather than deriving from the tool title.
	//
	// This is how a skill's body actually reaches the model on this platform:
	// the AGENT activates it, under a standing instruction to check for a
	// match before answering, so a client-side trigger matcher would be a
	// second and worse guesser competing with that judgement. Decoding this
	// is what makes the activation visible instead of a generic tool card.
	DisclosedContext *ACPDisclosedContext `json:"disclosedContext,omitempty"`
	// PolicyDenial is KAS's structured reason for a tool call the Cedar
	// policy refused, persisted so the explanation survives a reload. Without
	// it a refusal is indistinguishable from a broken command, and the two
	// want opposite reactions: edit the rule, or debug the tool.
	PolicyDenial   *ACPPolicyDenial   `json:"policyDenial,omitempty"`
	Checkpoint     *ACPCheckpointMeta `json:"checkpoint"`
	Kind           string             `json:"kind"`
	AgentSubtaskID string             `json:"agentSubtaskId"`
	// MessageID and Timestamp are KAS's own identity for the message
	// record a frame belongs to. Measured on the v3 wire (probe 23,
	// kiro-cli 2.16.0): present on user_message_chunk (a bare uuid —
	// vibekit's OWN prompt messageId when vibekit sent one),
	// agent_message_chunk (`<uuid>-say`), tool_call (`<id>-call`) and
	// tool_call_update (`<id>-result`). Timestamp is RFC3339 with
	// milliseconds.
	//
	// The replay projection depends on both: without MessageID it
	// fabricates ids, so the same session projects differently on every
	// load and nothing can address a message; without Timestamp every
	// replayed turn is stamped with the load's wall clock and a resumed
	// transcript claims all its history happened just now.
	MessageID string `json:"messageId"`
	Timestamp string `json:"timestamp"`
	// Notification tags a row KAS wrote onto a chat's transcript on
	// something else's behalf. kind "workflow-progress" is a workflow
	// step's progress persisted onto the LAUNCHING chat, which arrives as
	// a user_message_chunk carrying JSON — see isWorkflowProgress.
	Notification struct {
		Kind string `json:"kind"`
	} `json:"notification"`
	// Workflow is present on every frame of a workflow STEP's session
	// (probe 17). It is what makes a step frame self-describing: the frame
	// arrives on the launching chat's connection with a session id that is
	// neither the chat's nor a subagent's, and this block is the only thing
	// on the frame that says which.
	//
	// Note the nesting: this is `params.update._meta.kiro.workflow`, not
	// `params._meta` — `params` carries only `sessionId` and `update`.
	Workflow *ACPWorkflowMeta `json:"workflow"`
	HookAsk  json.RawMessage  `json:"hookAsk,omitempty"`
}

// acpKiroBlockShadow strips the UnmarshalJSON method so the real decode can run
// without recursing into it. The standard shadow-type trick; the alias must have
// the same layout, which a defined type over the same struct does.
type acpKiroBlockShadow ACPKiroBlock

// UnmarshalJSON decodes the block and, on the way past, reports any member KAS
// sent that this type does not read.
//
// The census runs HERE rather than at the handlers because encoding/json hands
// this method exactly the `_meta.kiro` object's bytes: probing a few hundred bytes
// per frame instead of rescanning a whole tool_call_update, which can carry a
// multi-megabyte diff. That difference is the reason this is a method at all.
//
// The decode's own error is returned verbatim and the census cannot contribute
// one. Every call site drops the frame on a decode error, so a probe that could
// fail would stop tool cards from rendering — a diagnostic must never be able to
// cost a turn.
func (b *ACPKiroBlock) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*acpKiroBlockShadow)(b)); err != nil {
		return err
	}
	// `preview` repeats the checkpoint URIs and adds originalContent and
	// modifiedContent in full; it is skipped on purpose (see Checkpoint above), so
	// reporting it would be noise on the first frame of every file write.
	censusMeta("_meta.kiro", data, reflect.TypeFor[acpKiroBlockShadow](), "preview")
	return nil
}

// ACPDisclosedContext is _meta.kiro.disclosedContext on a disclose_context call.
type ACPDisclosedContext struct {
	// Type is "skill" or "steering".
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
	URI         string `json:"uri"`
}

// ACPPolicyDenial is _meta.kiro.policyDenial on a tool call Cedar refused.
//
// MatchedRule is the rule that produced the verdict, which is the part worth
// surfacing: a denial that names its rule is one click from the rule, and the
// user owns the policy. `effect` on the outer object is always "deny", so it is
// not decoded; the inner rule's effect can be deny or ask.
type ACPPolicyDenial struct {
	MatchedRule *ACPPolicyRule `json:"matchedRule"`
	Capability  string         `json:"capability"`
	Resource    string         `json:"resource"`
	Scope       string         `json:"scope"`
	Source      string         `json:"source"`
}

// ACPPolicyRule is the matched rule inside a policy denial.
type ACPPolicyRule struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// ACPWorkflowMeta is the _meta.kiro.workflow block on a step session's frames.
//
// NodePath is KAS's own instance-unique address for a node — a repeat's second
// iteration is `[wf…, loop, iter-1, step]` — which is why it, rather than
// NodeID, is what a per-step attribution key is built from: two iterations of
// one step share a NodeID and must not share a block.
type ACPWorkflowMeta struct {
	WorkflowID string   `json:"workflowId"`
	NodeID     string   `json:"nodeId"`
	Type       string   `json:"type"`
	NodePath   []string `json:"nodePath"`
}

// SubtaskID is the per-block attribution key for a step's content.
//
// A step's prose otherwise MERGES into the launching chat's own paragraph, and
// that reproduces exactly the context confusion workflows exist to avoid. The
// mechanism: the chunk handlers append through `Buffer.AppendTextDelta(text,
// subtask)`, which extends the trailing block only when kind AND subtask match —
// and a step's text frame carries an EMPTY `agentSubtaskId` (KAS sets that only
// on tool frames), so empty matched empty and the step's words landed inside the
// parent agent's block.
//
// Reusing `agent_subtask_id` rather than adding a parallel channel is deliberate:
// the client already groups same-subtask blocks into a collapsible
// delegated-work block, so a step renders as delegated work with no client
// change at all. The `wf:` prefix keeps the two id spaces from ever colliding —
// KAS's subtask ids are uuids.
func (w *ACPWorkflowMeta) SubtaskID() string {
	if w == nil || w.WorkflowID == "" {
		return ""
	}
	if len(w.NodePath) > 0 {
		return "wf:" + strings.Join(w.NodePath, "/")
	}
	return "wf:" + w.WorkflowID + "/" + w.NodeID
}

// ACPCheckpointMeta is the _meta.kiro.checkpoint object on a completed
// file-writing tool_call_update. Every field is independently optional —
// see vibekit.ToolCheckpoint for the create-has-no-pre-image case.
type ACPCheckpointMeta struct {
	Original string `json:"original"`
	Modified string `json:"modified"`
	Local    string `json:"local"`
}

// ACPRefusalMeta is the _meta.kiro.refusal block on a refusal explanation
// chunk. Explanation duplicates the chunk text (KAS falls back to a canned
// message when absent), so only Category / RecommendedModel flow into the
// domain vibekit.RefusalInfo.
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
	Kind       vibekit.ToolKind          `json:"kind"`
	Status     vibekit.ToolStatus        `json:"status"`
	RawInput   json.RawMessage           `json:"rawInput"`
	Locations  []vibekit.ToolLocation    `json:"locations"`
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
	Kind       vibekit.ToolKind          `json:"kind"`
	Status     vibekit.ToolStatus        `json:"status"`
	Locations  []vibekit.ToolLocation    `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
}

// ACPPlanWire is the wire shape for plan session updates.
type ACPPlanWire struct {
	Entries []vibekit.PlanEntry `json:"entries"`
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

// ACPSessionUpdateBase extracts the two discriminators every session/update
// dispatch needs: the sessionUpdate kind, and whether the frame is a REPLAY
// of stored history rather than something happening now.
//
// KAS replays a session's whole transcript as ordinary session/update
// notifications in response to `session/load` (bounded only when the caller
// asks, via `_meta.kiro.replayLimit`), tagging each replayed frame
// `_meta.kiro.replay: true`. Note the nesting: the flag is on the **update**
// object, not on `params` — reading it off params yields false for every
// frame, which looks exactly like a wire that never sets it.
//
// Live frames leave it absent. Catalog frames that arrive during a load
// (`available_commands_update`, `config_option_update`) are deliberately NOT
// tagged: they carry the session's CURRENT state, not its history, so they
// must keep reaching the live handlers.
type ACPSessionUpdateBase struct {
	Kind vibekit.ACPUpdateKind `json:"sessionUpdate"`
	Meta struct {
		Kiro struct {
			// Workflow is present on a workflow STEP's frames and is the
			// discriminator the dispatcher classifies on. Decoded here, at the
			// same shallow depth as Replay, because both answer "which door does
			// this frame go through" before any per-kind decode happens.
			Workflow *ACPWorkflowMeta `json:"workflow"`
			Replay   bool             `json:"replay"`
		} `json:"kiro"`
	} `json:"_meta"`
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

// ContentTypeTerminal is the ACP content-block type that names the terminal
// running an execute tool call. Its terminalId is how the tool card finds its
// own output stream, which is what makes the transcript the rendering surface
// for agent commands.
const ContentTypeTerminal = "terminal"
