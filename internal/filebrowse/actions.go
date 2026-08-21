package filebrowse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp/v2"
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
type actionFunc func(ctx context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error

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
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)

	var body fileAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: action body too large",
				"limit", webhttp.MaxJSONBody, "error", maxErr)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("request body too large"))
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}
	l, ok := h.resolveOrForbid(w, body.Path)
	if !ok {
		return
	}

	fn, exists := fileActions[body.Action]
	if !exists {
		httpreply.BadRequest(w, "unknown action")
		return
	}
	if err := fn(r.Context(), w, body, l, h); err != nil {
		// fn may have already written a response (for synchronous bad-
		// input errors); sentinel errHandled signals that case so we
		// don't double-write.
		if errors.Is(err, errHandled) {
			return
		}
		slog.Warn("filebrowse: action failed",
			"action", body.Action, "path", l.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(body.Action+" failed"))
		return
	}
	webhttp.Ok(w)
}

// refuseMountPoint writes the shared 403 for create/delete/rename/move
// aimed at a granted root itself. Mounts are boot-time configuration;
// the UI must not be able to remove or shadow one.
func refuseMountPoint(w http.ResponseWriter, action string, l loc) error {
	slog.Warn("filebrowse: "+action+" blocked on granted root", "path", l.abs)
	httpreply.Forbidden(w, "refusing to "+action+" a granted root")
	return errHandled
}

func actionMkdir(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, _ *Handler) error {
	// Symmetric with actionDelete/actionRename/actionMove destination
	// guards: creation paths also need the protected-dir gate so a
	// cold-boot request with `mkdir /config/chats` can't pre-empt the
	// chat store's own directory before it's materialised.
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: mkdir blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to mkdir protected directory")
		return errHandled
	}
	// A granted root always exists; "creating" it is either a no-op or
	// (via a nested grant) an attempt to shadow a mount. Refuse, matching
	// actionDelete: don't allow what we can't undo.
	if l.isMountPoint() {
		return refuseMountPoint(w, "mkdir", l)
	}
	if err := l.m.root.MkdirAll(l.rel(), 0o755); err != nil {
		return err
	}
	slog.Info("filebrowse: mkdir", "path", l.abs)
	return nil
}

func actionTouch(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, _ *Handler) error {
	// Mirror of actionMkdir; also checks IsSensitive so a creation of
	// an exact-match sensitive file (e.g. /config/push-subs.json)
	// is refused before it ever hits the filesystem.
	if IsSensitive(l.abs) || isProtectedDir(l.abs) {
		slog.Warn("filebrowse: touch blocked on protected path", "path", l.abs)
		httpreply.Forbidden(w, "refusing to touch protected path")
		return errHandled
	}
	if l.isMountPoint() {
		return refuseMountPoint(w, "touch", l)
	}
	// O_EXCL rather than the syscall.O_NOFOLLOW this used to carry. The flag was
	// INERT: os.Root.OpenFile ORs O_NOFOLLOW in itself and re-resolves the link
	// on the resulting ELOOP, so a caller-supplied one is silently ignored
	// (go1.27.0, src/os/root_unix.go:85-101, and pinned by this package's own
	// search_test.go). O_EXCL is a refusal the kernel does honour: anything
	// already at the name — including a symlink planted after resolvePath
	// accepted it — makes the create fail instead of opening whatever it points
	// at. An existing entry is touch's no-op case, so EEXIST is success, which
	// keeps the observable behaviour identical (O_CREATE|O_WRONLY without
	// truncation never modified an existing file or its mtime either).
	f, err := l.m.root.OpenFile(l.rel(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			slog.Info("filebrowse: touch (already present)", "path", l.abs)
			return nil
		}
		return err
	}
	if closeErr := f.Close(); closeErr != nil {
		return closeErr
	}
	slog.Info("filebrowse: touch", "path", l.abs)
	return nil
}

