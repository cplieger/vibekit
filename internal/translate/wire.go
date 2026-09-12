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

// Shared ACP wire-format decode types for the translate layer, so a field kiro-cli
// adds lands here once rather than in N handler-local anonymous structs.

// ACPChunkWire is the wire shape for agent_message_chunk and agent_thought_chunk
// session updates. A nested subagent's chunks ride the parent session id and carry
// _meta.kiro.agentSubtaskId naming the tool call they belong to.
type ACPChunkWire struct {
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Meta ACPKiroMeta `json:"_meta"`
}

// ACPToolCallContentBlock is one element in a tool_call or tool_call_update's content
// array.
//
// On a type:"diff" block, OldText/NewText are whole-file contents for KAS's edit tools
// rather than the changed fragment (some tools send a hunk pair instead), so anything
// deriving a line count from them must diff the two sides.
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

// ACPKiroMeta is the top-level `_meta` carrying a `kiro` block. Kind=="agent-subtask"
// marks a subagent card, and AgentSubtaskID links it to the nested chunk deltas that
// carry the same id.
//
// Kiro.HookAsk is non-empty only on the synthetic kind:"other" tool call KAS emits for
// a pre-tool-use hook's ask gate — there is no ToolKind "hook" — so its presence, not
// the kind, is what suppresses hook cards when hooks.showStatus is off.
type ACPKiroMeta struct {
	Kiro ACPKiroBlock `json:"kiro"`
}

// ACPKiroBlock is the `kiro` object inside an `_meta`. A NAMED type rather than an
// anonymous struct so it can carry the wire census below.
type ACPKiroBlock struct {
	// Refusal is present only on the agent_message_chunk carrying a model-refusal
	// explanation. The turn then ends with core stopReason "refusal".
	Refusal *ACPRefusalMeta `json:"refusal"`
	// DisclosedContext identifies the skill or steering document a `disclose_context`
	// call loaded. KAS persists it, so it survives a session reload, which is what
	// makes it worth decoding rather than deriving from the tool title. Decoding it is
	// also what renders an activation as itself rather than a generic tool card.
	DisclosedContext *ACPDisclosedContext `json:"disclosedContext,omitempty"`
	// PolicyDenial is KAS's structured reason for a tool call the Cedar policy
	// refused, persisted so the explanation survives a reload. Without it a refusal is
	// indistinguishable from a broken command, and the two want opposite reactions:
	// edit the rule, or debug the tool.
	PolicyDenial   *ACPPolicyDenial   `json:"policyDenial,omitempty"`
	Checkpoint     *ACPCheckpointMeta `json:"checkpoint"`
	Kind           string             `json:"kind"`
	AgentSubtaskID string             `json:"agentSubtaskId"`
	// ToolID is KAS's machine name for a tool call (`execute_bash`, `user_input`).
	// Unlike Title it is not model- or locale-composed, which is what makes it safe to
	// key the internal-tool suppression on.
	ToolID string `json:"toolId"`
	// MessageID and Timestamp are KAS's own identity for the message record a frame
	// belongs to; Timestamp is RFC3339 with milliseconds.
	//
	// The replay projection depends on both: without MessageID it fabricates ids, so
	// one session projects differently on every load and nothing can address a message;
	// without Timestamp a resumed transcript claims all its history happened just now.
	MessageID string `json:"messageId"`
	Timestamp string `json:"timestamp"`
	// Source marks the whole steering CHANNEL, so a workflow-progress row, a step
	// notice and a steering-boundary row all carry it, not just a reader's steer. It
	// is stamped HERE by the replay builder rather than on the update object.
	Source string `json:"source"`
	// Notification tags a row KAS wrote onto a chat's transcript on something else's
	// behalf. kind "workflow-progress" is a step's progress persisted onto the
	// LAUNCHING chat, arriving as a user_message_chunk carrying JSON.
	Notification struct {
		Kind string `json:"kind"`
	} `json:"notification"`
	// Workflow is present on every frame of a workflow STEP's session, and is the only
	// thing on the frame that says so: a step's frames arrive on the launching chat's
	// connection under a session id that is neither the chat's nor a subagent's. Note
	// the nesting — `params.update._meta.kiro.workflow`, not `params._meta`.
	Workflow *ACPWorkflowMeta `json:"workflow"`
	HookAsk  json.RawMessage  `json:"hookAsk,omitempty"`
	// AgentInitiated marks a turn the ENGINE started. It rides CONTENT frames and never
	// the bracket, which is why acknowledgement has to be provisional; and a
	// zero-content auto-wake sends none, so the empty-turn gate cannot rest on it.
	AgentInitiated bool `json:"agentInitiated"`
}

