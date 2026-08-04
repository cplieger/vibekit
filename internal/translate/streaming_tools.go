package translate

// Tool call streaming handlers: tool_call, tool_call_update, ext session update.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/pathinside"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
)

// HandleToolCall adds a tool call to the current assistant message
// buffer and broadcasts it. On v3 (KAS) a subagent is an ordinary tool
// call tagged _meta.kiro.kind=="agent-subtask"; AgentSubtaskID is threaded
// onto the domain ToolCall so the client can render a subagent card and
// nest the subagent's chunks (which carry the same id) under it.
func (t *Translator) HandleToolCall(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil {
		return
	}
	// Hook status suppression. On v3 (KAS) a pre-tool-use hook's
	// ask-permission gate arrives as a kind:"other" tool call tagged
	// _meta.kiro.hookAsk (there is no ToolKind "hook" in v3's zToolKind).
	// When the kiro-cli hooks.showStatus setting is off, drop the hook-ask
	// card. Suppressing the initial tool_call also drops its follow-up
	// tool_call_update: HandleToolCallUpdate early-returns when the id was
	// never buffered.
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.deps.IsHookStatusEnabled() {
		return
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	t.ensureTurnStarted(ctx, chatID, buf)
	// A workflow STEP's tool frames carry KAS's own agentSubtaskId (or none),
	// while the step's TEXT is keyed by its nodePath — so without this override
	// one step's work fragments across two boxes. Same rule as the chunk path:
	// the step's workflow identity wins.
	subtask := tc.Meta.Kiro.AgentSubtaskID
	if wf := tc.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
		subtask = wf
	}
	var diffs []api.ToolDiff
	for _, c := range tc.Content {
		if c.Type == ContentTypeDiff && c.Path != "" {
			diffs = append(diffs, api.ToolDiff{
				Path: t.relPath(c.Path), OldText: c.OldText, NewText: c.NewText,
			})
		}
	}
	call := api.ToolCall{
		ID:             tc.ToolCallID,
		Title:          tc.Title,
		Kind:           tc.Kind,
		Status:         tc.Status,
		Input:          tc.RawInput,
		SubSessionID:   subSessionID,
		AgentSubtaskID: subtask,
		Locations:      tc.Locations,
		Diffs:          diffs,
		Ts:             time.Now().UnixMilli(),
	}
	buf.ToolCalls = append(buf.ToolCalls, call)
	if buf.ToolCallIndex == nil {
		buf.ToolCallIndex = make(map[string]int)
	}
	buf.ToolCallIndex[tc.ToolCallID] = len(buf.ToolCalls) - 1
	// Anchor the tool in the chronological block array. Always a new
	// block — back-to-back tool calls each get their own tool_use
	// block (the next text chunk after this will also start a new
	// block since the trailing block is now tool_use, not text).
	blockIndex := buf.AppendToolUseBlock(call.ID, subtask)
	buf.RecordToolStart(tc.ToolCallID)
	if len(diffs) > 0 {
		isNew := tc.Kind == api.ToolKindEdit && tc.Status == api.ToolPending
		buf.TrackFileChanges(diffs, isNew)
		turn := len(buf.ToolCalls)
		t.deps.LineTracker().RecordFromDiffs(chatID, diffs, turn, string(tc.Kind))
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventToolCall, chatID,
		api.ToolCallPayload{MessageID: buf.MessageID, ToolCall: call, BlockIndex: blockIndex}))
	t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID,
		api.WorkingLabelPayload{Label: api.WorkingLabelForKind(tc.Kind, tc.Title)}))
}

// HandleToolCallUpdate mutates an in-flight tool call's status and
// appends any new output chunks.
func (t *Translator) HandleToolCallUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil {
		return
	}
	output, diffs := t.parseToolUpdateContent(tu.Content)
	buf := t.deps.BufferStore().GetOrInit(chatID)
	idx, ok := buf.ToolCallIndex[tu.ToolCallID]
	if !ok || idx >= len(buf.ToolCalls) {
		return
	}
	t.applyToolCallUpdate(ctx, chatID, buf, idx, &tu, output, diffs, subSessionID)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventToolCallUpdate, chatID, api.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: buf.ToolCalls[idx]}))
}

// parseToolUpdateContent extracts the sanitized output delta and any
// file diffs from a tool_call_update's content blocks. Diff paths are
// normalized to workspace-relative form.
func (t *Translator) parseToolUpdateContent(items []ACPToolCallContentBlock) (output string, diffs []api.ToolDiff) {
	var outputDelta strings.Builder
	for _, item := range items {
		// Only "content" (text) and "diff" blocks are consumed here. The
		// execute tool's type:"terminal" content blocks are intentionally
		// ignored: that output already streams to the client through the
		// terminal/* SSE surface (terminal_output), so decoding it here
		// would double-render it on the tool card.
		if item.Type == ContentTypeContent && item.Content.Text != "" {
			outputDelta.WriteString(api.SanitizeOutput(item.Content.Text))
			outputDelta.WriteByte('\n')
		} else if item.Type == ContentTypeDiff && item.Path != "" {
			diffs = append(diffs, api.ToolDiff{
				Path: t.relPath(item.Path), OldText: item.OldText, NewText: item.NewText,
			})
		}
	}
	return outputDelta.String(), diffs
}