func actionDelete(_ context.Context, w http.ResponseWriter, _ fileAction, l loc, _ *Handler) error {
	// Refuse to delete a granted root itself. Everything INSIDE a
	// mount is deletable (the mount boundary replaces the old
	// segment-depth heuristic); users who genuinely want to nuke a
	// whole mount's contents can select-all or use the shell.
	if l.isMountPoint() {
		return refuseMountPoint(w, "delete", l)
	}
	// Layered guard: the mount-point check stops `/config` but would
	// let `/config/chats` through because IsSensitive only matches the
	// files inside. The isProtectedDir helper closes that gap by
	// blocking the container directories of every sensitive path too.
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: delete blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to delete protected directory")
		return errHandled
	}
	if err := l.m.root.RemoveAll(l.rel()); err != nil {
		return err
	}
	slog.Info("filebrowse: delete", "path", l.abs)
	return nil
}

func actionRename(_ context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error {
	// Source-side guards. Renaming a granted root would shadow the
	// mount; renaming a protected container (IsSensitive on the source
	// leaves /config/chats — no trailing slash — through because the
	// sensitive prefix is /config/chats/) could move the entire chat
	// store onto a fresh name and orphan the server's view of its
	// state. rename/move share the same destructive semantics and need
	// the same guards.
	if l.isMountPoint() {
		return refuseMountPoint(w, "rename", l)
	}
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: rename blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to rename protected directory")
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
		httpreply.BadRequest(w, "invalid name")
		return errHandled
	}
	dest := filepath.Join(filepath.Dir(l.abs), name)
	// Route the destination through resolvePath so the allow-list +
	// real-path (EvalSymlinks) checks fire against the rename target
	// the same way copy/move enforce them via resolveCopyMoveDest.
	destLoc, err := h.resolvePath(dest)
	if err != nil {
		slog.Warn("filebrowse: rename dest rejected",
			"from", l.abs, "to", dest, "reason", err.Error())
		httpreply.Forbidden(w, err.Error())
		return errHandled
	}
	// Paranoia: confirm the resolved destination is still a direct
	// child of the original parent (defense in depth against
	// separator surprises on non-Linux filesystems). Same parent
	// implies same mount, so the source root handle covers both ends.
	if filepath.Dir(destLoc.abs) != filepath.Dir(l.abs) {
		httpreply.Forbidden(w, "rename escapes parent directory")
		return errHandled
	}
	// Sensitive-path check on the DESTINATION. Without this the
	// touch→write→rename sequence trivially overwrites sensitive
	// files because rename targets a directory that's already passed
	// the lexical guard. isProtectedDir layers on top so a rename
	// that would land a decoy directory at exactly /config/chats (or
	// any other bare-directory sensitive prefix name) on cold boot
	// is also refused; a destination naming a granted root is the
	// mount-shadowing variant of the same attack.
	if IsSensitive(destLoc.abs) || isProtectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: rename blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "rename target is protected")
		return errHandled
	}
	if err := l.m.root.Rename(l.rel(), l.relOf(destLoc.abs)); err != nil {
		return err
	}
	slog.Info("filebrowse: rename", "from", l.abs, "to", destLoc.abs)
	return nil
}

func actionCopy(ctx context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error {
	destLoc, err := resolveCopyMoveDest(w, body, h)
	if err != nil {
		return err
	}
	// Copy is non-destructive on the source (unlike rename/move),
	// so only the destination needs the protected-dir / sensitive-
	// path gate. Keeps the per-action pattern uniform with rename
	// and move. Cross-mount copies are fine: the stream reads from
	// the source mount's root and writes through the destination's.
	if IsSensitive(destLoc.abs) || isProtectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: copy blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "copy target is protected")
		return errHandled
	}
	n, scErr := streamCopy(ctx, l, destLoc, maxCopySize)
	if scErr != nil {
		if errors.Is(scErr, errOversize) {
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("source file too large to copy"))
			return errHandled
		}
		return scErr
	}
	slog.Info("filebrowse: copy", "from", l.abs, "to", destLoc.abs, "bytes", n)
	return nil
}

// errOversize is returned by streamCopy when the source exceeds the
// size cap, either detected via Stat or via the LimitReader tail guard.
var errOversize = errors.New("source file too large")

