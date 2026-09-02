// Clone and re-clone handlers extracted from handlers_repo.go.
// These workspace-mutating operations share the URL scheme allowlist
// and are grouped for focused security review.

package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/workspace"
	"github.com/cplieger/webhttp/v2"
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
	out, err := h.clone(cloneCtx, body.URL)
	if err != nil {
		slog.Error("git clone: failed", "url", scrubAuth(body.URL), "error", err, "out", scrubAuth(out))
	}
	writeCmdResult(w, out, err)
}

// clone runs the clone for handleClone, choosing between a plain
// `git clone` and adoptDestination by what already sits at the
// destination — and on failure restores the destination to that
// pre-clone state. Restoring is load-bearing, not tidiness: a clone
// killed mid-transfer (the client aborted, the budget expired) leaves
// a directory holding a commitless .git skeleton, which IsRepo counts
// as a repository — so discovery lists it as cloned, the Sources row
// offers "delete local copy", and a re-clone is refused as "already
// exists", all over a corpse. Measured live on a 511 MB clone.
func (h *Handler) clone(ctx context.Context, remote string) (string, error) {
	name := cloneDirName(remote)
	if name == "" {
		// The destination is not predictable from this URL, so let git
		// derive it and report whatever it finds. No cleanup either: the
		// destination is unknown here for the same reason.
		return gitCmd(ctx, h.workDir, "clone", "--", remote)
	}
	dir := filepath.Join(h.workDir, name)
	if dir == h.workDir {
		// Unreachable through cloneDirName, which refuses "." and "..".
		// Kept so a future change there cannot turn the workspace root
		// into an adoption target.
		return gitCmd(ctx, h.workDir, "clone", "--", remote)
	}
	switch inspectCloneDest(ctx, dir) {
	case destRepo:
		return "", fmt.Errorf("%s already exists and is a git repository; delete it or use re-clone to replace it", name)
	case destOccupied:
		slog.Info("git clone: adopting an existing directory", "dir", name)
		out, err := adoptDestination(ctx, dir, remote)
		if err != nil {
			// The directory held the user's content before adoption; only the
			// .git that `git init` created is ours to take back.
			h.discardCloneDebris(dir, name, false)
		}
		return out, err
	case destEmpty:
		out, err := gitCmd(ctx, h.workDir, "clone", "--", remote)
		if err != nil {
			// The user created this directory, so it stays; everything git
			// put inside a previously-empty one is debris.
			h.discardCloneDebris(dir, name, true)
		}
		return out, err
	}
	// destAbsent: git creates the directory, so a failure removes it whole.
	// A state added to destState later lands here too, which is the safe
	// default for the clone itself; its cleanup takes the whole-directory
	// form because anything at the name was git's creation.
	out, err := gitCmd(ctx, h.workDir, "clone", "--", remote)
	if err != nil {
		if rmErr := h.removeRepoDir(dir); rmErr != nil {
			slog.Warn("git clone: failed to remove the partial destination", "dir", name, "error", rmErr)
		}
	}
	return out, err
}

// discardCloneDebris removes what a failed clone or adoption left inside a
// destination that existed BEFORE the operation. sweepAll removes every
// entry (a previously-empty directory: whatever is inside is git's);
// otherwise only .git goes (an adopted directory: the user's content
// predates the operation and only `git init`'s tree is ours).
//
// git cleans its own destination after an ordinary fatal error, so this
// usually finds nothing; what it exists for is the SIGKILL case (a
// cancelled request context kills the subprocess), where git never gets
// the chance. Best-effort: the clone's own error is the answer the caller
// reports, so a cleanup failure warns rather than masking it.
func (h *Handler) discardCloneDebris(dir, name string, sweepAll bool) {
	root, err := os.OpenRoot(h.workDir)
	if err != nil {
		slog.Warn("git clone: cleanup could not open the workspace root", "dir", name, "error", err)
		return
	}
	defer func() { _ = root.Close() }()
	rel, err := workspace.RelPath(h.workDir, dir)
	if err != nil {
		slog.Warn("git clone: cleanup refused the destination path", "dir", name, "error", err)
		return
	}
	// Opening the destination through the root refuses a symlink component,
	// so the removals below cannot be redirected outside the workspace.
	sub, err := root.OpenRoot(rel)
	if err != nil {
		// Gone already (git cleaned up after itself) is the goal state.
		return
	}
	defer func() { _ = sub.Close() }()
	if !sweepAll {
		if err := sub.RemoveAll(".git"); err != nil {
			slog.Warn("git clone: failed to remove the adopted .git", "dir", name, "error", err)
		}
		return
	}
	f, err := sub.Open(".")
	if err != nil {
		slog.Warn("git clone: cleanup could not list the destination", "dir", name, "error", err)
		return
	}
	entries, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		slog.Warn("git clone: cleanup could not list the destination", "dir", name, "error", err)
		return
	}
	for _, entry := range entries {
		if err := sub.RemoveAll(entry); err != nil {
			slog.Warn("git clone: failed to remove clone debris", "dir", name, "entry", entry, "error", err)
		}
	}
}

// destState describes what occupies a clone destination.
type destState int

