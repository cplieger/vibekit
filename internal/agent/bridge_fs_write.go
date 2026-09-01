// File-system write request handlers for kiro-cli ACP bridges.
//
// Spec: https://agentclientprotocol.com/protocol/file-system

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// respondFSWrite handles fs/write_text_file. Request params:
//
//	{ sessionId, path, content: "..." }
//
// Response: empty object on success. Caps content at fsWriteCap. Creates
// parent directories (0o755) if missing.
//
// A write reaching this handler is already authorized (KAS gates the whole
// turn) and applies immediately — including the REVERT of a rejected
// action, sent back as an ordinary fs/write_text_file. Do not gate, stage,
// snapshot or attribute that write as agent work: it would double-count the
// changed-files ledger, and under any surviving gate it would deadlock.
func (in *inbound) respondFSWrite(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("parse params: %w", err))
		return
	}
	if p.Path == "" {
		in.respondFSError(ctx, chatID, msg, errors.New("path is required"))
		return
	}
	if len(p.Content) > fsWriteCap {
		in.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: %d", errCapExceeded, fsWriteCap))
		return
	}
	root, rel, err := in.lifetime.confineInWorkDir(p.Path)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}

	// Preserve the existing file's permission bits so the agent can't silently
	// demote a 0o755 script or promote a 0o600 secret to 0o644. New files use
	// 0o644. Lstat (not Stat) so a symlink at the target is seen as a symlink
	// rather than its resolved target. Only a REGULAR file's mode is adopted —
	// the permission bits of a symlink, FIFO or device node describe an object
	// this write refuses to replace. atomicfile refuses the rest one layer down
	// (ErrSymlinkTarget / ErrNotRegular).
	mode := os.FileMode(0o644)
	if info, statErr := root.Lstat(rel); statErr == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}

	// One confined atomic write. Every component of rel is re-resolved inside
	// the root on every operation, so a swapped ancestor cannot redirect the
	// write. Temp-then-rename means the target is either the old bytes or all
	// of the new ones, never a truncated partial. WithMkdirMode fsyncs each
	// created directory level into its parent and enforces the mode there. A
	// target occupied by a directory, FIFO, device node or socket is refused up
	// front rather than opened (a FIFO's open(2) would otherwise block this
	// handler indefinitely under lifetime.inflight, against a KAS Call with no
	// timeout).
	if _, wErr := atomicfile.WriteFileInRoot(ctx, root, rel, []byte(p.Content),
		atomicfile.WithMode(mode),
		atomicfile.WithMkdirMode(0o755),
	); wErr != nil {
		in.respondFSError(ctx, chatID, msg, wErr)
		return
	}
	in.respondBridge(ctx, chatID, msg, map[string]any{}, nil)
}

// THERE IS NO WRITE GATE. KAS reviews a whole TURN (`autopilot: false` → a
// turn_approval permission request), so a write arriving here is already
// authorized and goes to disk unconditionally. Do not add a second gate: KAS
// restores a rejected action from its own snapshot, and a vibekit-side hold
// would make that restore operate on content KAS never wrote.
