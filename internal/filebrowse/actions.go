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
	"syscall"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
)

// --- /api/files/action (POST: mkdir, touch, delete, rename, copy, move) ---

type fileAction struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	// `dest` and `name` are optional at the wire level; enforced per-action
	// in the handlers (resolveCopyMoveDest rejects empty Dest for copy/move,
	// actionRename rejects empty Name).
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
			"action", logsafe.Field(body.Action), "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
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
	// guards: a cold-boot mkdir on a sensitive dir must not pre-empt it.
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: mkdir blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to mkdir protected directory")
		return errHandled
	}
	// A granted root always exists; "creating" it is a no-op or an attempt
	// to shadow a mount. Refuse, matching actionDelete.
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
	// Mirror of actionMkdir; also checks IsSensitive so creating an
	// exact-match sensitive file is refused before it hits the filesystem.
	if IsSensitive(l.abs) || isProtectedDir(l.abs) {
		slog.Warn("filebrowse: touch blocked on protected path", "path", l.abs)
		httpreply.Forbidden(w, "refusing to touch protected path")
		return errHandled
	}
	if l.isMountPoint() {
		return refuseMountPoint(w, "touch", l)
	}
	// O_EXCL rather than syscall.O_NOFOLLOW, which is INERT here:
	// os.Root.OpenFile ORs O_NOFOLLOW in itself and re-resolves the link on
	// the resulting ELOOP (go1.27.0, src/os/root_unix.go:85-101), so a
	// caller-supplied one is silently ignored. O_EXCL is a refusal the
	// kernel does honour: anything already at the name — including a
	// symlink planted after resolvePath accepted it — makes the create
	// fail instead of opening whatever it points at. An existing entry is
	// touch's no-op case, so EEXIST is success.
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
	// Refuse to delete a granted root itself; everything INSIDE a mount is
	// deletable.
	if l.isMountPoint() {
		return refuseMountPoint(w, "delete", l)
	}
	// Layered guard: the mount-point check stops `/config` but would let
	// `/config/chats` through, since IsSensitive only matches the files
	// inside. isProtectedDir closes that gap.
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: delete blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to delete protected directory")
		return errHandled
	}
	// The mount's os.Root confines this unlink but does not PIN it: it
	// deliberately follows an in-root symlink, so a multi-component rel can
	// resolve to a different file than the one isProtectedDir judged —
	// reachable through the sensitive-path check because that check is
	// exact-prefix over the resolved path. OpenParentInRoot descends
	// component by component, Lstat-ing each one and refusing a symlink
	// rather than following it, so naming only the final element through
	// the pinned parent removes every ancestor from the unlink's path.
	//
	// The parent's own RemoveAll, never atomicfile.RemoveFileInRoot: that
	// refuses anything non-regular with ErrNotRegular, which would make a
	// symlinked entry undeletable from the browser.
	parent, base, err := atomicfile.OpenParentInRoot(l.m.root, l.rel())
	if err != nil {
		// os.Root.RemoveAll reports an already-absent path as success; a
		// parent directory that is gone is the same answer to the caller.
		// Only ErrNotExist: a component refused for being a symlink or a
		// non-directory is a real failure and must surface.
		if errors.Is(err, fs.ErrNotExist) {
			slog.Info("filebrowse: delete (already absent)", "path", l.abs)
			return nil
		}
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := parent.RemoveAll(base); err != nil {
		return err
	}
	slog.Info("filebrowse: delete", "path", l.abs)
	return nil
}

// isSingleSegmentName reports whether name is one non-traversal path
// component, safe to Join onto a parent directory. filepath.Base alone
// isn't enough: a bare ".." passes Base untouched and Join then silently
// escapes to the parent directory.
func isSingleSegmentName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsRune(name, '/') && !strings.ContainsRune(name, '\\') &&
		!strings.ContainsRune(name, 0)
}

