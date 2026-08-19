// Clone and re-clone handlers extracted from handlers_repo.go.
// These workspace-mutating operations share the URL scheme allowlist
// and are grouped for focused security review.

package git

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
)

func (h *Handler) handleClone(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !decodePostBody(w, r, &body, "url required") {
		return
	}
	if body.URL == "" {
		httpreply.BadRequest(w, "url required")
		return
	}
	if !isAllowedRemoteScheme(body.URL) {
		slog.Warn("git clone: invalid scheme rejected", "url", scrubAuth(body.URL))
		httpreply.BadRequest(w, "only https:// and git@ URLs allowed")
		return
	}
	// Defense in depth against git argument-injection CVEs (1000117,
	// 11235, and future variants): pass `--` so git treats the URL
	// strictly as a positional argument even if parsing quirks would
	// otherwise interpret leading dashes as flags. The explicit scheme
	// prefix above already blocks `--flag=...`, but `--` is cheap and
	// makes the guarantee lexical rather than prefix-based.
	slog.Info("git clone", "url", scrubAuth(body.URL))
	cloneCtx, cancel := context.WithTimeout(r.Context(), h.timeouts.Clone)
	defer cancel()
	cmd := gitExec(cloneCtx, h.workDir, "clone", "--", body.URL)
	out, err := cmd.CombinedOutput()
	writeCmdResult(w, strings.TrimSpace(string(out)), err)
}

// handleReclone deletes the local copy of `repo` and re-clones it from its
// previously-configured origin URL. One-click recovery for divergent
// branches, detached HEAD, merge conflicts, or any other local-only
// mess the user doesn't want to fix by hand. If `repo` is empty or ".",
// we reject it: the workspace root isn't necessarily a git repo and
// accidentally nuking it would be a bad day.
//
// Caveat: this is a destructive operation with an atomicity gap — if
// the clone fails after os.RemoveAll, the user's repo is gone. A
// rename-then-clone-then-delete variant that restores the old tree
// on clone failure is tracked in .review/TODO.md.
func (h *Handler) handleReclone(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "repo required") {
		return
	}
	if body.Repo == "" || body.Repo == "." {
		httpreply.BadRequest(w, "re-clone requires a named repo (cannot target workspace root)")
		return
	}
	// The resolved variant, matching handleRemove: this handler DELETES, so it
	// needs the intermediate-symlink check the lexical resolver cannot make (see
	// repoDirForDelete). It answers "" for a path it will not vouch for.
	dir := h.repoDirForDelete(body.Repo)
	if dir == "" {
		httpreply.BadRequest(w, "that repo path is not inside the workspace")
		return
	}
	if dir == h.workDir {
		httpreply.BadRequest(w, "cannot re-clone workspace root")
		return
	}
	if !IsRepo(r.Context(), dir) {
		httpreply.BadRequest(w, msgNotAGitRepo)
		return
	}
	remote, err := gitCmd(r.Context(), dir, subRemote, "get-url", "origin")
	if err != nil || remote == "" {
		slog.Warn("git reclone: origin lookup failed", "repo", body.Repo, "error", err)
		httpreply.WriteJSON(w, httpreply.ErrorJSON("no origin remote"))
		return
	}
	// Defense-in-depth: the origin URL came from git config and could
	// have been set to a non-standard scheme by a prior clone (shared
	// workspace, compromised upstream hook, etc.). Mirror handleClone's
	// scheme allowlist so a re-clone can't silently switch to
	// `file://`, `ext::`, or another transport family. Do this BEFORE
	// os.RemoveAll so a rejected reclone leaves the working tree
	// intact.
	if !isAllowedRemoteScheme(remote) {
		httpreply.WriteJSON(w, httpreply.ErrorJSON("origin has unsupported scheme for re-clone"))
		return
	}
	slog.Info("git reclone starting", "repo", body.Repo)
	// Nuke and reclone in place. We delete after resolving the URL so a
	// partial delete doesn't strand the repo in an unreclonable state.
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		slog.Error("git reclone: remove failed", "repo", body.Repo, "error", rmErr)
		httpreply.WriteJSON(w, httpreply.ErrorJSON("remove failed"))
		return
	}
	// `--` barrier: the origin URL came from git config, but a prior
	// malicious clone could have stored a `-flag` there; treating it
	// strictly as a positional argument neutralises that attack.
	cmd := gitExec(r.Context(), h.workDir, "clone", "--", remote, filepath.Base(dir))
	out, cErr := cmd.CombinedOutput()
	if cErr != nil {
		slog.Error("git reclone: clone failed", "repo", body.Repo, "error", cErr, "out", scrubAuth(strings.TrimSpace(string(out))))
	} else {
		slog.Info("git reclone completed", "repo", body.Repo)
	}
	writeCmdResult(w, strings.TrimSpace(string(out)), cErr)
}

// isAllowedRemoteScheme reports whether url uses a transport scheme
// permitted for clone and re-clone operations: https:// or scp-style
// (git@). Restricted to those two to prevent the UI from accidentally
// driving insecure transports (http://) or remote helpers (ext::).
func isAllowedRemoteScheme(url string) bool {
	return strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "git@")
}