// acpKiroBlockShadow strips the UnmarshalJSON method so the real decode can run
// without recursing into it. The alias must keep the same layout, which a defined type
// over the same struct does.
type acpKiroBlockShadow ACPKiroBlock

// UnmarshalJSON decodes the block and, on the way past, reports any member KAS sent
// that this type does not read.
//
// The census runs here rather than at the handlers because encoding/json hands this
// method exactly the `_meta.kiro` bytes, so the probe stays cheap even when the
// surrounding frame carries a multi-megabyte diff. It contributes no error of its own:
// every call site drops the frame on a decode error.
func (b *ACPKiroBlock) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*acpKiroBlockShadow)(b)); err != nil {
		return err
	}
	// `preview` is skipped on purpose (it repeats the checkpoint URIs and adds both
	// file bodies in full), so reporting it would be noise on every file write.
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

// ACPPolicyDenial is _meta.kiro.policyDenial on a tool call Cedar refused. MatchedRule
// is the part worth surfacing: a denial that names its rule is one click from the rule
// the user owns. `effect` on the outer object is always "deny" and is not decoded; the
// inner rule's effect can be deny or ask.
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
// NodePath, not NodeID, is what a per-step attribution key is built from: two
// iterations of one step share a NodeID and must not share a block. Its `iter-<n>`
// segment is the FRAME spelling only — inspect's state tree names the same container
// `<repeatId>#<n>`, so a client joining the two translates the tree into this spelling.
// WorkflowName, Iteration and BranchID are decoded because the run card states them.
type ACPWorkflowMeta struct {
	WorkflowID   string   `json:"workflowId"`
	WorkflowName string   `json:"workflowName"`
	NodeID       string   `json:"nodeId"`
	Type         string   `json:"type"`
	BranchID     string   `json:"branchId"`
	NodePath     []string `json:"nodePath"`
	Iteration    int      `json:"iteration"`
}

// SubtaskID is the per-block attribution key for a step's content. Without one, a
// step's prose merges into the launching agent's own paragraph: the append extends a
// block whenever kind and subtask match, and a step's text frame carries an empty
// agentSubtaskId, so empty matched empty.
//
// It reuses agent_subtask_id rather than adding a channel, so a step renders through
// the grouping the client already has. The format lives in vibekit.StepSubtaskID,
// beside the parse that reads it back; this method only supplies the two segments.
func (w *ACPWorkflowMeta) SubtaskID() string {
	if w == nil || w.WorkflowID == "" {
		return ""
	}
	return vibekit.StepSubtaskID(w.WorkflowID, runNodePath(w))
}

// ACPCheckpointMeta is the _meta.kiro.checkpoint object on a file-writing
// tool_call_update. It arrives only on the update whose status is "completed", which is
// why it is merged per field rather than set once, and every field is independently
// optional — see vibekit.ToolCheckpoint for the create-has-no-pre-image case.
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

// ACPConsentMeta is the _meta.kiro.consent object on a session/request_permission, sent
// ONLY when persisting a rule for this command would NOT work.
//
// PersistableConsent's pointer is LOAD-BEARING: the polarity is absent-means-yes, so a
// plain bool would decode a wire that sends no consent object as "not persistable" and
// suppress the Always-allow row on every request; nil means KAS said nothing. The reason
// string is decoded and dropped at the seam: long, and it names a file the user never
// hand-edits.
type ACPConsentMeta struct {
	PersistableConsent       *bool  `json:"persistableConsent"`
	PersistableConsentReason string `json:"persistableConsentReason"`
}

// ACPPermissionMeta is the `_meta` on a session/request_permission. NAMED rather than
// inline in the handler's decode struct because this frame is the one human APPROVAL
// surface on the wire, and its `_meta` multiplexes three unrelated concerns an inline
// struct would hide from everything but today's handler.
type ACPPermissionMeta struct {
	Kiro ACPPermissionKiroBlock `json:"kiro"`
}

