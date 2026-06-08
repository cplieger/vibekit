// Repo discovery logic shared by handleRepos and handleStatusAll.

package git

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
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
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
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
