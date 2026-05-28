// Checkpoint HTTP endpoints:
//
//	GET  /api/checkpoints/{chatID}/diff?from=&to=
//	GET  /api/checkpoints/{chatID}/restore-preview?tag=
//	GET  /api/checkpoints/{chatID}/conflicts
//	GET  /api/checkpoints/{chatID}/blob/{sha}
//
// `diff` returns per-file change stats between two tags so the
// client can render a "Changes since checkpoint N" summary.
//
// `restore-preview` returns the workspace-relative paths a restore
// would touch so the client can warn the user if any are currently
// open in the editor with unsaved changes.
//
// `conflicts` replays every recorded cross-chat conflict for the
// chat so the client can re-render conflict badges after a page
// reload (SSE only covers the live session).
//
// `blob/{sha}` returns the raw content of a blob the chat's event
// log references. Access is chat-scoped — the Manager rejects
// SHAs the chat doesn't own, preventing cross-chat blob probing.

package hub

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"vibekit/internal/api"
	"vibekit/internal/checkpoint"
)

// registerCheckpointRoutes is called from RegisterRoutes. Kept in a
// dedicated file so the route wiring and the handler live next to
// each other.
func (h *Hub) registerCheckpointRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/checkpoints/", h.handleCheckpoint)
}

// Supported sub-resources. New routes are added here rather than in
// separate Handlers so the /api/checkpoints/{chatID}/ prefix check
// stays in one place.
const (
	checkpointDiffPath           = "diff"
	checkpointRestorePreviewPath = "restore-preview"
	checkpointConflictsPath      = "conflicts"
	checkpointBlobPath           = "blob"
)

// handleCheckpoint routes the GET sub-resources. Anything else
// returns 404 so future sub-resources can be added without
// colliding silently.
func (h *Hub) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	if h.checkpoints == nil {
		api.NotFound(w, "checkpoints not available")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/checkpoints/")
	chatID, sub, ok := strings.Cut(rest, "/")
	if !ok || chatID == "" || !validChatID(api.ChatID(chatID)) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	cid := api.ChatID(chatID)
	// `blob` takes an extra path segment. Split the sub once more
	// to route blob/{sha} without polluting the Cut logic above.
	first, rest2, hasRest := strings.Cut(sub, "/")
	switch first {
	case checkpointDiffPath:
		h.handleCheckpointDiff(w, r, cid)
	case checkpointRestorePreviewPath:
		h.handleCheckpointRestorePreview(w, r, cid)
	case checkpointConflictsPath:
		h.handleCheckpointConflicts(w, r, cid)
	case checkpointBlobPath:
		if !hasRest || rest2 == "" {
			api.BadRequest(w, "blob sha required")
			return
		}
		h.handleCheckpointBlob(w, r, cid, rest2)
	default:
		api.NotFound(w, "unknown checkpoint sub-resource")
	}
}

// handleCheckpointDiff answers diff?from=&to=.
func (h *Hub) handleCheckpointDiff(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		api.BadRequest(w, "from and to are required")
		return
	}
	fromTag, err := checkpoint.ParseTag(from)
	if err != nil {
		api.BadRequest(w, "invalid from tag")
		return
	}
	toTag, err := checkpoint.ParseTag(to)
	if err != nil {
		api.BadRequest(w, "invalid to tag")
		return
	}
	files, err := h.checkpoints.Diff(r.Context(), chatID, fromTag, toTag)
	if err != nil {
		if errors.Is(err, checkpoint.ErrTagNotFound) {
			api.NotFound(w, "tag not found")
			return
		}
		api.InternalError(w, err)
		return
	}
	api.WriteJSON(w, api.CheckpointDiffResponse[checkpoint.FileChange]{Files: files})
}

// handleCheckpointRestorePreview answers restore-preview?tag=.
// Response shape: {"files": [...]}. The client compares this list
// against its current editor tab set to decide whether to surface
// an "unsaved edits in N files will be overwritten" warning before
// dispatching the restore_checkpoint command.
func (h *Hub) handleCheckpointRestorePreview(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		api.BadRequest(w, "tag is required")
		return
	}
	parsedTag, err := checkpoint.ParseTag(tag)
	if err != nil {
		api.BadRequest(w, "invalid tag format")
		return
	}
	files, err := h.checkpoints.RestorePreview(r.Context(), chatID, parsedTag)
	if err != nil {
		if errors.Is(err, checkpoint.ErrTagNotFound) {
			api.NotFound(w, "tag not found")
			return
		}
		api.InternalError(w, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	api.WriteJSON(w, api.CheckpointRestorePreviewResponse{Files: files})
}

// handleCheckpointConflicts replays every conflict event for a
// chat. Lets the client restore conflict badges after a reload
// when the SSE ring buffer has already wrapped past the events.
func (h *Hub) handleCheckpointConflicts(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	conflicts, err := h.checkpoints.Conflicts(r.Context(), chatID)
	if err != nil {
		api.InternalError(w, err)
		return
	}
	if conflicts == nil {
		// Return an empty slice (not null) so the client can
		// iterate without a nil check.
		api.WriteJSON(w, api.CheckpointConflictsResponse[checkpoint.ConflictPayload]{Conflicts: []checkpoint.ConflictPayload{}})
		return
	}
	api.WriteJSON(w, api.CheckpointConflictsResponse[checkpoint.ConflictPayload]{Conflicts: conflicts})
}

// handleCheckpointBlob returns the raw bytes of a chat-owned blob.
// The store validates that `sha` is hex and that the chat's event
// log references the blob. Content-Type is plain text because our
// checkpoints only cover text files — the fs_read handler
// already refuses binary content upstream.
func (h *Hub) handleCheckpointBlob(w http.ResponseWriter, r *http.Request, chatID api.ChatID, sha string) {
	data, err := h.checkpoints.ReadBlob(r.Context(), chatID, sha)
	if err != nil {
		// Don't leak whether the chat has the blob — same
		// response for "bad sha" and "not owned". The Manager
		// returns ErrBlobNotFound for both, so this collapses
		// naturally.
		api.NotFound(w, "blob not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// Content-Type locks the browser to plain rendering; the SHA
	// is validated as hex and chat-scoped upstream. Blob content
	// is whatever the agent wrote, which is user/agent authored
	// code — treating it as untrusted for XSS makes no sense
	// because the browser never renders it as HTML.
	if _, err := w.Write(data); err != nil { // #nosec G705 -- content-typed text/plain; user-owned blob
		slog.Debug("checkpoint: blob write failed", "error", err)
	}
}

// advanceCheckpointTurn bumps the turn counter for chatID, recording
// the current message count as the turn-start watermark. No-op when
// the hub has no checkpoint store wired (e.g. no configDir provided).
// Kept on Hub as a one-liner so cmdPrompt doesn't accrue extra
// control flow for the nil guard.
func (h *Hub) advanceCheckpointTurn(ctx context.Context, chatID api.ChatID) {
	if h.checkpoints == nil {
		return
	}
	h.checkpoints.AdvanceTurn(ctx, chatID, h.currentMessageCount(ctx, chatID))
}