func actionRename(_ context.Context, w http.ResponseWriter, body fileAction, l loc, h *Handler) error {
	// Source-side guards. Renaming a granted root would shadow the mount;
	// renaming a protected container could orphan the server's view of
	// its state.
	if l.isMountPoint() {
		return refuseMountPoint(w, "rename", l)
	}
	if isProtectedDir(l.abs) {
		slog.Warn("filebrowse: rename blocked on protected dir", "path", l.abs)
		httpreply.Forbidden(w, "refusing to rename protected directory")
		return errHandled
	}
	if !isSingleSegmentName(body.Name) {
		httpreply.BadRequest(w, "invalid name")
		return errHandled
	}
	dest := filepath.Join(filepath.Dir(l.abs), body.Name)
	// Route the destination through resolvePath so the allow-list +
	// real-path checks fire against the rename target the same way
	// copy/move enforce them via resolveCopyMoveDest.
	destLoc, err := h.resolvePath(dest)
	if err != nil {
		slog.Warn("filebrowse: rename dest rejected",
			"from", l.abs, "to", dest, "reason", err.Error())
		httpreply.Forbidden(w, err.Error())
		return errHandled
	}
	// Confirm the resolved destination is still a direct child of the
	// original parent (defense in depth against separator surprises on
	// non-Linux filesystems). Same parent implies same mount.
	if filepath.Dir(destLoc.abs) != filepath.Dir(l.abs) {
		httpreply.Forbidden(w, "rename escapes parent directory")
		return errHandled
	}
	// Sensitive-path check on the DESTINATION: without this a
	// touch→write→rename sequence could overwrite sensitive files.
	// isProtectedDir layers on top for a decoy directory landing at a
	// bare-directory sensitive prefix name.
	if IsSensitive(destLoc.abs) || isProtectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: rename blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "rename target is protected")
		return errHandled
	}
	// One pinned parent addresses both ends here, since the same-parent
	// assertion above already established they share one directory. See
	// actionDelete for why the mount's os.Root is not enough on its own.
	parent, base, err := atomicfile.OpenParentInRoot(l.m.root, l.rel())
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Rename(base, filepath.Base(destLoc.abs)); err != nil {
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
	// Copy is non-destructive on the source, so only the destination needs
	// the protected-dir / sensitive-path gate. Cross-mount copies are fine.
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
// write-to-temp-then-rename, kernel-confined to the destination mount. The
// copy is capped at sizeCap bytes; exceeding it returns errOversize. The
// context allows callers to cancel mid-stream.
//
// The temp used to be created with os.CreateTemp on the destination's
// ABSOLUTE parent — an ambient path that could receive the source file's
// bytes if the parent was replaced by a symlink pointing outside every
// granted mount after resolvePath accepted it. atomicfile.WriteReaderInRoot
// stages the temp INSIDE the root, closing that.
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

	// 0o600 preserves the mode the deleted os.CreateTemp produced. WithMaxBytes
	// REJECTS an over-cap source rather than the old io.LimitReader tail
	// guard detecting it after the fact.
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
	// Source-side guards mirror actionRename. Copy is non-destructive so it
	// skips the source check.
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
	// Destination guard mirrors actionRename.
	if IsSensitive(destLoc.abs) || isProtectedDir(destLoc.abs) || destLoc.isMountPoint() {
		slog.Warn("filebrowse: move blocked on sensitive dest",
			"from", l.abs, "to", destLoc.abs)
		httpreply.Forbidden(w, "move target is protected")
		return errHandled
	}
	// A rename cannot cross os.Root handles; surface the actionable error.
	if destLoc.m != l.m {
		httpreply.BadRequest(w, "cannot move across granted roots; use copy")
		return errHandled
	}
	if err := renameAcrossPinnedParents(l, destLoc); err != nil {
		return err
	}
	slog.Info("filebrowse: move", "from", l.abs, "to", destLoc.abs)
	return nil
}

// renameAcrossPinnedParents renames src to dest with BOTH parent directories
// pinned by atomicfile.OpenParentInRoot, so no ancestor component of either end
// can redirect the rename at another file inside the mount. actionDelete carries
// the reasoning for why the mount's os.Root is not enough on its own.
//
// renameat(2) rather than os.Root.Rename, because a move's two ends can be in
// different directories and os.Root.Rename resolves both names inside ONE root:
// a pinned parent's root is that single directory, so the other end is only
// reachable through a ".." the root correctly refuses. renameat is the syscall
// os.Root.Rename itself issues; what changes is that each name is a single final
// element relative to a descended, identity-confirmed directory rather than a
// multi-component path re-resolved at operation time. The caller has already
// established that src and dest share a mount.
func renameAcrossPinnedParents(src, dest loc) error {
	srcParent, srcBase, err := atomicfile.OpenParentInRoot(src.m.root, src.rel())
	if err != nil {
		return err
	}
	defer func() { _ = srcParent.Close() }()
	destParent, destBase, err := atomicfile.OpenParentInRoot(dest.m.root, dest.rel())
	if err != nil {
		return err
	}
	defer func() { _ = destParent.Close() }()
	// The descriptors are what renameat addresses; the pinned roots hold
	// them open so neither directory can be swapped between the descent
	// that confirmed its identity and the rename itself.
	srcDir, err := srcParent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = srcDir.Close() }()
	destDir, err := destParent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = destDir.Close() }()
	return syscall.Renameat(int(srcDir.Fd()), srcBase, int(destDir.Fd()), destBase)
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
// ctx.Err() so a cancelled/disconnected request aborts on the next chunk
// boundary.
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