// applyToolCallUpdate folds a parsed tool_call_update into the buffered
// tool call at idx: status (emitting a working label on terminal
// status), appended output, replaced locations, appended diffs (with
// line tracking), and a first-seen subsession id.
func (t *Translator) applyToolCallUpdate(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer, idx int, tu *ACPToolCallUpdateWire, output string, diffs []api.ToolDiff, subSessionID string) {
	tc := &buf.ToolCalls[idx]
	// A mid-flight update may refine the card's title/kind (KAS sends them
	// nullish on tool_call_update); apply only when present so an update
	// that omits them doesn't wipe the values set on the initial tool_call.
	if tu.Title != "" {
		tc.Title = tu.Title
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	if tu.Status != "" {
		tc.Status = tu.Status
		if tu.Status == api.ToolCompleted || tu.Status == api.ToolFailed {
			tc.DurationMs = buf.ComputeDuration(tu.ToolCallID)
			t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID,
				api.WorkingLabelPayload{Label: api.WorkingLabelThinking}))
		}
	}
	if output != "" {
		tc.Output += output
	}
	if len(tu.Locations) > 0 {
		tc.Locations = tu.Locations
	}
	if len(diffs) > 0 {
		tc.Diffs = append(tc.Diffs, diffs...)
		buf.TrackFileChanges(diffs, false)
		turn := len(buf.ToolCalls)
		t.deps.LineTracker().RecordFromDiffs(chatID, diffs, turn, string(tc.Kind))
	}
	if tc.SubSessionID == "" && subSessionID != "" {
		tc.SubSessionID = subSessionID
	}
	if tc.AgentSubtaskID == "" {
		// Late adoption mirrors the create path, workflow identity first.
		if wf := tu.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
			tc.AgentSubtaskID = wf
		} else if tu.Meta.Kiro.AgentSubtaskID != "" {
			tc.AgentSubtaskID = tu.Meta.Kiro.AgentSubtaskID
		}
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
}

// mergeCheckpoint folds a tool_call_update's _meta.kiro.checkpoint into the
// buffered tool call, field by field so that a frame omitting a key cannot
// erase one an earlier frame supplied.
//
// Per-field rather than wholesale because the key set genuinely varies
// between frames for the same tool call: KAS sends {modified, local} for a
// file creation and adds `original` only when there was a pre-image, so
// replacing the struct would be correct today and lossy the moment a second
// frame arrives with a narrower set.
func mergeCheckpoint(tc *api.ToolCall, in *ACPCheckpointMeta) {
	if in == nil || (in.Original == "" && in.Modified == "" && in.Local == "") {
		return
	}
	if tc.Checkpoint == nil {
		tc.Checkpoint = &api.ToolCheckpoint{}
	}
	if in.Original != "" {
		tc.Checkpoint.Original = in.Original
	}
	if in.Modified != "" {
		tc.Checkpoint.Modified = in.Modified
	}
	if in.Local != "" {
		tc.Checkpoint.Local = in.Local
	}
}

// relPath strips the workspace root prefix from an absolute path. A path that
// is not under the workspace is returned unchanged.
//
// The escape test is pathinside.RelEscapes on the rel this function already
// computes for its result, not a leading-".." string prefix: the
// separator-precise rule keeps a file under a workspace directory whose name
// merely BEGINS with two dots ("..drafts/main.go") relative, where the string
// test leaked the absolute path to the client.
func (t *Translator) relPath(abs string) string {
	workDir := t.deps.WorkDir()
	if workDir == "" {
		return abs
	}
	clean := filepath.Clean(abs)
	root := filepath.Clean(workDir)
	rel, err := filepath.Rel(root, clean)
	if err != nil || pathinside.RelEscapes(rel) {
		return abs
	}
	return filepath.ToSlash(rel)
}

// ensureTurnStarted initializes the buffer for a new turn if not already
// started: assigns the message id and broadcasts message_created.
//
// It owns no crash durability. It used to open the .partial sidecar lazily on
// the first content chunk; that sidecar is deleted, and a turn interrupted
// mid-flight is now rebuilt from KAS's own log by the session/load replay
// projection — measured to hold each sub-message as it completes, so a
// tool-first turn is covered from its first tool call rather than from its
// first text.
func (t *Translator) ensureTurnStarted(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer) {
	if buf.Started {
		return
	}
	buf.Started = true
	buf.MessageID = t.newMsgID()
	t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageCreated, chatID,
		api.Message{ID: buf.MessageID, Role: api.RoleAssistant, Ts: time.Now().UnixMilli()}))
}
