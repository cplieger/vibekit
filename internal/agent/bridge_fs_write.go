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
// Supervised mode does not reach here. KAS gates the whole turn, so a write
// arriving on this handler is already authorized and applies immediately —
// including the REVERT of a rejected action, which KAS sends back as an
// ordinary fs/write_text_file. Do not gate, stage, snapshot or attribute that
// write as agent work: it would double-count the changed-files ledger, and
// under any surviving gate it would deadlock.
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

	// No supervised gate here — see the note further down. A write reaching this
	// handler has already been authorized (either autopilot is on, or KAS's
	// turn approval accepted the action), so it goes to disk.

	// Preserve the existing file's permission bits so the agent can't silently
	// demote a 0o755 script or promote a 0o600 secret to 0o644. New files use
	// 0o644. Lstat (not Stat) so a symlink at the target is seen as a symlink
	// rather than its resolved target, and confined so the name cannot be read
	// through a swapped ancestor. Only a REGULAR file's mode is adopted: the
	// permission bits of a symlink, FIFO or device node describe an object this
	// write refuses to replace, so reading them would be meaningless. The
	// refusal itself is atomicfile's, one layer down — ErrSymlinkTarget for a
	// symlink and ErrNotRegular for the rest, which is a sentinel where the
	// hand-rolled check here was a bare string.
	mode := os.FileMode(0o644)
	if info, statErr := root.Lstat(rel); statErr == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}

	// One confined atomic write, replacing MkdirAll + Lstat + O_NOFOLLOW open +
	// WriteString + Sync + Close. Four things change, all in the same direction:
	//
	//   - Every component of rel is re-resolved inside the root on every
	//     operation, so an ancestor swapped for a symlink after the resolve
	//     cannot redirect the write out of the workspace. syscall.O_NOFOLLOW,
	//     which the deleted open carried for exactly this reason, only ever
	//     guarded the FINAL component.
	//   - The rewrite is no longer in place. The old sequence's own comment
	//     named the hazard it could not fix — O_TRUNC destroys the old contents
	//     before the new ones are durable, so a crash mid-write leaves a
	//     TRUNCATED file and Sync was the only thing standing between the agent
	//     and data loss. A temp-then-rename has no such window: the target is
	//     either the old bytes or all of the new ones.
	//   - WithMkdirMode replaces os.MkdirAll, and it fsyncs each level it
	//     creates into that level's PARENT and enforces the mode there. MkdirAll
	//     reports only success, so a directory entry it created was present but
	//     not durable, and a crash could take the whole subtree — including the
	//     file this write just fsynced. It also stored 0o700 for a requested
	//     0o755 under umask 077, because a mkdir mode is a request.
	//   - A target occupied by a directory, FIFO, device node or socket is
	//     refused up front instead of being opened. The deleted open would have
	//     BLOCKED in open(2) on a FIFO left at the name, wedging this handler
	//     under lifetime.inflight against a KAS Call that has no timeout.
	//
	// What it costs, stated: the file's inode changes, so a hard link to it
	// keeps the old contents, the owner becomes this process, and a watcher sees
	// a rename rather than a modify. Every writer in this container is root and
	// nothing in the workspace is hard-linked by vibekit, and inotify consumers
	// (dev servers, test watchers) handle both events — this is the same trade
	// the chat store already takes for the same reason.
	if _, wErr := atomicfile.WriteFileInRoot(ctx, root, rel, []byte(p.Content),
		atomicfile.WithMode(mode),
		atomicfile.WithMkdirMode(0o755),
	); wErr != nil {
		in.respondFSError(ctx, chatID, msg, wErr)
		return
	}
	in.respondBridge(ctx, chatID, msg, map[string]any{}, nil)
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
