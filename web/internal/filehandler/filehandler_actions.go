package filehandler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"vibekit/internal/api"
)

// --- /api/files/action (POST: mkdir, touch, delete, rename, copy, move) ---

type fileAction struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	// `dest` and `name` are optional at the wire level; optionality is
	// enforced per-action in the handlers (resolveCopyMoveDest rejects
	// empty Dest for copy/move, actionRename rejects empty Name). Tags
	// stay uniform because `omitempty` has zero effect on decode.
	Dest string `json:"dest"`
	Name string `json:"name"`
}

// actionFunc is the signature every file-action handler matches. The
// request context is threaded through so long-running IO (copy) can
// respect client cancellation; ctx wraps r.Context() at the caller.
type actionFunc func(ctx context.Context, w http.ResponseWriter, body fileAction, resolved string, h *Handler) error

var fileActions = map[string]actionFunc{
	"mkdir":  actionMkdir,
	"touch":  actionTouch,
	"delete": actionDelete,
	"rename": actionRename,
	"copy":   actionCopy,
	"move":   actionMove,
}

func (h *Handler) handleFilesAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	api.LimitBody(w, r, api.MaxJSONBody)

	var body fileAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filehandler: action body too large",
				"limit", api.MaxJSONBody, "error", maxErr)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{api.JSONKeyError: "request body too large"})
			return
		}
		api.BadRequest(w, "invalid json")
		return
	}
	resolved := resolveOrForbid(w, body.Path)
	if resolved == "" {
		return
	}

	fn, ok := fileActions[body.Action]
	if !ok {
		api.BadRequest(w, "unknown action")
		return
	}
	if err := fn(r.Context(), w, body, resolved, h); err != nil {
		// fn may have already written a response (for synchronous bad-
		// input errors); sentinel errHandled signals that case so we
		// don't double-write.
		if errors.Is(err, errHandled) {
			return
		}
		slog.Warn("filehandler: action failed",
			"action", body.Action, "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{api.JSONKeyError: body.Action + " failed"})
		return
	}
	api.Ok(w)
}

func actionMkdir(_ context.Context, w http.ResponseWriter, _ fileAction, resolved string, h *Handler) error {
	// Symmetric with actionDelete/actionRename/actionMove destination
	// guards: creation paths also need the protected-dir gate so a
	// cold-boot request with `mkdir /config/chats` can't pre-empt the
	// chat store's own directory before it's materialised.
	if isProtectedDir(resolved) {
		slog.Warn("filehandler: mkdir blocked on protected dir", "path", resolved)
		api.Forbidden(w, "refusing to mkdir protected directory")
		return errHandled
	}
	// Match actionDelete's segment-depth guard: refuse to create
	// anything shallower than two segments deep. Without this, the
	// UI lets users mkdir `/new folder` at the FS root, then the
	// matching delete is refused by the depth guard and the user is
	// stuck with a folder they can't remove via the UI. Symmetric
	// guards: don't allow what we can't undo.
	clean := filepath.Clean(resolved)
	if strings.Count(clean, "/") < 2 {
		slog.Warn("filehandler: mkdir blocked on top-level path", "path", clean)
		api.Forbidden(w, "refusing to create top-level directory")
		return errHandled
	}
	if err := h.root.MkdirAll(h.relPath(resolved), 0o755); err != nil {
		return err
	}
	slog.Info("filehandler: mkdir", "path", resolved)
	return nil
}

