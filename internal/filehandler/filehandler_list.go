package filehandler

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// --- /api/files (GET directory listing) ---

type fileEntry struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

func (h *Handler) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" || reqPath == "." {
		reqPath = "/"
	}
	// "/" is not a real directory in the allow-list model — it is the
	// synthetic listing of the granted mounts.
	if filepath.Clean("/"+reqPath) == "/" {
		h.listMounts(w)
		return
	}
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return
	}
	f, err := l.m.root.Open(l.rel())
	if err != nil {
		if os.IsNotExist(err) {
			api.NotFound(w, "not found")
			return
		}
		slog.Warn("filehandler: readdir failed", "path", l.abs, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON(errReadFailed))
		return
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		slog.Warn("filehandler: readdir failed", "path", l.abs, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON(errReadFailed))
		return
	}
	files := listEntries(r.Context(), entries, l.abs)
	api.WriteJSON(w, map[string]any{
		respPath:   reqPath,
		"files":    files,
		"writable": h.isWritable(l),
	})
}

// listMounts writes the synthetic root listing: one directory entry
// per granted mount (a nested grant like /data/media renders as the
// slash-joined name "data/media"). The root itself is never writable —
// mounts are boot-time configuration, not filesystem entries.
func (h *Handler) listMounts(w http.ResponseWriter) {
	files := make([]fileEntry, 0, len(h.mounts))
	for i := range h.mounts {
		m := &h.mounts[i]
		e := fileEntry{Name: m.name, IsDir: true, Mode: os.ModeDir.String()}
		if info, err := m.root.Stat("."); err == nil {
			e.Mode = info.Mode().String()
			e.ModTime = info.ModTime().UnixMilli()
		}
		files = append(files, e)
	}
	// mounts are sorted longest-first for prefix matching; the UI wants
	// them alphabetical.
	slices.SortFunc(files, func(a, b fileEntry) int { return strings.Compare(a.Name, b.Name) })
	api.WriteJSON(w, map[string]any{
		respPath:   "/",
		"files":    files,
		"writable": false,
	})
}

// listEntries filters DirEntries: sensitive paths are hidden.
func listEntries(ctx context.Context, entries []os.DirEntry, resolved string) []fileEntry {
	files := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			break
		}
		name := e.Name()
		if isSensitive(filepath.Join(resolved, name)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// Races (file deleted between ReadDir and Info), EACCES
			// on restrictive modes, and transient NFS errors all land
			// here. Debug level so agent-driven directory churn doesn't
			// create noise; operators can flip the level when the
			// "some files missing from UI" report comes in.
			slog.Debug("filehandler: listEntries entry stat failed",
				"dir", resolved, "name", name, "error", err)
			continue
		}
		files = append(files, fileEntry{
			Name:    name,
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().UnixMilli(),
		})
	}
	return files
}

// isWritable probes write access by creating and removing a
// zero-byte probe file THROUGH the mount's kernel-confined root
// handle, so the same os.Root confinement that gates every other file
// operation also applies to the probe. O_EXCL plus a random suffix
// keeps concurrent probes collision-free. Failures are logged at
// Debug (probe couldn't even create a file — not actionable) or Warn
// (probe file leaked — operator may want to sweep). The probe prefix
// is named so a future startup sweeper can scan for ".vibekit-probe-*"
// leftovers.
func (h *Handler) isWritable(l loc) bool {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// crypto/rand cannot fail on supported platforms; treat an
		// impossible failure as "unknown", i.e. not writable.
		return false
	}
	rel := l.relOf(filepath.Join(l.abs, fmt.Sprintf(".vibekit-probe-%x", suffix)))
	f, err := l.m.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	if closeErr := f.Close(); closeErr != nil {
		slog.Debug("filehandler: probe close failed", "path", rel, "error", closeErr)
	}
	if rmErr := l.m.root.Remove(rel); rmErr != nil {
		slog.Warn("filehandler: probe cleanup failed", "path", rel, "error", rmErr)
	}
	return true
}