// ACPPermissionKiroBlock is the `kiro` object inside a permission request's
// `_meta`.
// Field order is fieldalignment's, not reading order: Consent leads because it
// holds the only pointer in the block.
type ACPPermissionKiroBlock struct {
	// Consent is 2.19.1's persistability verdict. Absent for every 2.19.0 and
	// earlier frame, and absent on 2.19.1 whenever a rule WOULD match.
	Consent ACPConsentMeta `json:"consent"`
	// MCPTool carries the identity KAS verified for an MCP-backed tool.
	MCPTool ACPMCPToolWire `json:"mcpTool"`
	// Type marks a TURN APPROVAL ("turn_approval"), which KAS raises as an ordinary
	// session/request_permission — so this is the only thing distinguishing "may I run
	// this tool" from "may I apply this turn's writes".
	Type string `json:"type"`
	// Files is the turn approval's staged file list. Paths arrive ABSOLUTE and the
	// action id arrives as `toolCallId`; both are renamed on the way out.
	Files []ACPApprovalFile `json:"files"`
}

// ACPMCPToolWire is the verified MCP identity attached to a permission request.
type ACPMCPToolWire struct {
	Identity struct {
		ServerName string `json:"serverName"`
		ToolName   string `json:"toolName"`
	} `json:"identity"`
}

// ACPApprovalFile is one entry of a turn approval's `files` array.
type ACPApprovalFile struct {
	Path        string `json:"path"`
	SnapshotURI string `json:"snapshotUri"`
	ToolCallID  string `json:"toolCallId"`
}

// ACPToolCallWire is the wire shape for tool_call session updates.
type ACPToolCallWire struct {
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       vibekit.ToolKind          `json:"kind"`
	Status     vibekit.ToolStatus        `json:"status"`
	RawInput   json.RawMessage           `json:"rawInput"`
	Locations  []vibekit.ToolLocation    `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
	// Meta trails because ACPKiroBlock ends in a bool, and fieldalignment counts
	// LEADING pointer bytes. Field order carries no other meaning here.
	Meta ACPKiroMeta `json:"_meta"`
}

// ACPToolCallUpdateWire is the wire shape for tool_call_update session updates.
// title/kind are decoded so a card can be relabelled mid-flight. rawOutput stays
// opaque except for the workflow link and the narrow text fallbacks below.
type ACPToolCallUpdateWire struct {
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       vibekit.ToolKind          `json:"kind"`
	Status     vibekit.ToolStatus        `json:"status"`
	RawOutput  json.RawMessage           `json:"rawOutput"`
	Locations  []vibekit.ToolLocation    `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
	// Meta trails because ACPKiroBlock ends in a bool, and fieldalignment counts
	// LEADING pointer bytes. Field order carries no other meaning here.
	Meta ACPKiroMeta `json:"_meta"`
}