func actionTouch(_ context.Context, w http.ResponseWriter, _ fileAction, resolved string, h *Handler) error {
	// Mirror of actionMkdir; also checks isSensitive so a creation of
	// an exact-match sensitive file (e.g. /config/push-subs.json)
	// is refused before it ever hits the filesystem.
	if isSensitive(resolved) || isProtectedDir(resolved) {
		slog.Warn("filehandler: touch blocked on protected path", "path", resolved)
		api.Forbidden(w, "refusing to touch protected path")
		return errHandled
	}
	// Match actionDelete's segment-depth guard (see actionMkdir for
	// the rationale): no creating top-level files we can't undo.
	clean := filepath.Clean(resolved)
	if strings.Count(clean, "/") < 2 {
		slog.Warn("filehandler: touch blocked on top-level path", "path", clean)
		api.Forbidden(w, "refusing to create top-level file")
		return errHandled
	}
	f, err := h.root.OpenFile(h.relPath(resolved), os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if closeErr := f.Close(); closeErr != nil {
		return closeErr
	}
	slog.Info("filehandler: touch", "path", resolved)
	return nil
}

func actionDelete(_ context.Context, w http.ResponseWriter, _ fileAction, resolved string, h *Handler) error {
	// Refuse to recursively delete anything shallower than two
	// segments deep (i.e. /foo is out, /foo/bar is fine). Prevents
	// catastrophic `rm -rf /home/kiro` accidents (or CSRF) via a
	// single API call. Users who genuinely want to nuke their home
	// dir can use the shell.
	//
	// resolved is already canonical (filepath.Clean ran inside
	// resolvePath). Re-clean defensively so the slash-count invariant
	// survives future callers that forget the prelude.
	clean := filepath.Clean(resolved)
	if clean == "/" {
		api.Forbidden(w, "cannot delete root")
		return errHandled
	}
	segments := strings.Count(clean, "/")
	if segments < 2 {
		api.Forbidden(w, "refusing to delete top-level directory")
		return errHandled
	}
	// Layered guard: the segment count stops `/config` (2 slashes too
	// few) but would let `/config/chats` through because it has two
	// slashes and isSensitive only matches the files inside. The
	// isProtectedDir helper closes that gap by blocking the container
	// directories of every sensitive path too.
	if isProtectedDir(clean) {
		slog.Warn("filehandler: delete blocked on protected dir", "path", clean)
		api.Forbidden(w, "refusing to delete protected directory")
		return errHandled
	}
	if err := h.root.RemoveAll(h.relPath(resolved)); err != nil {
		return err
	}
	slog.Info("filehandler: delete", "path", resolved)
	return nil
}

func actionRename(_ context.Context, w http.ResponseWriter, body fileAction, resolved string, h *Handler) error {
	// Source-side protected-dir gate. isSensitive on the source leaves
	// /config/chats (no trailing slash) through because the sensitive
	// prefix is /config/chats/; without this gate, a single rename
	// request could move the entire chat store onto a fresh name and
	// orphan the server's view of its state. S3 closed this for
	// actionDelete via isProtectedDir; rename/move share the same
	// destructive semantics and need the same guard.
	if isProtectedDir(resolved) {
		slog.Warn("filehandler: rename blocked on protected dir", "path", resolved)
		api.Forbidden(w, "refusing to rename protected directory")
		return errHandled
	}
	// `name` must be a single-segment, non-empty, non-traversal
	// filename. Anything with a path separator or ".." gets rejected
	// before we touch the filesystem. filepath.Base alone isn't
	// enough: a bare ".." would pass Base untouched, then Join
	// silently escapes to the parent directory.
	name := body.Name
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') ||
		strings.ContainsRune(name, 0) {
		api.BadRequest(w, "invalid name")
		return errHandled
	}
	dest := filepath.Join(filepath.Dir(resolved), name)
	// Route the destination through resolvePath so the blacklist +
	// real-path (EvalSymlinks) checks fire against the rename target
	// the same way copy/move enforce them via resolveCopyMoveDest.
	// Without this the top-level blacklist was not enforced on the
	// destination (EXDEV saved us in practice, but the design
	// invariant "everything we touch passes resolvePath" was broken).
	destResolved, err := resolvePath(dest)
	if err != nil {
		slog.Warn("filehandler: rename dest rejected",
			"from", resolved, "to", dest, "reason", err.Error())
		api.Forbidden(w, err.Error())
		return errHandled
	}
	// Paranoia: confirm the resolved destination is still a direct
	// child of the original parent (defense in depth against
	// separator surprises on non-Linux filesystems).
	if filepath.Dir(destResolved) != filepath.Dir(resolved) {
		api.Forbidden(w, "rename escapes parent directory")
		return errHandled
	}
	// Sensitive-path check on the DESTINATION. Without this the
	// touch→write→rename sequence trivially overwrites
	// /config/home/.kiro/steering/vibekit.md (or any other sensitive
	// file) because rename targets a directory that's already passed
	// the lexical guard. Applying isSensitive to the destination
	// closes that bypass. isProtectedDir layers on top so a rename
	// that would land a decoy directory at exactly /config/chats (or
	// any other bare-directory sensitive prefix name) on cold boot
	// is also refused.
	if isSensitive(destResolved) || isProtectedDir(destResolved) {
		slog.Warn("filehandler: rename blocked on sensitive dest",
			"from", resolved, "to", destResolved)
		api.Forbidden(w, "rename target is protected")
		return errHandled
	}
	if err := h.root.Rename(h.relPath(resolved), h.relPath(destResolved)); err != nil {
		return err
	}
	slog.Info("filehandler: rename", "from", resolved, "to", destResolved)
	return nil
}

func actionCopy(ctx context.Context, w http.ResponseWriter, body fileAction, resolved string, h *Handler) (err error) {
	destResolved, err := resolveCopyMoveDest(w, body)
	if err != nil {
		return err
	}
	// Copy is non-destructive on the source (unlike rename/move),
	// so only the destination needs the protected-dir / sensitive-
	// path gate. Keeps the per-action pattern uniform with rename
	// and move.
	if isSensitive(destResolved) || isProtectedDir(destResolved) {
		slog.Warn("filehandler: copy blocked on sensitive dest",
			"from", resolved, "to", destResolved)
		api.Forbidden(w, "copy target is protected")
		return errHandled
	}
	n, scErr := streamCopy(ctx, resolved, destResolved, maxCopySize, h)
	if scErr != nil {
		if errors.Is(scErr, errOversize) {
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{api.JSONKeyError: "source file too large to copy"})
			return errHandled
		}
		return scErr
	}
	slog.Info("filehandler: copy", "from", resolved, "to", destResolved, "bytes", n)
	return nil
}

