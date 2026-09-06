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
	"golang.org/x/sync/errgroup"
)

// gitDirName is the entry that marks a working tree: a DIRECTORY in an ordinary
// clone, a FILE holding a `gitdir:` pointer in a linked worktree or submodule.
const gitDirName = ".git"

// IsRepo reports whether dir contains a .git entry, in any of its forms or as a
// symlink to one (os.Stat follows symlinks).
func IsRepo(ctx context.Context, dir string) bool {
	if ctx.Err() != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, gitDirName)) // #nosec G703 -- handlers resolve dir through repoDir, which refuses ".." and absolute paths; reads no content
	return err == nil
}

// maxRepoEntries caps the top-level entries scanned, so a pathological workspace
// cannot block the handler indefinitely.
const maxRepoEntries = 1024

// repoEntry is a discovered git repository with its name and absolute path.
type repoEntry struct {
	Name string
	Dir  string
}

// discoverRepos scans workDir for git repositories: "." first when workDir itself
// is one, then every immediate subdirectory that is, sorted by name.
func discoverRepos(ctx context.Context, workDir string) []repoEntry {
	var repos []repoEntry
	if IsRepo(ctx, workDir) {
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
		// Only ".git" itself: a dot-NAMED repo (".github", ".kiro") is a legitimate
		// clone target, and skipping every dot-dir made such a clone invisible here
		// while the Sources row kept offering Clone into the non-empty directory.
		if !e.IsDir() || e.Name() == ".git" {
			continue
		}
		name := e.Name()
		dir := filepath.Join(workDir, name)
		g.Go(func() error {
			if IsRepo(gctx, dir) {
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

// cachedDiscoverRepos wraps discoverRepos in a singleflight, so concurrent callers
// (multi-tab, SSE reconnect) share one scan.
func (h *Handler) cachedDiscoverRepos(ctx context.Context) []repoEntry {
	v, _, _ := h.repoFlight.Do("discover", func() (any, error) {
		return discoverRepos(ctx, h.workDir), nil
	})
	r, _ := v.([]repoEntry)
	return r
}

// ownerOf resolves which discovered repository owns a WORKSPACE-relative path,
// returning the repo name and the path rewritten repo-relative; ok is false when
// no repo owns it. Longest name first, so a nested repo wins over its ancestor,
// and the workspace-root repo (".") owns whatever no subdirectory repo claims.
//
// Server-side because the server owns the repo inventory: a client-side split
// needs a second copy of this rule, and the one that existed got it wrong and
// showed no status at all.
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
