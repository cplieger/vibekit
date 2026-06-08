// File-system write request handlers for kiro-cli ACP bridges.
//
// Spec: https://agentclientprotocol.com/protocol/file-system

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/workspace"
)

// respondFSWrite handles fs/write_text_file. Request params:
//
//	{ sessionId, path, content: "..." }
//
// Response: empty object on success. Caps content at fsWriteCap. Creates
// parent directories (0o755) if missing.
//
// Supervised mode: if the chat has SupervisedMode=true, the write is
// staged via h.perm.pending instead of applying immediately. The goroutine
// blocks on the op's resume channel until the user resolves it via
// resolve_pending_change or resolve_all_pending_changes, then either
// applies the write (accept) or returns an error to the agent (reject).
// The checkpoint snapshot is taken ONLY when the write applies, so
// rejected writes leave no checkpoint trace.
func (h *Hub) respondFSWrite(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		h.respondFSError(ctx, chatID, msg, fmt.Errorf("parse params: %w", err))
		return
	}
	if p.Path == "" {
		h.respondFSError(ctx, chatID, msg, errors.New("path is required"))
		return
	}
	if len(p.Content) > fsWriteCap {
		h.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsWriteCap))
		return
	}
	abs, err := h.resolveInsideWorkDir(p.Path)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}

	// Supervised gate. The chat's SupervisedMode is authoritative; we
	// re-read it per-call so toggles take effect on the next staged op
	// without restarting the bridge. Per-turn trust overrides the mode:
	// once the user clicks "Trust remaining" in the Supervised pill,
	// subsequent writes in the same turn bypass staging until the turn
	// ends (clearPerTurnTrust fires on turn_ended, cancel, and the
	// usual cleanup paths).
	if h.chatInSupervisedMode(ctx, chatID) && !h.perm.supervised.HasTrust(chatID) {
		accepted, override, err := h.stageFSWrite(ctx, chatID, msg, abs, p.Path, p.Content)
		if err != nil {
			h.respondFSError(ctx, chatID, msg, err)
			return
		}
		if !accepted {
			h.respondFSError(ctx, chatID, msg, errRejectedByUser)
			return
		}
		if override != "" {
			// User-edited merged content overrides the agent's
			// proposal. Only kicks in when resolve_pending_change_partial
			// was used; plain accept leaves override empty so we write
			// the original p.Content unchanged.
			p.Content = override
		}
		// Fall through to the write path with accept semantics.
	}

	// Record the checkpoint BEFORE the write lands so Snapshot
	// reads the pre-write content as beforeSHA. Restore and
	// per-file Undo both key off beforeSHA to roll a file back;
	// taking the snapshot after the write would pin
	// beforeSHA == afterSHA and turn every Restore into a silent
	// no-op. See TestCrossChatConflictDetected in the checkpoint
	// package for the invariant (Snapshot → write).
	//
	// Tradeoff: if os.WriteFile below fails, we've already
	// appended a phantom snapshot event whose afterSHA never
	// landed. The cross-chat index records that afterSHA and the
	// next chat snapshotting this path may see a false-positive
	// drift conflict. Any subsequent successful write on the same
	// path overwrites the index entry, so the false positive is
	// bounded; a broken Restore would be permanent, which is the
	// worse failure mode.
	//
	// Snapshot is per-file (not per-workspace), so it only
	// touches files THIS chat's agent has written — unrelated
	// workspace state (other chats' agents, user editor buffers,
	// shell side-effects) survives every Restore click.
	//
	// The snapshot runs under context.Background because a failure
	// must not block the response the agent is waiting on. Any
	// error is logged and we still attempt the write; a missed
	// snapshot surfaces as a "no Restore button for this tool" in
	// the UI (via the oldest-tag gate) — worst case is loss of
	// undo for that one operation, not data loss.
	if h.checkpoints != nil {
		// Convert absolute path back to workspace-relative for the
		// event log. workspace.RelPath normalizes separators to
		// forward slashes so the stored path is portable.
		rel, relErr := workspace.RelPath(h.lifecycle.workDir, abs)
		if relErr == nil {
			messageCount := h.currentMessageCount(ctx, chatID)
			newContent := []byte(p.Content)
			if _, err := h.checkpoints.Snapshot(ctx, chatID, rel, newContent, messageCount); err != nil {
				slog.Warn("checkpoint snapshot failed", "chat_id", chatID, "path", rel, "error", err)
			}
		}
	}

	// Preserve the existing file's permission bits so the agent
	// can't silently demote a 0o755 script or promote a 0o600
	// secret to 0o644. New files use 0o644 as the default.
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(abs); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(abs, []byte(p.Content), mode); err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	h.respondBridge(ctx, chatID, msg, map[string]any{}, nil)
}

// chatInSupervisedMode reports whether the chat has SupervisedMode=true.
// Returns false on any lookup error (missing chat, store unavailable)
// so failures default to the permissive, pre-feature behaviour.
func (h *Hub) chatInSupervisedMode(ctx context.Context, chatID api.ChatID) bool {
	if chatID == "" {
		return false
	}
	c, ok := h.chatStore.Get(ctx, chatID)
	return ok && c.SupervisedMode
}

// currentMessageCount returns the number of persisted messages for
// chatID, or 0 if the chat isn't found. Used as the restore watermark
// on every snapshot.
func (h *Hub) currentMessageCount(ctx context.Context, chatID api.ChatID) int {
	if c, ok := h.chatStore.Get(ctx, chatID); ok {
		return len(c.Messages)
	}
	return 0
}
