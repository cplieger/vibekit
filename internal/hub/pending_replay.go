package hub

// Pending-change replay and flush infrastructure.
//
// These helpers are called from multiple hub files (hub.go shutdown,
// bridge_fs.go trust-clear, command.go cancel, command_chat_ops.go
// delete) and are infrastructure rather than command handlers.
// Extracted from command_pending.go to keep that file focused on
// HTTP dispatch.

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
)

// flushPendingForChat rejects every outstanding pending op for a chat
// and broadcasts the resolution + cleared events. Called fire-and-forget
// from shutdown, cancel, and chat-delete paths.
func (h *Hub) flushPendingForChat(ctx context.Context, chatID api.ChatID, reason api.ClearReason) {
	snaps := h.perm.pending.RejectAllForChat(chatID)
	for _, snap := range snaps {
		h.Broadcast(ctx, api.NewEvent(api.EventPendingChangeResolved, chatID, api.PendingChangeResolvedPayload{
			ToolCallID: snap.ToolCallID,
			Action:     api.PendingActionReject,
			Path:       snap.Path,
		}))
	}
	if len(snaps) > 0 {
		h.Broadcast(ctx, api.NewEvent(api.EventPendingChangesCleared, chatID, api.PendingChangesClearedPayload{Reason: reason}))
	}
}

// listChatIDsWithPending returns the set of chat IDs that currently
// have at least one pending op. Delegates to the pending store's
// ChatIDs method which is O(1) bounded by pending ops.
func (h *Hub) listChatIDsWithPending() []api.ChatID {
	return h.perm.pending.ChatIDs()
}

// handlePendingChange serves GET /api/pending-changes/{tool_call_id}.
// Returns the staged op's old + new content so the editor's
// pending-diff virtual path can render a full diff pane even when the
// SSE payload truncated the content. 404 if the op isn't pending.
//
// Path parsing is the same pattern chat.Store uses for its
// sub-resources: trim the prefix, reject anything with further slashes.
// No PUT / DELETE here; state transitions go through the command
// envelope so they pass the request_id idempotency cache.
func (h *Hub) handlePendingChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	id := r.URL.Path[len("/api/pending-changes/"):]
	if id == "" {
		api.BadRequest(w, "tool_call_id is required")
		return
	}
	snap, ok := h.perm.pending.Get(id)
	if !ok {
		api.NotFound(w, "no such pending change")
		return
	}
	api.WriteJSON(w, snap)
}
