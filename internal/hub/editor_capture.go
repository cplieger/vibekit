// Editor-save checkpoint capture. A successful save from the built-in
// editor (PUT /api/file) is folded into the checkpoint timeline of the
// chat that owns the file's lineage, so restores, per-file undo, and
// diffs treat the manual edit as a known, undoable state instead of
// invisible out-of-band drift. Shell writes and other out-of-band
// mutations remain deliberately unsupported — the built-in editor is
// the one manual-change surface vibekit vouches for.

package hub

import (
	"context"
	"log/slog"

	"github.com/cplieger/pathinside"
	"github.com/cplieger/vibekit/internal/workspace"
)

// CaptureEditorSave records a built-in-editor save as a checkpoint
// snapshot in the owning chat's timeline. Wired into the filehandler's
// write observer by the composition root; runs synchronously just
// BEFORE the editor write lands (same ordering as the agent write
// path) so Snapshot reads the pre-save disk content as beforeSHA — the
// undo target — and records the incoming content as afterSHA, keeping
// the cross-chat index's view of disk current so the next agent write
// in another chat doesn't raise a spurious drift conflict for a save
// the user made on purpose.
//
// Routing: the owner is the chat whose agent most recently wrote the
// file (cross-chat index). A file no agent ever wrote has no
// checkpoint lineage — no restore will ever touch it — so there is
// nothing to capture. Saves outside the work tree (e.g. the /config
// mount) are ignored: checkpoints cover the workspace only.
//
// No turn-tag stamping: the save is not part of an agent turn, so it
// must not claim the turn-canonical Restore anchor on a prompt
// message (see stampTurnCheckpointTag). Consequence: a save landing
// mid-turn before the agent's first write consumes the turn-canonical
// "N" slot and that one turn offers per-file undo but no whole-turn
// restore anchor — rare, and strictly better than mislabeling.
//
// Failures are logged, never surfaced: the checkpoint exists to serve
// the user, not to gate their save.
//
// The boundary test is pathinside.RelEscapes on the workspace-relative
// name, which is separator-precise, so a file whose first segment merely
// BEGINS with two dots ("..extras/movie.mkv") is captured rather than
// silently skipped. RelPath returns a forward-slash-normalised value and
// RelEscapes cleans before testing, so the separator question is settled
// inside the library on either platform. The rel=="." rejection stays
// here on purpose: pathinside's containment predicates count the root as
// inside, and this site must EXCLUDE it — a save whose relative name is
// the work tree itself has no file lineage to snapshot.
func (h *Hub) CaptureEditorSave(ctx context.Context, absPath string, content []byte) {
	if h.checkpoints == nil {
		return
	}
	rel, err := workspace.RelPath(h.lifecycle.workDir, absPath)
	if err != nil || rel == "." || pathinside.RelEscapes(rel) {
		return // outside the work tree (or the root itself): not checkpoint territory
	}
	owner, ok := h.checkpoints.OwnerOf(ctx, rel)
	if !ok {
		return // no chat tracks this file; restores never touch it
	}
	tag, err := h.checkpoints.Snapshot(ctx, owner, rel, content, h.currentMessageCount(ctx, owner))
	if err != nil {
		slog.Warn("checkpoint: editor-save capture failed",
			"chat_id", owner, "path", rel, "error", err)
		return
	}
	slog.Debug("checkpoint: editor save captured",
		"chat_id", owner, "path", rel, "tag", tag)
}
