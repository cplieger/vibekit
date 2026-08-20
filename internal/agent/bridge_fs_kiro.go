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

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"syscall"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
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
// assistant streaming. The fresh runtime-scoped context matters just as much — the
// per-event ctx is cancelled by translateACPEvent's defer the moment it returns,
// and Bridge.Respond drops a write on a cancelled ctx, which would hang the
// agent's Call forever (KAS's `extMethod` has NO timeout: no deadline, no abort).
func (in *inbound) handleKiroFSRequest(_ context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	var handler func(context.Context, vibekit.ChatID, *vibekit.RPCResponse)
	switch msg.Method {
	case methodKiroFSStat:
		handler = in.respondKiroFSStat
	case methodKiroFSReadDirectory:
		handler = in.respondKiroFSReadDirectory
	case methodKiroFSDelete:
		handler = in.respondKiroFSDelete
	default:
		return false
	}
	in.lifetime.inflight.Go(func() {
		ctx, cancel := in.lifetime.derivedContext()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("kiro fs handler panic",
					"chat_id", chatID, "method", msg.Method, "panic", r)
				in.respondBridge(ctx, chatID, msg, nil, errors.New("internal error"))
			}
		}()
		handler(ctx, chatID, msg)
	})
	return true
}

// kiroFSPath decodes the request's path and returns the workspace root together
// with the root-relative name that addresses it. The single place the
// containment gain is realised, so all three verbs share it.
//
// It returns the confined PAIR rather than an absolute path, because an absolute
// path is what the three verbs used to act on with ambient os calls — the
// resolver's containment verdict and the operation had no handle in common, so a
// directory renamed into a symlink after the verdict redirected the operation,
// delete included. See lifetime.confineInWorkDir.
func (in *inbound) kiroFSPath(msg *vibekit.RPCResponse) (root *os.Root, rel string, err error) {
	var p kiroFSParams
	if pErr := parseRequest(msg, &p); pErr != nil {
		return nil, "", fmt.Errorf("decode %s params: %w", msg.Method, pErr)
	}
	return in.lifetime.confineInWorkDir(p.Path)
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
func (in *inbound) respondKiroFSStat(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	root, rel, err := in.kiroFSPath(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	// Root.Stat follows symlinks, matching KAS's own fs.stat. Its symlink branch
	// is therefore unreachable there too; the vocabulary is kept whole because
	// read_directory DOES reach it.
	info, err := root.Stat(rel)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	body := kiroStatBody{Type: fsTypeFile, Size: info.Size()}
	if info.IsDir() {
		body.Type = fsTypeDirectory
	}
	in.respondBridge(ctx, chatID, msg, body, nil)
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
func (in *inbound) respondKiroFSReadDirectory(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	root, rel, err := in.kiroFSPath(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	dirEntries, err := readDirInRoot(root, rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			in.respondBridge(ctx, chatID, msg, kiroReadDirBody{Entries: []kiroDirEntry{}}, nil)
			return
		}
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	in.respondBridge(ctx, chatID, msg, kiroReadDirBody{
		Entries: in.filterDirEntries(ctx, chatID, rel, dirEntries),
	}, nil)
}

// readDirInRoot lists the directory at rel inside root.
//
// os.Root has no ReadDir, so the listing is an open plus File.ReadDir — and the
// two flags on that open are what make it a safe replacement for os.ReadDir
// rather than a mechanical one. O_DIRECTORY makes the KERNEL refuse anything at
// the name that is not a directory, so the "is it a directory" question is
// answered by the same syscall that opens it instead of by a separate stat.
// O_NONBLOCK is the one that matters operationally: root.Open is a plain
// O_RDONLY openat, and a reader-less FIFO left at the name blocks that open(2)
// indefinitely — which here would wedge the handler under lifetime.inflight
// against a KAS Call that carries no timeout. The flag has no effect on a
// directory, which is the only thing this can open, so it costs nothing.
func readDirInRoot(root *os.Root, rel string) ([]os.DirEntry, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

// filterDirEntries maps os.DirEntry values onto the wire shape, dropping any
// entry the agent-ignore list matches. dirRel is the listed directory's
// workspace-relative name ("." for the workspace root itself).
//
// There is no fail-open branch left. The old one skipped the filter — and kept
// the entry — when filepath.Rel failed on the absolute path, which put the
// discovery vector this filter exists to close behind an error nobody could
// trigger deliberately but nobody had ruled out either. Joining onto the
// already-relative dirRel cannot fail, so the case is gone rather than handled.
func (in *inbound) filterDirEntries(ctx context.Context, _ vibekit.ChatID, dirRel string, dirEntries []os.DirEntry) []kiroDirEntry {
	out := make([]kiroDirEntry, 0, len(dirEntries))
	for _, e := range dirEntries {
		entryType := fsTypeFile
		switch {
		case e.IsDir():
			entryType = fsTypeDirectory
		case e.Type()&os.ModeSymlink != 0:
			// File.ReadDir does not follow symlinks, matching node's
			// withFileTypes, so unlike stat this branch is live.
			entryType = fsTypeSymlink
		}
		// path.Join, not filepath.Join: the matcher documents slash-separated
		// paths and dirRel already is one (workspace.RelPath normalises it).
		if in.ignore != nil && in.ignore.Matches(ctx, path.Join(dirRel, e.Name()), e.IsDir()) {
			continue
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
func (in *inbound) respondKiroFSDelete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	root, rel, err := in.kiroFSPath(msg)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	// The workspace root itself, which is "." once the path is expressed relative
	// to the root. This replaces a filepath.Clean(abs) == filepath.Clean(workDir)
	// comparison and the filepath.IsAbs assertion that existed only to make that
	// comparison meaningful: a root-relative name has exactly one spelling for
	// the root, so there is nothing left to normalise or to assert.
	if rel == "." {
		in.respondFSError(ctx, chatID, msg, errRefusedWorkDirRoot)
		return
	}
	// The delete is the one verb whose lost race is unrecoverable, so it does not
	// settle for os.Root's confinement. atomicfile.OpenParentInRoot descends to
	// the parent component by component, Lstat-ing each one, REFUSING a symlink
	// instead of following it, and confirming with os.SameFile that the directory
	// it opened is the one it inspected. Naming only the final element through
	// that pinned handle removes every ancestor from the unlink's path: no
	// in-workspace symlink can redirect it, which plain root.Remove of a
	// multi-component name still permits because a root follows an in-root link
	// by design.
	parent, base, err := atomicfile.OpenParentInRoot(root, rel)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	defer func() { _ = parent.Close() }()

	info, err := parent.Lstat(base)
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	// Recursive for a directory, plain unlink otherwise — including for a
	// symlink, which Remove unlinks rather than follows. atomicfile's own
	// RemoveFileInRoot would refuse a symlink with ErrNotRegular; that is the
	// right rule for a writer sweeping names it created, and the wrong one here,
	// because this handler exists to CONFINE the delete KAS's NodeFileSystem
	// would otherwise do itself (fs.rm / unlink, which delete a symlink), and
	// diverging would make it a behaviour change rather than a confinement.
	if info.IsDir() {
		err = parent.RemoveAll(base)
	} else {
		err = parent.Remove(base)
	}
	if err != nil {
		in.respondFSError(ctx, chatID, msg, err)
		return
	}
	slog.Info("agent deleted a path", "chat_id", chatID, "dir", info.IsDir())
	// KAS's isFSDeleteCapabilityResponse accepts any object, and it THROWS a
	// non-empty `message` field as an error — so the success answer must be an
	// empty object, never one carrying a status string.
	in.respondBridge(ctx, chatID, msg, struct{}{}, nil)
}
