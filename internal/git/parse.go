package git

// Pure parsing and path-validation helpers. Extracted from helpers.go
// and handlers.go to separate testable-without-HTTP domain logic from
// HTTP scaffolding and subprocess delegates.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/gitexec"
)

// --- git status parsing ---

// gitFile represents a single entry from `git status --porcelain=v1 -z`.
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

// parseGitStatusOutput parses `git status --porcelain=v1 -z -uall` output
// into gitFile entries. Exposed as a pure function so the parsing logic
// can be tested without invoking git.
//
// The -z (NUL-delimited) format is used deliberately over the default
// newline format: with -z git NEVER quotes paths, whereas the default
// format C-quotes any path containing non-ASCII bytes, spaces-with-
// specials, or control characters and wraps it in double quotes
// (café.txt → "caf\303\251.txt"). Those quoted strings were displayed
// verbatim AND round-tripped into stage/unstage/discard/diff, where
// `git add -- '"caf\303\251.txt"'` matched nothing and the op failed
// end-to-end. Parsing the raw NUL-separated records means every entry's
// Path is the exact on-disk path, so downstream git ops resolve it.
//
// Record grammar (`git status` -z docs): records are separated by NUL,
// each is `XY<space><path>` (a space still separates the 2-char status
// from the path). For rename (R) and copy (C) entries the ` -> ` is
// dropped and the field order is reversed, so the record spans two
// NUL-terminated fields: `XY<space><new-path>` then `<orig-path>`. We
// keep the new path (the entry's current location — what stage/discard/
// diff all need) and consume the trailing orig field so it isn't
// mis-parsed as its own record.
func parseGitStatusOutput(raw []byte) []gitFile {
	records := strings.Split(string(raw), "\x00")
	var files []gitFile
	for i := 0; i < len(records); i++ {
		line := records[i]
		// A valid record is at least "XY P": 2 status bytes, a space,
		// and a >=1-char path. Anything shorter is the trailing empty
		// field after the final NUL, or a consumed orig-path half.
		if len(line) < 4 {
			continue
		}
		x, y, path := line[0], line[1], line[3:]
		if isRenameOrCopy(x, y) {
			// Rename/copy entries carry a second NUL field (the origin
			// path); skip it so it isn't parsed as a standalone record.
			i++
		}
		if strings.HasSuffix(path, "/") {
			continue
		}
		files = appendStatusEntries(files, x, y, path)
	}
	return files
}

// isRenameOrCopy reports whether an XY status pair denotes a rename (R)
// or copy (C). In the -z format such an entry is followed by a second
// NUL-terminated field carrying the origin path.
func isRenameOrCopy(x, y byte) bool {
	return x == 'R' || x == 'C' || y == 'R' || y == 'C'
}

// appendStatusEntries appends the gitFile rows for one porcelain XY
// status pair + path. A path that is BOTH staged (X) and changed in the
// worktree (Y) yields two rows — one staged, one unstaged — so the git
// panel can stage/discard each side independently.
func appendStatusEntries(files []gitFile, x, y byte, path string) []gitFile {
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
	return files
}

// --- git status helpers ---

// splitTrackedUntracked partitions the given file list into tracked
// (checkout --) and untracked (clean -fd) buckets based on the current
// git status.
func splitTrackedUntracked(ctx context.Context, dir string, files []string) (tracked, untracked []string) {
	raw, err := gitExec(ctx, dir, "status", "--porcelain=v1", "-z", "-uall").CombinedOutput()
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