// streamCopy streams the file at src into dest atomically via
// write-to-temp-then-rename, kernel-confined to the destination mount. The copy
// is capped at sizeCap bytes; exceeding it returns errOversize. The context
// allows callers to cancel mid-stream (e.g. on client disconnect).
//
// The temp used to be created with os.CreateTemp on the destination's ABSOLUTE
// parent, which is an ambient path — the one write in this handler that did not
// go through a root. That was an escape, not an inconsistency: a destination
// parent replaced by a symlink pointing out of every granted mount after
// resolvePath accepted it received the source file's bytes, which turns the copy
// action into an exfiltration primitive. atomicfile.WriteReaderInRoot stages the
// temp INSIDE the root, so the whole sequence is confined; it is the same call
// writeOneUpload already makes for the identical job.
func streamCopy(ctx context.Context, src, dest loc, sizeCap int64) (int64, error) {
	in, err := src.m.root.Open(src.rel())
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	// Stat the source so we can reject oversize copies before
	// allocating the destination and burning IO bandwidth.
	info, statErr := in.Stat()
	if statErr != nil {
		slog.Debug("filebrowse: copy source stat failed, relying on the write cap",
			"path", src.abs, "error", statErr)
	} else if info.Size() > sizeCap {
		return 0, errOversize
	}

	// 0o600 preserves the mode the deleted os.CreateTemp produced, so a copied
	// file lands with exactly the bits it did before. WithMaxBytes REJECTS an
	// over-cap source (a sparse file, or one growing under the copy) where the
	// old io.LimitReader tail guard had to detect it after the fact.
	cr := &countingReader{r: &ctxReader{ctx: ctx, r: in}}
	if _, wErr := atomicfile.WriteReaderInRoot(ctx, dest.m.root, dest.rel(), cr,
		atomicfile.WithMode(0o600), atomicfile.WithMaxBytes(sizeCap)); wErr != nil {
		if errors.Is(wErr, atomicfile.ErrFileTooLarge) {
			slog.Warn("filebrowse: copy source exceeded cap after stat",
				"path", src.abs, "copied_bytes", cr.n)
			return 0, errOversize
		}
		return 0, wErr
	}
	return cr.n, nil
}

func actionMove(_ context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error {
	// Source-side guards mirror actionRename: moving a granted root
	// (or /config/chats to /workspace/stolen-chats) detaches state
	// from its expected location. Copy is non-destructive so it skips
	// the source check.
	if l.isMountPoint() {
		return refuseMountPoint(w, "move", l)
	}
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: move blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to move protected directory")
		return errHandled
	}
	destLoc, err := resolveCopyMoveDest(w, body, h)
	if err != nil {
		return err
	}
	// Destination guard mirrors actionRename: sensitive files,
	// protected directories, and granted roots are off-limits.
	if IsSensitive(destLoc.abs) || isProtectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: move blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "move target is protected")
		return errHandled
	}
	// A rename cannot cross os.Root handles. Moves across granted
	// roots were already broken in practice (the mounts are separate
	// volumes in the shipped container, so rename returned EXDEV);
	// surface the honest, actionable error instead.
	if destLoc.m != l.m {
		httpreply.BadRequest(w, "cannot move across granted roots; use copy")
		return errHandled
	}
	if err := l.m.root.Rename(l.rel(), destLoc.rel()); err != nil {
		return err
	}
	slog.Info("filebrowse: move", "from", l.abs, "to", destLoc.abs)
	return nil
}

// resolveCopyMoveDest validates + resolves the `dest` field common to
// copy and move. Returns errHandled when it wrote an error response.
func resolveCopyMoveDest(w http.ResponseWriter, body fileAction, h *Handler) (loc, error) {
	if body.Dest == "" {
		httpreply.BadRequest(w, "missing dest")
		return loc{}, errHandled
	}
	destLoc, err := h.resolvePath(body.Dest)
	if err != nil {
		slog.Warn("filebrowse: dest path rejected",
			"dest", body.Dest, "reason", err.Error())
		httpreply.Forbidden(w, err.Error())
		return loc{}, errHandled
	}
	return destLoc, nil
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
