// KAS's own filesystem verbs: `_kiro/fs/{stat,read_directory,delete}`.
//
// # Why these are implemented at all
//
// Each of the three is gated on `clientCapabilities.fs._meta.kiro.<name> === true`
// (strict, `resolveCapabilities` in KAS's resolved-capabilities.ts). The
// else-branch is NOT `execute_bash` and it is NOT a failure: it is a bare
// `NodeFileSystem`, which does the `fs.stat` / `fs.readdir` / `fs.rm` **inside the
// KAS process**. So an undeclared verb is not an absent capability, it is the same
// capability with no vibekit path check on it at all.
//
// That inverts the usual reasoning. Declaring `delete` does not GRANT the agent a
// delete it lacked — it already had one — it puts the delete it already had behind
// `resolveInsideWorkDir` for the first time. And declaring `read_directory` is
// what lets the agent-ignore list reach a LISTING, which is the vector the list
// exists to close: an unfiltered listing is how an agent discovers the
// `.env.dec` that the read filter would then refuse to open.
//
// # The division of labour
//
// vibekit is the confined EXECUTOR; KAS is the REVIEWER. These handlers resolve,
// filter, and execute. They do NOT block, stage, snapshot, or attribute — and
// that is a decision, not an omission. KAS's `DeleteFile` tool already
// checkpoints before it unlinks and already routes through `acpToolApproval`, and
// its per-turn review restores a rejected delete by writing the snapshot back
// through an ordinary `fs/write_text_file` A→C request. A second vibekit-side
// gate would intercept that restore and re-stage it, asking the user to approve
// the undoing of their own rejection and stalling KAS mid-`restorePendingChanges`
// with the remaining actions unreverted. Two gates must not both be on. A restore
// write is a user-instructed write (invariant 4's spirit): let it through.
//
// Read and write are deliberately NOT declared here. `fs._meta.kiro.readFile` /
// `writeFile` would move them off the `fs/read_text_file` / `fs/write_text_file`
// rung vibekit implements — the rung that carries the supervised staging path —
// onto this one, which has no staging. See `vibekit-acp.md`.

package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/workspace"
)

// File-type strings KAS's own NodeFileSystem returns, and therefore the only
// values its consumers know how to read.
const (
	fsTypeFile      = "file"
	fsTypeDirectory = "directory"
	fsTypeSymlink   = "symlink"
)

// errRefusedWorkDirRoot rejects a delete aimed at the workspace root itself.
var errRefusedWorkDirRoot = errors.New("refusing to delete the workspace root")

// kiroFSParams is the shape all three verbs share: `{sessionId, path}`. The
// path arrives already absolute — KAS's resolveAcpPath joins a relative one onto
// the session cwd before it reaches the wire — so resolveInsideWorkDir is
// re-checking a claim rather than completing one, which is the point.
type kiroFSParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

// kiroStatBody answers `_kiro/fs/stat`.
//
// `size` is present because KAS's `isFSStatCapabilityResponse` requires BOTH
// `type` and `size` to be present or it throws "Invalid stat response" — even
// though `KiroStatAdapter` then returns only `{type}` and no consumer reads the
// size. Dropping the field would break the call for no gain.
type kiroStatBody struct {
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// kiroDirEntry is one `_kiro/fs/read_directory` entry.
type kiroDirEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// kiroReadDirBody answers `_kiro/fs/read_directory`. Entries is non-nil so an
// empty directory marshals to `[]` rather than `null`: KAS's guard only checks
// that the `entries` key exists, but it then calls `.map` on the value.
type kiroReadDirBody struct {
	Entries []kiroDirEntry `json:"entries"`
}

// handleKiroFSRequest dispatches the three `_kiro/fs/*` verbs. Returns true when
// msg was one of them.
//
// Dispatched async under inflight for the same reason as `fs/read_text_file`:
// these touch the disk, and blocking the forward goroutine stalls this chat's
// assistant streaming. The fresh hub-scoped context matters just as much — the
// per-event ctx is cancelled by translateACPEvent's defer the moment it returns,
// and Bridge.Respond drops a write on a cancelled ctx, which would hang the
// agent's Call forever (KAS's `extMethod` has NO timeout: no deadline, no abort).
func (h *Hub) handleKiroFSRequest(_ context.Context, chatID api.ChatID, msg *api.RPCResponse) bool {
	var handler func(context.Context, api.ChatID, *api.RPCResponse)
	switch msg.Method {
	case methodKiroFSStat:
		handler = h.respondKiroFSStat
	case methodKiroFSReadDirectory:
		handler = h.respondKiroFSReadDirectory
	case methodKiroFSDelete:
		handler = h.respondKiroFSDelete
	default:
		return false
	}
	h.lifecycle.inflight.Go(func() {
		ctx, cancel := h.hubContext()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("kiro fs handler panic",
					"chat_id", chatID, "method", msg.Method, "panic", r)
				h.respondBridge(ctx, chatID, msg, nil, errors.New("internal error"))
			}
		}()
		handler(ctx, chatID, msg)
	})
	return true
}

// kiroFSPath decodes and confines the request's path. The single place the
// containment gain is realised, so all three verbs share it.
func (h *Hub) kiroFSPath(msg *api.RPCResponse) (abs string, err error) {
	var p kiroFSParams
	if pErr := parseRequest(msg, &p); pErr != nil {
		return "", fmt.Errorf("decode %s params: %w", msg.Method, pErr)
	}
	return h.resolveInsideWorkDir(p.Path)
}

