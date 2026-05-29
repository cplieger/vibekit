// Path resolution helpers for the file handler.
//
// Defense layers (all three must pass for a request to reach the
// filesystem):
//
//  1. Lexical blacklist + sensitive-prefix check on the cleaned path
//     (resolvePath). Catches simple and traversal-via-`..` attempts.
//  2. Symlink evaluation on the resolved target (or on the parent for
//     yet-to-be-created targets), plus a re-run of layer 1 against the
//     real on-disk path. Catches symlinks planted by an agent/shell
//     that point into blacklisted or sensitive territory. Mirrors
//     hub/bridge_fs.resolveInsideWorkDir.
//  3. Per-action guards (actionDelete's isProtectedDir, actionRename's
//     destination sensitive-path check). Catches destructive operations
//     aimed at sensitive paths that the lexical layer protects only as
//     leaves, not as containers.

package filehandler

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// resolvePath cleans reqPath to an absolute form and enforces the two
// access-control layers (blacklist + sensitive list) on both the
// lexical and real-path forms. Symlinks that would escape either layer
// are rejected with the same error shape as a lexical violation — the
// attacker can't distinguish symlink-probe failures from blacklist hits.
//
// For yet-to-exist targets (touch / mkdir / upload new file), the
// parent directory is evaluated for symlinks and the real base is
// recomposed against the resolved parent. For existing targets, the
// leaf itself is evaluated.
func resolvePath(reqPath string) (string, error) {
	resolved := filepath.Clean("/" + reqPath)
	if err := enforceAccess(resolved); err != nil {
		return "", err
	}
	realPath, err := resolveRealPath(resolved)
	if err != nil {
		return "", err
	}
	if err := enforceAccess(realPath); err != nil {
		return "", err
	}
	return realPath, nil
}

// resolveRealPath evaluates symlinks on `clean` (an absolute,
// already-cleaned path). If the target exists it returns the fully
// symlink-resolved path. If it does not, it walks ancestors upward
// until it finds one that resolves, then recomposes the missing
// suffix against the resolved ancestor.
//
// The ancestor walk closes the bypass where a symlinked ancestor +
// a two-or-more-deep non-existent leaf would otherwise fall through
// to the unresolved lexical form: EvalSymlinks(parent) returns
// ENOENT because the leaf's sibling doesn't exist in the symlink
// target, and the caller would only see the "leaf missing, parent
// missing" path and return the lexical form — leaking an unresolved
// symlinked segment through to the second enforceAccess check.
// Walking up until an ancestor resolves forces the symlink
// crossing to surface, so enforceAccess sees the real top-level
// segment (e.g. /etc) and rejects.
func resolveRealPath(clean string) (string, error) {
	if realPath, err := filepath.EvalSymlinks(clean); err == nil {
		return realPath, nil
	} else if !os.IsNotExist(err) {
		// ENOENT anywhere along the chain is expected for
		// yet-to-exist targets; any other eval error (e.g.
		// permission denied on an intermediate dir) is a real
		// failure and should surface.
		return "", err
	}
	// Walk upward collecting missing tail components until we hit
	// an existing ancestor we can resolve. Stop when Dir stops
	// making progress (reached "/"); in that case every component
	// was missing and the already-validated lexical path is safe
	// to return — MkdirAll creates the tree under the blessed top
	// segment.
	var tail []string
	cur := clean
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean, nil
		}
		tail = append(tail, filepath.Base(cur))
		realParent, err := filepath.EvalSymlinks(parent)
		if err == nil {
			out := realParent
			for _, t := range slices.Backward(tail) {
				out = filepath.Join(out, t)
			}
			return out, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		cur = parent
	}
}

// relPath converts an absolute resolved path to a root-relative path
// suitable for os.Root operations. Strips the handler's rootDir prefix.
func (h *Handler) relPath(resolved string) string {
	rel := strings.TrimPrefix(resolved, h.rootDir)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}