const (
	// destAbsent is nothing at the name, or something that is not a
	// directory. Both are git's to report.
	destAbsent destState = iota
	// destEmpty is an existing directory with no entries.
	destEmpty
	// destRepo is an existing git repository.
	destRepo
	// destOccupied is an existing directory holding other content.
	destOccupied
)

// inspectCloneDest reports what sits at dir. A symlink reads as
// destAbsent deliberately: adopting one would write through it to a
// target the caller never named, so it stays git's error to report.
func inspectCloneDest(ctx context.Context, dir string) destState {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return destAbsent
	}
	if IsRepo(ctx, dir) {
		return destRepo
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		// An unreadable directory falls through to plain clone, whose
		// failure names the real reason.
		return destEmpty
	}
	return destOccupied
}

// adoptDestination clones remote into dir, an existing directory that
// already holds content. Plain `git clone` refuses that outright
// ("destination path ... already exists and is not an empty directory"),
// which made a whole class of repository unclonable here: activating code
// intelligence writes <workspace>/.kiro/settings/lsp.json, so the
// destination for a repo NAMED .kiro exists before anyone asks to clone
// it, and every attempt failed in a few milliseconds.
//
// The sequence is git's own way to populate a directory that already has
// content, and it keeps the property that made plain clone refuse in the
// first place: the final checkout REFUSES to overwrite an untracked file,
// so pre-existing content is either left untouched or the operation fails
// with git naming the colliding paths. Nothing here deletes or overwrites.
func adoptDestination(ctx context.Context, dir, remote string) (string, error) {
	var combined strings.Builder
	run := func(args ...string) (string, error) {
		out, err := gitCmd(ctx, dir, args...)
		if out != "" {
			combined.WriteString(out)
			combined.WriteString("\n")
		}
		return out, err
	}
	report := func() string { return strings.TrimSpace(combined.String()) }

	for _, args := range [][]string{
		{"init", "--quiet"},
		// `--` barrier for the same reason handleClone passes one.
		{subRemote, "add", remoteOrigin, "--", remote},
		{subFetch, "--quiet", "--no-tags", remoteOrigin},
	} {
		if _, err := run(args...); err != nil {
			return report(), err
		}
	}
	// origin/HEAD names the branch a plain clone would have checked out.
	// A remote with no commits cannot answer, and that is not a failure:
	// the repository is initialised and tracked, there is simply nothing
	// to check out yet.
	if _, err := run(subRemote, "set-head", remoteOrigin, "--auto"); err != nil {
		return report(), nil
	}
	head, err := run("symbolic-ref", "--short", "refs/remotes/"+remoteOrigin+"/HEAD")
	if err != nil {
		return report(), nil
	}
	// --short answers "origin/<branch>"; the local branch is what remains.
	branch := strings.TrimPrefix(strings.TrimSpace(head), remoteOrigin+"/")
	if !isValidGitRef(branch) {
		return report(), fmt.Errorf("remote HEAD is not a usable branch name: %q", branch)
	}
	// A tracking branch by DWIM, exactly what clone would have left behind.
	if _, err := run(subCheckout, branch); err != nil {
		return report(), err
	}
	return report(), nil
}

// handleReclone deletes the local copy of `repo` and re-clones it from its
// previously-configured origin URL. One-click recovery for divergent
// branches, detached HEAD, merge conflicts, or any other local-only
// mess the user doesn't want to fix by hand. Rejects `repo` empty or ".":
// the workspace root isn't necessarily a git repo and accidentally
// nuking it would be a bad day.
//
// Caveat: this is a destructive operation with an atomicity gap — if
// the clone fails after os.RemoveAll, the user's repo is gone.
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
	dir := h.repoDir(body.Repo)
	if dir == h.workDir {
		httpreply.BadRequest(w, "cannot re-clone workspace root")
		return
	}
	if !IsRepo(r.Context(), dir) {
		httpreply.BadRequest(w, msgNotAGitRepo)
		return
	}
	remote, err := gitCmd(r.Context(), dir, subRemote, "get-url", remoteOrigin)
	if err != nil || remote == "" {
		slog.Warn("git reclone: origin lookup failed", "repo", body.Repo, "error", err)
		webhttp.WriteJSON(w, httpreply.ErrorJSON("no origin remote"))
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
		webhttp.WriteJSON(w, httpreply.ErrorJSON("origin has unsupported scheme for re-clone"))
		return
	}
	slog.Info("git reclone starting", "repo", body.Repo)
	// Delete after resolving the URL so a partial delete doesn't strand
	// the repo in an unreclonable state. Through the pinned parent, same
	// as handleRemove — see removeRepoDir for why a name is not safe to
	// unlink directly.
	if rmErr := h.removeRepoDir(dir); rmErr != nil {
		if errors.Is(rmErr, ErrUnsafeRepoPath) {
			slog.Warn("git reclone: refused", "repo", body.Repo, "error", rmErr)
			httpreply.BadRequest(w, "that repo path is not inside the workspace")
			return
		}
		slog.Error("git reclone: remove failed", "repo", body.Repo, "error", rmErr)
		webhttp.WriteJSON(w, httpreply.ErrorJSON("remove failed"))
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
