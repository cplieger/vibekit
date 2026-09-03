package git

// The git panel's row shape and the status-letter rules, plus path validation.
// Separated from HTTP scaffolding and subprocess delegates so the domain logic is
// testable without either. The status FORMAT and its one invocation are
// porcelain.go's; this file owns what an entry means for the UI.

import (
	"context"
)

// gitFile represents a single row the git panel renders for one path, built from
// a porcelain status entry (see porcelain.go).
//
// OrigPath is set only on a rename or copy entry, carrying the path the
// content came FROM. Every other field describes the path it is at now.
// It is omitted from the JSON when empty, so an ordinary entry costs no
// wire bytes for it.
type gitFile struct {
	Path     string `json:"path"`
	Display  string `json:"display"`
	Status   string `json:"status"`
	OrigPath string `json:"orig_path,omitempty"`
	Staged   bool   `json:"staged"`
}

// statusLabels covers every status character a porcelain status entry
// emits. 'T' (typechange — a regular file replaced by a symlink or the
// reverse) was missing and therefore rendered as "Unknown": measured on
// git 2.x, `rm f && ln -s /tmp f` reports " T f" unstaged and "T  f"
// staged, so it is an ordinary status a working tree reaches, not a
// hypothetical.
var statusLabels = map[byte]string{
	'M': "Modified",
	'T': "Typechange",
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
	// Reachable only if git evolves the porcelain format; returning a fixed
	// label avoids leaking control bytes into the UI.
	return "Unknown"
}

// appendStatusEntries appends the gitFile rows for one porcelain XY
// status pair + path. A path that is BOTH staged (X) and changed in the
// worktree (Y) yields two rows — one staged, one unstaged — so the git
// panel can stage/discard each side independently. Any caller counting
// CHANGED FILES therefore has to count distinct paths rather than
// entries (git-types.ts changedPathCount is the client's).
//
// orig is the rename/copy origin path, empty for every other entry. It
// rides only the row whose status letter is the R or C, because the
// other side of a partially-staged rename describes an ordinary edit to
// the file at its new path and did not come from anywhere.
func appendStatusEntries(files []gitFile, x, y byte, path, orig string) []gitFile {
	f := gitFile{Path: path}
	switch {
	case x == '?' && y == '?':
		f.Status = "?"
		f.Display = "Untracked"
	case x != ' ' && x != '?':
		f.Status = string(x)
		f.Staged = true
		f.Display = statusLabel(x)
		if x == 'R' || x == 'C' {
			f.OrigPath = orig
		}
	default:
		f.Status = string(y)
		f.Display = statusLabel(y)
		if y == 'R' || y == 'C' {
			f.OrigPath = orig
		}
	}
	files = append(files, f)
	if x != ' ' && x != '?' && y != ' ' && y != '?' {
		second := gitFile{
			Path: path, Status: string(y), Staged: false,
			Display: statusLabel(y),
		}
		if y == 'R' || y == 'C' {
			second.OrigPath = orig
		}
		files = append(files, second)
	}
	return files
}

// splitTrackedUntracked partitions the given file list into tracked
// (checkout --) and untracked (clean -fd) buckets based on the current
// git status.
//
// It reads through the same one status invocation the dashboard uses, so the
// tree carries a single status format and a single row builder. A failed call
// yields two empty buckets, which is what makes the discard a no-op rather than
// a guess.
func splitTrackedUntracked(ctx context.Context, dir string, files []string) (tracked, untracked []string) {
	st, err := readStatus(ctx, dir)
	if err != nil {
		return nil, nil
	}
	wanted := make(map[string]bool, len(files))
	for _, f := range files {
		wanted[f] = true
	}
	seen := make(map[string]bool, len(files))
	for _, f := range st.Files {
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
