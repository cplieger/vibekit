// Repo discovery logic shared by handleRepos and handleStatusAll.

package git

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/fileutil"
	"golang.org/x/sync/errgroup"
)

// maxRepoEntries caps the number of top-level directory entries scanned
// for git repos. Prevents pathological workspaces from blocking the
// handler indefinitely.
const maxRepoEntries = 1024

// repoEntry is a discovered git repository with its name and absolute path.
type repoEntry struct {
	Name string
	Dir  string
}

// discoverRepos scans workDir for git repositories. Returns "." first
// if workDir itself is a repo, then all immediate subdirectories that
// are repos (sorted by name). Caps the scan at maxRepoEntries.
func discoverRepos(ctx context.Context, workDir string) []repoEntry {
	var repos []repoEntry
	if fileutil.IsGitRepo(ctx, workDir) {
		repos = append(repos, repoEntry{Name: ".", Dir: workDir})
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		slog.Debug("git repos: read workDir failed", "path", workDir, "error", err)
		return repos
	}
	if len(entries) > maxRepoEntries {
		slog.Warn("git repos: workDir entry count exceeds cap",
			"path", workDir, "count", len(entries), "cap", maxRepoEntries)
		entries = entries[:maxRepoEntries]
	}
	var (
		mu    sync.Mutex
		found []repoEntry
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, e := range entries {
		// Skip only ".git" itself — a dot-NAMED repo (".github", ".kiro")
		// is a legitimate clone target; hiding every dot-dir made such a
		// clone succeed on disk yet stay invisible to /api/git/repos and
		// status-all, so the Sources row kept offering Clone (which then
		// failed on the non-empty dir). Hidden non-repo dirs (.cache,
		// .venv) cost one IsGitRepo stat and are skipped by its result.
		if !e.IsDir() || e.Name() == ".git" {
			continue
		}
		name := e.Name()
		dir := filepath.Join(workDir, name)
		g.Go(func() error {
			if fileutil.IsGitRepo(gctx, dir) {
				mu.Lock()
				found = append(found, repoEntry{Name: name, Dir: dir})
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	slices.SortFunc(found, func(a, b repoEntry) int { return cmp.Compare(a.Name, b.Name) })
	repos = append(repos, found...)
	return repos
}

// cachedDiscoverRepos wraps discoverRepos in a singleflight so
// concurrent callers (multi-tab, SSE reconnect) share one scan.
func (h *Handler) cachedDiscoverRepos(ctx context.Context) []repoEntry {
	v, _, _ := h.repoFlight.Do("discover", func() (any, error) {
		return discoverRepos(ctx, h.workDir), nil
	})
	r, _ := v.([]repoEntry)
	return r
}

// ownerOf resolves which discovered repository owns a WORKSPACE-relative
// path, returning the repo name and the path rewritten repo-relative.
// ok is false when no repo owns it.
//
// This lives on the server because the server owns the repo inventory. The
// client cannot do the split without a second copy of this rule, and the one
// place it tried (git-status-store.ts, which composed "<repoName>/<relPath>"
// keys and then looked them up with absolute paths) got it wrong and silently
// showed no status at all. One rule, one owner.
//
// Longest name first, so a repo nested inside another wins over its ancestor.
// The workspace-root repo (".") owns everything no subdirectory repo claims,
// which reproduces the previous default without making it the only answer.
func (h *Handler) ownerOf(ctx context.Context, relPath string) (repo, inRepo string, ok bool) {
	repos := h.cachedDiscoverRepos(ctx)
	best := -1
	for _, e := range repos {
		if e.Name == "." {
			if best < 0 {
				repo, inRepo, ok, best = ".", relPath, true, 0
			}
			continue
		}
		if !pathinside.Root(e.Name).Contains(relPath) || len(e.Name) <= best {
			continue
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(relPath, e.Name), "/")
		if rest == "" {
			// The repo DIRECTORY itself, not a file in it.
			continue
		}
		repo, inRepo, ok, best = e.Name, rest, true, len(e.Name)
	}
	return repo, inRepo, ok
}
