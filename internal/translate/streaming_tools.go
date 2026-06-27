package translate

// Tool call streaming handlers: tool_call, tool_call_update, ext session update.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
)

// HandleToolCall adds a tool call to the current assistant message
// buffer and broadcasts it.
func (t *Translator) HandleToolCall(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil || IsSubagentNoiseTitle(tc.Title) {
		return
	}
	if tc.Kind == api.ToolKindHook && !t.deps.IsHookStatusEnabled() {
		return
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	t.ensureTurnStarted(ctx, chatID, buf, false)
	var diffs []api.ToolDiff
	for _, c := range tc.Content {
		if c.Type == ContentTypeDiff && c.Path != "" {
			diffs = append(diffs, api.ToolDiff{
				Path: t.relPath(c.Path), OldText: c.OldText, NewText: c.NewText,
			})
		}
	}
	call := api.ToolCall{
		ID:           tc.ToolCallID,
		Title:        tc.Title,
		Kind:         tc.Kind,
		Status:       tc.Status,
		Input:        tc.RawInput,
		SubSessionID: subSessionID,
		Locations:    tc.Locations,
		Diffs:        diffs,
		Ts:           time.Now().UnixMilli(),
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
	blockIndex := buf.AppendToolUseBlock(call.ID)
	buf.RecordToolStart(tc.ToolCallID)
	buf.WritePartial(ctx)
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
}

// HandleExtSessionUpdate handles the extension channel for subagent
// tool-call chunks.
func (t *Translator) HandleExtSessionUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind       string `json:"sessionUpdate"`
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Kind2      string `json:"kind"`
		} `json:"update"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.Update.Kind != ExtUpdateToolCallChunk {
		return
	}
	subSessionID := t.deriveSubSession(chatID, p.SessionID)
	buf := t.deps.BufferStore().GetOrInit(chatID)
	t.ensureTurnStarted(ctx, chatID, buf, false)
	call := api.ToolCall{
		ID:           p.Update.ToolCallID,
		Title:        p.Update.Title,
		Kind:         api.ToolKind(p.Update.Kind2),
		Status:       api.ToolPending,
		SubSessionID: subSessionID,
		Ts:           time.Now().UnixMilli(),
	}
	buf.ToolCalls = append(buf.ToolCalls, call)
	if buf.ToolCallIndex == nil {
		buf.ToolCallIndex = make(map[string]int)
	}
	buf.ToolCallIndex[p.Update.ToolCallID] = len(buf.ToolCalls) - 1
	buf.RecordToolStart(p.Update.ToolCallID)
	buf.WritePartial(ctx)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventToolCall, chatID, api.ToolCallPayload{MessageID: buf.MessageID, ToolCall: call}))
}

// relPath strips the workspace root prefix from an absolute path.
func (t *Translator) relPath(abs string) string {
	workDir := t.deps.WorkDir()
	if workDir == "" {
		return abs
	}
	clean := filepath.Clean(abs)
	root := filepath.Clean(workDir)
	rel, err := filepath.Rel(root, clean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

// ensureTurnStarted initializes the buffer for a new turn if not already started.
// openPartial controls whether the partial recovery file is opened (content chunks
// need it; tool-call-only turns do not).
func (t *Translator) ensureTurnStarted(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer, openPartial bool) {
	if buf.Started {
		return
	}
	buf.Started = true
	buf.MessageID = t.newMsgID()
	if openPartial {
		t.deps.OpenPartialFile(ctx, chatID, buf)
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageCreated, chatID,
		api.Message{ID: buf.MessageID, Role: api.RoleAssistant, Ts: time.Now().UnixMilli()}))
}
