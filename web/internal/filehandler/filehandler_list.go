package filehandler

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"vibekit/internal/api"
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
	// resolvePath("/") returns ("/", nil): filepath.Clean("//") is
	// "/", the top segment splits to "" which is not in the
	// blacklist, and "/" never matches isSensitive. No special case
	// needed.
	resolved := resolveOrForbid(w, reqPath)
	if resolved == "" {
		return
	}
	f, err := h.root.Open(h.relPath(resolved))
	if err != nil {
		if os.IsNotExist(err) {
			api.NotFound(w, "not found")
			return
		}
		slog.Warn("filehandler: readdir failed", "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "read failed"})
		return
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		slog.Warn("filehandler: readdir failed", "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "read failed"})
		return
	}
	files := listEntries(r.Context(), entries, resolved)
	api.WriteJSON(w, map[string]any{
		"path":     reqPath,
		"files":    files,
		"writable": isWritable(resolved),
	})
}

// listEntries filters DirEntries: at root, blacklisted dirs + dotfiles
// are hidden; sensitive paths are hidden everywhere.
func listEntries(ctx context.Context, entries []os.DirEntry, resolved string) []fileEntry {
	isRoot := resolved == "/"
	files := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			break
		}
		name := e.Name()
		if isRoot && (blacklist[name] || name[0] == '.') {
			continue
		}
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
// zero-byte temp file. Failures are logged at Debug (probe couldn't
// even create a file — not actionable) or Warn (probe file leaked —
// operator may want to sweep). The probe prefix is named so a future
// startup sweeper can scan for ".vibekit-probe-*" leftovers.
func isWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".vibekit-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	if closeErr := f.Close(); closeErr != nil {
		slog.Debug("filehandler: probe close failed", "path", name, "error", closeErr)
	}
	if rmErr := os.Remove(name); rmErr != nil {
		slog.Warn("filehandler: probe cleanup failed", "path", name, "error", rmErr)
	}
	return true
}