// respondKiroFSStat answers `_kiro/fs/stat` with `{type, size}`.
//
// Deliberately NOT filtered through the agent-ignore list, unlike
// read_directory. Filtering it would make KAS's derived `exists()` (a stat in a
// try/catch) report false for a file that is really there — and because a WRITE
// is not ignore-filtered (git semantics: an ignored file stays writable), the
// agent's next move on a false "absent" is to CREATE it, clobbering the very
// file the user asked to keep out of the way. An honest stat is the safer answer;
// the listing is where the discovery vector actually is.
func (h *Hub) respondKiroFSStat(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	abs, err := h.kiroFSPath(msg)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	// os.Stat follows symlinks, matching KAS's own fs.stat. Its symlink branch
	// is therefore unreachable there too; the vocabulary is kept whole because
	// read_directory DOES reach it.
	info, err := os.Stat(abs)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	body := kiroStatBody{Type: fsTypeFile, Size: info.Size()}
	if info.IsDir() {
		body.Type = fsTypeDirectory
	}
	h.respondBridge(ctx, chatID, msg, body, nil)
}

// respondKiroFSReadDirectory answers `_kiro/fs/read_directory` with `{entries}`,
// FILTERED through the agent-ignore list.
//
// The filter is the whole reason this verb is worth declaring. Without it the
// listing is the discovery vector for exactly the files the read filter refuses
// to open, which §15.8 names as confined-but-unfiltered.
//
// A missing directory answers with an empty list rather than an error, matching
// KAS's NodeFileSystem (it swallows ENOENT and returns []). Diverging would make
// a probe for an optional directory look like a failure.
func (h *Hub) respondKiroFSReadDirectory(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	abs, err := h.kiroFSPath(msg)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.respondBridge(ctx, chatID, msg, kiroReadDirBody{Entries: []kiroDirEntry{}}, nil)
			return
		}
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	h.respondBridge(ctx, chatID, msg, kiroReadDirBody{
		Entries: h.filterDirEntries(ctx, chatID, abs, dirEntries),
	}, nil)
}

// filterDirEntries maps os.DirEntry values onto the wire shape, dropping any
// entry the agent-ignore list matches.
//
// Fails OPEN on a relative-path failure (the entry is kept, with a Warn),
// matching the read handler: resolveInsideWorkDir has already anchored abs under
// workDir so Rel cannot fail in practice, and dropping a listing silently on an
// impossible error would be the harder failure to diagnose.
func (h *Hub) filterDirEntries(ctx context.Context, chatID api.ChatID, abs string, dirEntries []os.DirEntry) []kiroDirEntry {
	out := make([]kiroDirEntry, 0, len(dirEntries))
	for _, e := range dirEntries {
		entryType := fsTypeFile
		switch {
		case e.IsDir():
			entryType = fsTypeDirectory
		case e.Type()&os.ModeSymlink != 0:
			// os.ReadDir does not follow symlinks, matching node's
			// withFileTypes, so unlike stat this branch is live.
			entryType = fsTypeSymlink
		}
		if h.perm.ignore != nil {
			rel, relErr := workspace.RelPath(h.lifecycle.workDir, filepath.Join(abs, e.Name()))
			if relErr != nil {
				slog.Warn("ignore check skipped: filepath.Rel failed",
					"chat_id", chatID, "dir", abs, "entry", e.Name(), "error", relErr)
			} else if h.perm.ignore.Matches(ctx, rel, e.IsDir()) {
				continue
			}
		}
		out = append(out, kiroDirEntry{Name: e.Name(), Type: entryType})
	}
	return out
}

// respondKiroFSDelete answers `_kiro/fs/delete` with `{}`.
//
// Recursive for a directory, matching KAS's NodeFileSystem (`fs.rm` with
// recursive, versus `unlink` for a file) — the fallback this replaces, so
// mirroring it is what keeps the change a confinement rather than a behaviour
// change. The one refusal is the workspace root itself: no tool means it (the
// `delete_file` schema's arg is `targetFile`), and it is unrecoverable.
//
// NOT ignore-filtered, and NOT gated. Not filtered because a delete is
// write-class and writes follow git semantics (an ignored file stays writable);
// not gated because KAS checkpoints before the unlink and reviews after it, and a
// second gate here would intercept its restore write. See the file header.
func (h *Hub) respondKiroFSDelete(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	abs, err := h.kiroFSPath(msg)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	// Absoluteness is asserted, not assumed. resolveInsideWorkDir returns an
	// absolute path today, but the root comparison below is only meaningful
	// against one — a relative path would slip past it and hand os.RemoveAll a
	// target resolved against the SERVER's cwd. Cheap, and it forecloses the
	// worst blast radius on this path.
	if !filepath.IsAbs(abs) {
		h.respondFSError(ctx, chatID, msg, fmt.Errorf("%w: resolved path is not absolute", errRefusedWorkDirRoot))
		return
	}
	if filepath.Clean(abs) == filepath.Clean(h.lifecycle.workDir) {
		h.respondFSError(ctx, chatID, msg, errRefusedWorkDirRoot)
		return
	}
	info, err := os.Lstat(abs)
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	if info.IsDir() {
		err = os.RemoveAll(abs)
	} else {
		err = os.Remove(abs)
	}
	if err != nil {
		h.respondFSError(ctx, chatID, msg, err)
		return
	}
	slog.Info("agent deleted a path", "chat_id", chatID, "dir", info.IsDir())
	// KAS's isFSDeleteCapabilityResponse accepts any object, and it THROWS a
	// non-empty `message` field as an error — so the success answer must be an
	// empty object, never one carrying a status string.
	h.respondBridge(ctx, chatID, msg, struct{}{}, nil)
}
