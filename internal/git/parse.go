package git

// Pure parsing and path-validation helpers. Extracted from helpers.go
// and handlers.go to separate testable-without-HTTP domain logic from
// HTTP scaffolding and subprocess delegates.

import (
	"context"
	"github.com/cplieger/vibekit/internal/gitexec"
	"log/slog"
	"strings"
)

// --- git status parsing ---

// gitFile represents a single entry from `git status --porcelain`.
type gitFile struct {
	Path    string `json:"path"`
	Display string `json:"display"`
	Status  string `json:"status"`
	Staged  bool   `json:"staged"`
}

var statusLabels = map[byte]string{
	'M': "Modified",
	'A': "Added",
	'D': "Deleted",
	'R': "Renamed",
	'C': "Copied",
	'U': "Unmerged",
	'?': "Untracked",
}

func statusLabel(c byte) string {
	if label, ok := statusLabels[c]; ok {
		return label
	}
	// Reachable only if git evolves the porcelain-v1 format; returning
	// a fixed label avoids leaking control bytes into the UI.
	return "Unknown"
}

// parseGitStatusOutput parses `git status --porcelain -uall` output into
// gitFile entries. Exposed as a pure function so the parsing logic can be
// tested without invoking git.
func parseGitStatusOutput(raw []byte) []gitFile {
	out := strings.TrimRight(string(raw), " \t\r\n")
	if out == "" {
		return nil
	}
	var files []gitFile
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) < 3 {
			continue
		}
		x, y, path := line[0], line[1], line[3:]
		if strings.HasSuffix(path, "/") {
			continue
		}
		if idx := strings.Index(path, " -> "); idx != -1 {
			path = path[idx+4:]
		}
		f := gitFile{Path: path}
		switch {
		case x == '?' && y == '?':
			f.Status = "?"
			f.Display = "Untracked"
		case x != ' ' && x != '?':
			f.Status = string(x)
			f.Staged = true
			f.Display = statusLabel(x)
		default:
			f.Status = string(y)
			f.Display = statusLabel(y)
		}
		files = append(files, f)
		if x != ' ' && x != '?' && y != ' ' && y != '?' {
			files = append(files, gitFile{
				Path: path, Status: string(y), Staged: false,
				Display: statusLabel(y),
			})
		}
	}
	return files
}

// --- git status helpers ---

// splitTrackedUntracked partitions the given file list into tracked
// (checkout --) and untracked (clean -fd) buckets based on the current
// git status.
func splitTrackedUntracked(ctx context.Context, dir string, files []string) (tracked, untracked []string) {
	raw, err := gitExec(ctx, dir, "status", "--porcelain", "-uall").CombinedOutput()
	if err != nil {
		slog.Warn("git status failed during discard", "repo", dir, "error", err, "out", gitexec.ScrubAuth(string(raw)))
		return nil, nil
	}
	wanted := make(map[string]bool, len(files))
	for _, f := range files {
		wanted[f] = true
	}
	seen := make(map[string]bool, len(files))
	for _, f := range parseGitStatusOutput(raw) {
		if !wanted[f.Path] || seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		if f.Status == "?" {
			untracked = append(untracked, f.Path)
		} else {
			tracked = append(tracked, f.Path)
		}
	}
	return tracked, untracked
}
