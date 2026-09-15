package translate

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// hookUpdateBlock is the kind=="hook_update" sub-block of a session_info_update, one
// frame per hook execution. Name is untrusted workspace file content.
type hookUpdateBlock struct {
	HookID      string `json:"hookId"`
	OperationID string `json:"operationId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ActionType  string `json:"actionType"`
}

// hookStatusCompleted is the wire status of a hook execution KAS reports as successful.
const hookStatusCompleted = "completed"

// handleHookUpdate appends a `Hook fired` card to the chat's open turn, the way
// HandleToolCall appends a real tool call, gated on hooks.showStatus.
func (t *Translator) handleHookUpdate(ctx context.Context, chatID vibekit.ChatID, h *hookUpdateBlock, attr FrameAttribution) {
	if !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(attr.Step))
	t.ensureTurnStarted(ctx, chatID, buf)
	call := hookToolCall(h, time.Now().UnixMilli())
	buf.AppendToolCall(&call)
	blockIndex, version := buf.AppendToolUseBlock(call.ID, "")
	frame := vibekit.NewEvent(vibekit.EventToolCall, chatID,
		vibekit.ToolCallPayload{MessageID: buf.MessageID, ToolCall: call, BlockIndex: blockIndex})
	frame.Subject = vibekit.NewSubjectStamp(string(subject.KindLiveTurn), string(chatID), version)
	t.bus.Broadcast(ctx, frame)
}

// hookToolCall is the settled tool call one hook_update frame becomes: no input, no
// output, no content.
func hookToolCall(h *hookUpdateBlock, ts int64) vibekit.ToolCall {
	return vibekit.ToolCall{
		ID:     "hook-" + h.OperationID,
		Title:  "Hook fired: " + displayText(h.Name),
		Kind:   vibekit.ToolKindHook,
		Status: hookToolStatus(h.Status),
		Ts:     ts,
	}
}

// hookToolStatus maps the frame's status onto the card's terminal state.
//
// No outcome text is rendered: KAS 2.21.4's hook emitter hardcodes actionState:"Success"
// and sends no output, so a hook that exited non-zero still arrives "completed"
// (kirodotdev/Kiro#11369). The state is mapped from the wire rather than fixed so a
// build that starts reporting failures paints red with no code change.
func hookToolStatus(s string) vibekit.ToolStatus {
	if s == hookStatusCompleted {
		return vibekit.ToolCompleted
	}
	return vibekit.ToolFailed
}
