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

	"github.com/cplieger/vibekit/internal/vibekit"
)

// respondFSWrite handles fs/write_text_file. Request params:
//
//	{ sessionId, path, content: "..." }
//
// Response: empty object on success. Caps content at fsWriteCap. Creates
// parent directories (0o755) if missing.
//
// Supervised mode does not reach here. KAS gates the whole turn, so a write
// arriving on this handler is already authorized and applies immediately —
// including the REVERT of a rejected action, which KAS sends back as an
// ordinary fs/write_text_file. Do not gate, stage, snapshot or attribute that
// write as agent work: it would double-count the changed-files ledger, and
// under any surviving gate it would deadlock.
func (h *Hub) respondFSWrite(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
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

	// No supervised gate here — see the note further down. A write reaching this
	// handler has already been authorized (either autopilot is on, or KAS's
	// turn approval accepted the action), so it goes to disk.
	//
	// MkdirAll used to be deliberately sequenced AFTER the gate, because a
	// rejected staged create had to be side-effect-free and creating parents first
	// left empty directories behind. With no reject path here that ordering
	// constraint is gone; KAS restores rejected files from its own snapshots.
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

// THERE IS NO WRITE GATE. applySupervisedWriteGate held every agent write in
// memory, staged it, broadcast it and waited for a per-file verdict before
// letting it reach disk. KAS reviews a whole TURN instead (`autopilot: false` →
// a turn_approval permission request), so a write arriving here is already
// authorized and goes to disk unconditionally.
//
// Do not add a second gate. Two gates must not both be on: KAS applies the
// accepted actions and restores the rejected ones from its own snapshots, and a
// vibekit-side hold would make its restore operate on content KAS never wrote.

// currentMessageCount returns the number of persisted messages for
// chatID, or 0 if the chat isn't found. Used as the restore watermark
// on every snapshot.
func (h *Hub) currentMessageCount(ctx context.Context, chatID vibekit.ChatID) int {
	if c, ok := h.chatStore.Get(ctx, chatID); ok {
		return len(c.Messages)
	}
	return 0
}
