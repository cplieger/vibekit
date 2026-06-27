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

	// Supervised gate. When the chat is in supervised mode (and per-turn
	// trust is not set) the write is staged and this call blocks until
	// the user resolves it. A reject/error responds to the bridge inside
	// the helper, so proceed=false means stop here; on accept, content
	// is the effective content to write (a user-edited partial merge
	// overrides the agent's proposal, otherwise it is unchanged).
	proceed, content := h.applySupervisedWriteGate(ctx, chatID, msg, abs, p.Path, p.Content)
	if !proceed {
		return
	}
	p.Content = content

	// Record the checkpoint BEFORE the write lands so beforeSHA captures
	// the pre-write content (Restore and per-file Undo both depend on
	// it). See recordCheckpointSnapshot for the full rationale and the
	// phantom-snapshot tradeoff on a failed write.
	h.recordCheckpointSnapshot(ctx, chatID, abs, []byte(p.Content))

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

// applySupervisedWriteGate runs the Supervised-mode staging gate for a
// write. When the chat is in supervised mode without per-turn trust, it
// stages the write and blocks until the user resolves it. It returns
// proceed=false (having already responded to the bridge with the
// rejection or error) when the caller must NOT write; otherwise
// proceed=true and effective is the content to write — a user-edited
// partial merge when one was supplied, otherwise the original content
// unchanged. Outside supervised mode (or with per-turn trust active) it
// is a pass-through that returns the content unchanged.
func (h *Hub) applySupervisedWriteGate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, abs, reqPath, content string) (proceed bool, effective string) {
	if !h.chatInSupervisedMode(ctx, chatID) || h.perm.supervised.HasTrust(chatID) {
		return true, content
	}
	accepted, override, err := h.stageFSWrite(ctx, chatID, msg, abs, reqPath, content)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return false, content
	}
	if !accepted {
		h.respondFSError(ctx, chatID, msg, errRejectedByUser)
		return false, content
	}
	if override != "" {
		// User-edited merged content overrides the agent's proposal.
		// Only set when resolve_pending_change_partial was used; a plain
		// accept leaves override empty so the original content stands.
		content = override
	}
	return true, content
}

// recordCheckpointSnapshot records a pre-write checkpoint snapshot for
// chatID's write to abs so Restore and per-file Undo can roll the file
// back. No-op when checkpoints are disabled.
//
// The snapshot is taken BEFORE the write lands so Snapshot reads the
// pre-write content as beforeSHA; taking it after would pin
// beforeSHA == afterSHA and turn every Restore into a silent no-op.
//
// Tradeoff: if the subsequent os.WriteFile fails, a phantom snapshot
// whose afterSHA never landed has already been appended. The cross-chat
// index records that afterSHA and the next chat snapshotting this path
// may see a false-positive drift conflict; any later successful write on
// the same path overwrites the index entry, so the false positive is
// bounded — a broken Restore would be permanent, the worse failure mode.
//
// Snapshot is per-file (not per-workspace), so it only touches files
// THIS chat's agent has written. Failures are logged and the write still
// proceeds: a missed snapshot only costs undo for that one operation,
// never data.
func (h *Hub) recordCheckpointSnapshot(ctx context.Context, chatID api.ChatID, abs string, content []byte) {
	if h.checkpoints == nil {
		return
	}
	// workspace.RelPath normalizes separators to forward slashes so the
	// stored path is portable.
	rel, relErr := workspace.RelPath(h.lifecycle.workDir, abs)
	if relErr != nil {
		return
	}
	messageCount := h.currentMessageCount(ctx, chatID)
	if _, err := h.checkpoints.Snapshot(ctx, chatID, rel, content, messageCount); err != nil {
		slog.Warn("checkpoint snapshot failed", "chat_id", chatID, "path", rel, "error", err)
	}
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
