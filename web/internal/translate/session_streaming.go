package translate

// Streaming content handlers for session/update sub-types.

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"vibekit/internal/api"
)

// maxBufferBytes caps the per-turn content buffer at 32 MiB. Prevents
// OOM from a pathological agent turn (e.g. cat of a large binary).
// kiro-cli has its own output limits, so this is defense-in-depth.
const maxBufferBytes = 32 << 20

// HandleAssistantChunk streams a text delta to clients and accumulates
// it for later persistence. Reasoning chunks (isReasoning=true) flow
// into buf.Reasoning; regular content chunks flow into buf.Content.
// The IsReasoning flag is forwarded on the SSE so the client routes
// each delta to the correct bubble (reasoning details vs content).
func (t *Translator) HandleAssistantChunk(ctx context.Context, chatID api.ChatID, raw json.RawMessage, isReasoning bool) {
	var chunk ACPChunkWire
	if json.Unmarshal(raw, &chunk) != nil || chunk.Content.Type != api.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	if !buf.Started {
		buf.Started = true
		buf.MessageID = t.deps.NewMessageID()
		t.deps.OpenPartialFile(ctx, chatID, buf)
		t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageCreated, chatID,
			api.Message{ID: buf.MessageID, Role: api.RoleAssistant, Ts: time.Now().UnixMilli()}))
	}
	totalLen := buf.Content.Len() + buf.Reasoning.Len()
	if totalLen+len(chunk.Content.Text) > maxBufferBytes {
		// Defense-in-depth: cap the buffer so a pathological turn
		// (e.g. agent cats a huge file) cannot OOM the container.
		// Silently drop further content; the turn will still end
		// normally via turn_ended and the truncated message is
		// persisted.
		return
	}
	if isReasoning {
		buf.Reasoning.WriteString(chunk.Content.Text)
	} else {
		buf.Content.WriteString(chunk.Content.Text)
	}
	buf.WritePartial(ctx)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageChunk, chatID,
		api.MessageChunkPayload{MessageID: buf.MessageID, Delta: chunk.Content.Text, IsReasoning: isReasoning}))
}

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
	if !buf.Started {
		buf.Started = true
		buf.MessageID = t.deps.NewMessageID()
		t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageCreated, chatID,
			api.Message{ID: buf.MessageID, Role: api.RoleAssistant, Ts: time.Now().UnixMilli()}))
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
	buf.RecordToolStart(tc.ToolCallID)
	buf.WritePartial(ctx)
	if len(diffs) > 0 {
		isNew := tc.Kind == api.ToolKindEdit && tc.Status == api.ToolPending
		buf.TrackFileChanges(diffs, isNew)
		turn := len(buf.ToolCalls)
		t.deps.LineTracker().RecordFromDiffs(chatID, diffs, turn, string(tc.Kind))
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventToolCall, chatID, api.ToolCallPayload{MessageID: buf.MessageID, ToolCall: call}))
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
	var outputDelta strings.Builder
	var diffs []api.ToolDiff
	for _, item := range tu.Content {
		if item.Type == ContentTypeContent && item.Content.Text != "" {
			outputDelta.WriteString(api.SanitizeOutput(item.Content.Text))
			outputDelta.WriteByte('\n')
		} else if item.Type == ContentTypeDiff && item.Path != "" {
			diffs = append(diffs, api.ToolDiff{
				Path: t.relPath(item.Path), OldText: item.OldText, NewText: item.NewText,
			})
		}
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	idx, ok := buf.ToolCallIndex[tu.ToolCallID]
	if !ok || idx >= len(buf.ToolCalls) {
		return
	}
	if tu.Status != "" {
		buf.ToolCalls[idx].Status = tu.Status
		if tu.Status == api.ToolCompleted || tu.Status == api.ToolFailed {
			buf.ToolCalls[idx].DurationMs = buf.ComputeDuration(tu.ToolCallID)
			t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID,
				api.WorkingLabelPayload{Label: api.WorkingLabelThinking}))
		}
	}
	if outputDelta.Len() > 0 {
		buf.ToolCalls[idx].Output += outputDelta.String()
	}
	if len(tu.Locations) > 0 {
		buf.ToolCalls[idx].Locations = tu.Locations
	}
	if len(diffs) > 0 {
		buf.ToolCalls[idx].Diffs = append(buf.ToolCalls[idx].Diffs, diffs...)
		buf.TrackFileChanges(diffs, false)
		turn := len(buf.ToolCalls)
		t.deps.LineTracker().RecordFromDiffs(chatID, diffs, turn, string(buf.ToolCalls[idx].Kind))
	}
	if buf.ToolCalls[idx].SubSessionID == "" && subSessionID != "" {
		buf.ToolCalls[idx].SubSessionID = subSessionID
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventToolCallUpdate, chatID, api.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: buf.ToolCalls[idx]}))
}

// HandlePlan persists a plan message directly.
func (t *Translator) HandlePlan(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p ACPPlanWire
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	msg := api.Message{
		ID:   t.deps.NewMessageID(),
		Role: api.RoleAssistant,
		Ts:   time.Now().UnixMilli(),
		Plan: p.Entries,
	}
	if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &msg); err != nil {
		slog.Error("persist plan", "chat_id", chatID, "error", err)
	}
	if ctx.Err() != nil {
		return
	}
	allDone := true
	for _, e := range p.Entries {
		if e.Status != api.PlanCompleted {
			allDone = false
			break
		}
	}
	var plan []api.PlanEntry
	if !allDone {
		plan = p.Entries
	}
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CurrentPlan = plan
		return true
	}); err != nil {
		slog.Error("persist plan update", "chat_id", chatID, "error", err)
	}
}

// HandleModeUpdate persists the agent's new mode and broadcasts mode_changed.
func (t *Translator) HandleModeUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p ACPModeUpdateWire
	if json.Unmarshal(raw, &p) != nil || p.ModeID == "" {
		return
	}
	changed := false
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex || c.CurrentModeID == p.ModeID {
			return false
		}
		c.CurrentModeID = p.ModeID
		changed = true
		return true
	}); err != nil {
		slog.Error("mode update persist", "chat_id", chatID, "error", err)
	}
	if changed {
		t.deps.Broadcast(ctx, api.NewEvent(api.EventModeChanged, chatID, api.ModeChangedPayload{ModeID: p.ModeID}))
	}
}

// HandleSteeringInclusion processes steering_inclusion events.
func (t *Translator) HandleSteeringInclusion(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p ACPSteeringWire
	if json.Unmarshal(raw, &p) != nil || len(p.Documents) == 0 {
		return
	}
	names := make([]string, 0, len(p.Documents))
	for _, d := range p.Documents {
		name := d.Name
		if name == "" {
			name = d.Path
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventSteeringLoaded, chatID, api.SteeringLoadedPayload{Documents: names}))
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
	if !buf.Started {
		buf.Started = true
		buf.MessageID = t.deps.NewMessageID()
		t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageCreated, chatID, api.Message{
			ID: buf.MessageID, Role: api.RoleAssistant,
			Ts: time.Now().UnixMilli(),
		}))
	}
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
