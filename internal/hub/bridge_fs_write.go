// File-system write request handlers for kiro-cli ACP bridges.
//
// Spec: https://agentclientprotocol.com/protocol/file-system

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cplieger/vibekit/internal/api"
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

	// Create parent directories only AFTER the gate: a rejected staged
	// create must be side-effect-free, and MkdirAll before the gate left
	// freshly created empty parent directories behind on reject.
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}

	// Preserve the existing file's permission bits so the agent can't
	// silently demote a 0o755 script or promote a 0o600 secret to 0o644.
	// New files use 0o644 as the default. Lstat (not Stat) so a symlink
	// at the target is seen as a symlink rather than its resolved target.
	mode := os.FileMode(0o644)
	if info, statErr := os.Lstat(abs); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			h.respondFSError(ctx, chatID, msg, errors.New("refusing to write through a symlink"))
			return
		}
		mode = info.Mode().Perm()
	}
	// Open with O_NOFOLLOW so a symlink planted at the final path component
	// AFTER resolveInsideWorkDir evaluated the path (a TOCTOU race) can't
	// redirect the write outside workDir — os.WriteFile follows symlinks,
	// this doesn't. Behaviour is identical for normal (non-symlink) writes.
	f, openErr := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if openErr != nil {
		h.respondFSError(ctx, chatID, msg, openErr)
		return
	}
	if _, wErr := f.WriteString(p.Content); wErr != nil {
		_ = f.Close()
		h.respondFSError(ctx, chatID, msg, wErr)
		return
	}
	if cErr := f.Close(); cErr != nil {
		h.respondFSError(ctx, chatID, msg, cErr)
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
	accepted, override, merged, err := h.stageFSWrite(ctx, chatID, msg, abs, reqPath, content)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return false, content
	}
	if !accepted {
		h.respondFSError(ctx, chatID, msg, errRejectedByUser)
		return false, content
	}
	if merged {
		// User-edited merged content overrides the agent's proposal.
		// Gated on the merged flag (set only when
		// resolve_pending_change_partial was used), NOT on override != "",
		// so an empty merge — e.g. a create whose only hunk was rejected —
		// writes the empty result instead of falling back to the agent's
		// original content.
		content = override
	}
	return true, content
}

// chatInSupervisedMode reports whether chatID has the supervised
// write-gate enabled.
//
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