// ACPRawOutput is the object shape this client reads out of a tool call's
// `rawOutput`, which KAS types as `unknown` and fills with whatever the tool returned.
//
// `run_workflow` is the reason for WorkflowID: the field on its terminal update is
// the only structural link from the invocation to the run it started. Error is a
// failure fallback. Message is also a failure fallback, except when a content block
// is provably a stringified copy of the same object. Object decoding stays narrow so
// a structured success payload cannot become a general output channel.
//
// Updated is a fourth purpose-named field rather than a widening of that rule: it is
// `update_workflow`'s own verdict, a fact about the world that no status can carry,
// where the banned `success` merely restates actionState. A POINTER because absent
// means the tool made no claim, which reads as TAKEN — the same rule
// internal/agent/run_host.go `stepStatusRefusal` applies to the sibling RPC channel.
type ACPRawOutput struct {
	Updated    *bool  `json:"updated"`
	WorkflowID string `json:"workflowId"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

// rawOutputWorkflowID extracts the workflow id a `run_workflow` invocation
// reports, or "" when this update's rawOutput is absent, not an object, or
// carries no id.
func rawOutputWorkflowID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out ACPRawOutput
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out.WorkflowID
}

// rawOutputUpdate reports a workflow-update tool's own verdict: whether the update
// was taken, and whether the tool stated a verdict at all.
//
// `present` is false for an absent, non-object or malformed rawOutput AND for one
// carrying no `updated` key, so no other tool can be touched — measured, `updated`
// inside a rawOutput OBJECT occurs on exactly one tool's output in the whole bundle
// (the task tool's `updated` is text inside a `message` string, which decodes to nil
// here). Absent therefore means TAKEN, matching internal/agent/run_host.go
// `stepStatusRefusal` on the RPC channel: an unstated field making a working verb
// report a refusal is the worse direction.
func rawOutputUpdate(raw json.RawMessage) (updated, present bool) {
	if len(raw) == 0 {
		return false, false
	}
	var out ACPRawOutput
	if json.Unmarshal(raw, &out) != nil || out.Updated == nil {
		return false, false
	}
	return *out.Updated, true
}

// rawOutputString extracts rawOutput only when it is a bare JSON string. KAS uses
// that shape when an edit's diff content block is suppressed, so it is the one
// non-content output channel that is safe on any status.
func rawOutputString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// stringifiedRawOutputMessage returns message only when content encodes the same
// multi-field object as rawOutput; a different content block remains canonical.
func stringifiedRawOutputMessage(raw json.RawMessage, content string) string {
	var out ACPRawOutput
	var rawObject map[string]any
	var contentObject map[string]any
	if json.Unmarshal(raw, &out) != nil || json.Unmarshal(raw, &rawObject) != nil || len(rawObject) < 2 {
		return ""
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &contentObject) != nil {
		return ""
	}
	if !reflect.DeepEqual(rawObject, contentObject) {
		return ""
	}
	return strings.TrimSpace(out.Message)
}

// rawOutputFailureText extracts the reason a failed tool call reports, or "" when
// rawOutput is absent, malformed, neither a string nor an object, or carries no text.
func rawOutputFailureText(raw json.RawMessage) string {
	if text := rawOutputString(raw); text != "" {
		return text
	}
	var out ACPRawOutput
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	if out.Error != "" {
		return strings.TrimSpace(out.Error)
	}
	return strings.TrimSpace(out.Message)
}

// ACPPlanWire is the wire shape for plan session updates.
type ACPPlanWire struct {
	Entries []vibekit.PlanEntry `json:"entries"`
}

// ACPModeUpdateWire is the wire shape for the current_mode_update session/update
// sub-kind. The new mode is keyed on `currentModeId`, NOT `modeId` — that is the field
// name on the outbound set_mode REQUEST, a different message — and reading the wrong one
// leaves ModeID empty, so no agent-initiated mode change is persisted.
type ACPModeUpdateWire struct {
	ModeID string `json:"currentModeId"`
}

// ACPSessionUpdateEnvelope is the outer envelope for session/update
// notifications.
type ACPSessionUpdateEnvelope struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ACPSessionUpdateBase extracts the two discriminators every session/update dispatch
// needs: the sessionUpdate kind, and whether the frame is a replay of stored history.
//
// A session/load replays the whole transcript as ordinary session/update notifications
// tagged `_meta.kiro.replay: true`. Note the nesting: the flag is on the UPDATE object,
// so reading it off params yields false for every frame. Absent means live, and also
// means "describes current state rather than history" — a rule rather than a list, since
// the untagged set grows over releases, so do not replace it with "drop during a load".
type ACPSessionUpdateBase struct {
	Kind vibekit.ACPUpdateKind `json:"sessionUpdate"`
	Meta struct {
		Kiro struct {
			// Workflow is the discriminator the dispatcher classifies a step on.
			Workflow *ACPWorkflowMeta `json:"workflow"`
			Replay   bool             `json:"replay"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// ContentTypeContent is the ACP content-block type discriminator value "content".
// Distinct from jsonFieldContent, which is the JSON field *name* "content".
const ContentTypeContent = "content"

// ContentTypeDiff is the ACP content-block type for file-change diffs.
const ContentTypeDiff = "diff"

// ContentTypeTerminal is the ACP content-block type naming the terminal running an
// execute tool call. Its terminalId is how the tool card finds its own output stream.
const ContentTypeTerminal = "terminal"