// errOversize is returned by streamCopy when the source exceeds the
// size cap, either detected via Stat or via the LimitReader tail guard.
var errOversize = errors.New("source file too large")

// streamCopy streams the file at srcPath into destPath atomically via
// write-to-temp-then-rename. The copy is capped at sizeCap bytes;
// exceeding it returns errOversize. The context allows callers to
// cancel mid-stream (e.g. on client disconnect).
func streamCopy(ctx context.Context, srcPath, destPath string, sizeCap int64, h *Handler) (int64, error) {
	in, err := h.root.Open(h.relPath(srcPath))
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	// Stat the source so we can reject oversize copies before
	// allocating the destination and burning IO bandwidth.
	info, statErr := in.Stat()
	if statErr != nil {
		slog.Debug("filehandler: copy source stat failed, relying on LimitReader",
			"path", srcPath, "error", statErr)
	} else if info.Size() > sizeCap {
		return 0, errOversize
	}

	// Stream into a temp sibling of dest; rename into place on
	// success so a crash / size-cap trip / mid-stream disconnect
	// never leaves a truncated file at destPath.
	destDir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(destDir, filepath.Base(destPath)+".copy-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	// LimitReader with sizeCap+1 catches sparse files / races
	// where Stat under-reports size.
	buf := make([]byte, copyBufSize)
	n, copyErr := io.CopyBuffer(tmp, &ctxReader{
		ctx: ctx,
		r:   io.LimitReader(in, sizeCap+1),
	}, buf)
	if copyErr != nil {
		_ = tmp.Close()
		return 0, copyErr
	}
	if n > sizeCap {
		_ = tmp.Close()
		slog.Warn("filehandler: copy source exceeded cap after stat",
			"path", srcPath, "copied_bytes", n)
		return 0, errOversize
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		_ = tmp.Close()
		return 0, syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return 0, closeErr
	}
	if renameErr := h.root.Rename(h.relPath(tmpName), h.relPath(destPath)); renameErr != nil {
		return 0, renameErr
	}
	cleanup = false
	return n, nil
}

func actionMove(_ context.Context, w http.ResponseWriter, body fileAction, resolved string, h *Handler) error {
	// Source-side protected-dir gate. Moving /config/chats to
	// /workspace/stolen-chats detaches the chat store from its
	// expected location and orphans the server view. Same logic as
	// actionRename — rename/move are the two destructive-on-source
	// operations and need matching guards. Copy is non-destructive
	// so it skips this check.
	if isProtectedDir(resolved) {
		slog.Warn("filehandler: move blocked on protected dir", "path", resolved)
		api.Forbidden(w, "refusing to move protected directory")
		return errHandled
	}
	destResolved, err := resolveCopyMoveDest(w, body)
	if err != nil {
		return err
	}
	// Destination guard mirrors actionRename: sensitive files and
	// protected directories are both off-limits as move targets.
	if isSensitive(destResolved) || isProtectedDir(destResolved) {
		slog.Warn("filehandler: move blocked on sensitive dest",
			"from", resolved, "to", destResolved)
		api.Forbidden(w, "move target is protected")
		return errHandled
	}
	if err := h.root.Rename(h.relPath(resolved), h.relPath(destResolved)); err != nil {
		return err
	}
	slog.Info("filehandler: move", "from", resolved, "to", destResolved)
	return nil
}

// resolveCopyMoveDest validates + resolves the `dest` field common to
// copy and move. Returns errHandled when it wrote an error response.
func resolveCopyMoveDest(w http.ResponseWriter, body fileAction) (string, error) {
	if body.Dest == "" {
		api.BadRequest(w, "missing dest")
		return "", errHandled
	}
	destResolved, err := resolvePath(body.Dest)
	if err != nil {
		slog.Warn("filehandler: dest path rejected",
			"dest", body.Dest, "reason", err.Error())
		api.Forbidden(w, err.Error())
		return "", errHandled
	}
	return destResolved, nil
}

// ctxReader wraps an io.Reader with a context. Every Read first checks
// ctx.Err() so a cancelled/disconnected request aborts the copy on
// the next chunk boundary instead of running to completion after the
// client is long gone.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
