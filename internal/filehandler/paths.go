// Path resolution for the file handler: allow-list mount matching
// plus symlink-aware canonicalisation.
//
// Defense layers (all must pass for a request to reach the filesystem):
//
//  1. Mount match + sensitive-prefix check on the lexically cleaned
//     path (enforce). Unknown roots are denied by DEFAULT — only the
//     granted mounts resolve. Catches simple and traversal-via-`..`
//     attempts.
//  2. Symlink evaluation on the resolved target (or on the parent for
//     yet-to-be-created targets), plus a re-run of layer 1 against the
//     real on-disk path. Catches symlinks planted by an agent/shell
//     that point outside the granted mounts or into sensitive
//     territory. Mirrors hub/bridge_fs.resolveInsideWorkDir.
//  3. The per-mount os.Root. Every filesystem operation goes through
//     the matched mount's kernel-confined root handle, so a symlink
//     swapped in AFTER layer 2 (TOCTOU) still cannot escape the mount.
//  4. Per-action guards (actionDelete's isProtectedDir, actionRename's
//     destination sensitive-path check, the mount-point refusals).
//     Catch destructive operations aimed at sensitive paths that the
//     lexical layer protects only as leaves, not as containers.

package filehandler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/pathinside"
)

// mount is one granted browse root: a cleaned absolute directory plus
// its kernel-confined os.Root handle.
type mount struct {
	root *os.Root
	dir  string // clean, absolute, no trailing slash, never "/"
	name string // dir without the leading slash — the synthetic root entry name
}

// loc is a fully resolved location: the granted mount that owns it and
// the real (symlink-evaluated) absolute path inside it. All filesystem
// operations derive from a loc so they run through the mount's os.Root.
type loc struct {
	m   *mount
	abs string
}

// rel returns the path relative to the owning mount, in the form
// os.Root operations expect ("." for the mount directory itself).
func (l loc) rel() string {
	rel := strings.TrimPrefix(l.abs, l.m.dir)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}

// relOf converts sibling absolute path p (must be inside l's mount —
// true by construction at both call sites, which join a basename onto
// a directory already resolved into this mount) to root-relative form.
func (l loc) relOf(p string) string {
	return loc{m: l.m, abs: p}.rel()
}

// isMountPoint reports whether the location is the granted root
// itself, which create/rename/delete refuse to touch.
func (l loc) isMountPoint() bool { return l.abs == l.m.dir }

// errOutsideRoots is the uniform denial for paths outside every
// granted mount. Symlink-probe failures and plain out-of-tree requests
// share one message so an attacker can't distinguish them.
var errOutsideRoots = errors.New("access denied: outside granted roots")

// mountFor returns the granted mount owning the cleaned absolute path,
// or nil. Mounts are sorted longest-first, so a nested grant wins over
// its ancestor.
//
// pathinside.Inside is the containment rule: root itself IS inside (a
// request for the mount directory resolves to its own mount), and the
// separator-precise test is what keeps a sibling grant-lookalike
// ("/workspace-evil" against the "/workspace" grant) out. mount.dir is
// clean, absolute and never "/" by construction (openMounts), and clean
// is cleaned by resolvePath, so no normalisation is lost here.
func (h *Handler) mountFor(clean string) *mount {
	for i := range h.mounts {
		m := &h.mounts[i]
		if pathinside.Inside(m.dir, clean) {
			return m
		}
	}
	return nil
}

// enforce runs the allow-list + sensitive-path policy on an
// already-canonicalised absolute path. Applied to both the lexical and
// the real-path forms by resolvePath so they enforce identical policy —
// any drift would create a symlink-based bypass.
func (h *Handler) enforce(clean string) (*mount, error) {
	m := h.mountFor(clean)
	if m == nil {
		return nil, errOutsideRoots
	}
	if IsSensitive(clean) {
		return nil, errors.New("access denied: protected path")
	}
	return m, nil
}

// resolvePath cleans reqPath to an absolute form and enforces the
// access policy on both the lexical and real-path forms. Symlinks that
// would escape the granted mounts or land on a sensitive path are
// rejected with the same error shape as a lexical violation.
//
// For yet-to-exist targets (touch / mkdir / upload new file), the
// parent directory is evaluated for symlinks and the real base is
// recomposed against the resolved parent. For existing targets, the
// leaf itself is evaluated. The returned loc carries the mount that
// owns the REAL path — an in-tree symlink crossing from one granted
// mount into another resolves to the target's mount.
func (h *Handler) resolvePath(reqPath string) (loc, error) {
	clean := filepath.Clean("/" + reqPath)
	if _, err := h.enforce(clean); err != nil {
		return loc{}, err
	}
	realPath, err := resolveRealPath(clean)
	if err != nil {
		return loc{}, err
	}
	m, err := h.enforce(realPath)
	if err != nil {
		return loc{}, err
	}
	return loc{m: m, abs: realPath}, nil
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
// symlinked segment through to the second enforce check. Walking up
// until an ancestor resolves forces the symlink crossing to surface,
// so enforce sees the real top-level segment (e.g. /etc) and rejects.
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
	// to return — MkdirAll creates the tree under the granted mount.
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

// ParseBrowseRoots normalises a colon-separated root list (the
// VIBEKIT_BROWSE_ROOTS format, PATH-style) into cleaned absolute
// directories. Relative entries and "/" are rejected with an error
// listing so the caller can log them; duplicates collapse.
func ParseBrowseRoots(raw string) (roots, invalid []string) {
	seen := make(map[string]bool)
	for entry := range strings.SplitSeq(raw, ":") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !filepath.IsAbs(entry) {
			invalid = append(invalid, entry+" (not absolute)")
			continue
		}
		clean := filepath.Clean(entry)
		if clean == "/" {
			invalid = append(invalid, entry+" (root grant defeats the allow-list)")
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		roots = append(roots, clean)
	}
	return roots, invalid
}

// openMounts opens an os.Root per granted directory. A directory that
// cannot be opened (missing, not a dir, permissions) is skipped with
// its error recorded — a typo'd grant must not brick the UI — but at
// least one mount must survive or the handler is useless and the
// caller should fail loudly.
func openMounts(rootDirs []string) ([]mount, []error) {
	var errs []error
	seen := make(map[string]bool)
	mounts := make([]mount, 0, len(rootDirs))
	for _, dir := range rootDirs {
		clean := filepath.Clean(dir)
		if !filepath.IsAbs(clean) || clean == "/" {
			errs = append(errs, fmt.Errorf("browse root %q: must be an absolute path other than /", dir))
			continue
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		root, err := os.OpenRoot(clean)
		if err != nil {
			errs = append(errs, fmt.Errorf("browse root %q: %w", dir, err))
			continue
		}
		mounts = append(mounts, mount{
			root: root,
			dir:  clean,
			name: strings.TrimPrefix(clean, "/"),
		})
	}
	// Longest dir first so nested grants win prefix matching.
	slices.SortFunc(mounts, func(a, b mount) int {
		if d := len(b.dir) - len(a.dir); d != 0 {
			return d
		}
		return strings.Compare(a.dir, b.dir)
	})
	return mounts, errs
}
