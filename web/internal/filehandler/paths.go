// Path resolution + access-control helpers for the file handler.
// The blacklist blocks top-level system dirs; the sensitive-path list
// blocks specific paths we don't want to expose even when they'd
// otherwise pass the blacklist (auto-regenerated config, session state,
// internal runtime files).
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Directories hidden from the root listing and blocked from traversal.
// Covers standard Debian system dirs plus vibekit's own install
// locations (/app holds the compiled web binary, /opt/vibekit holds
// the entrypoint scripts and tools.json.default template). Only
// mounted volumes (/workspace, /config) should remain visible at /.
var blacklist = map[string]bool{
	"app": true, "bin": true, "boot": true, "dev": true, "etc": true,
	"lib": true, "lib64": true, "media": true, "opt": true,
	"proc": true, "root": true, "run": true, "sbin": true,
	"srv": true, "sys": true, "tmp": true, "usr": true,
	"var": true,
}

// sensitivePath describes a single blocked path entry with explicit
// match semantics: IsDir=true means "directory prefix" (blocks all
// contents), IsDir=false means "exact file match".
type sensitivePath struct {
	Path  string
	IsDir bool
}

// Specific paths or path prefixes blocked from the file-editor surface.
// Protects system-level agent config, auto-generated docs, and internal
// state files that users should not accidentally edit from the UI.
//
// All kiro-cli per-user state lives under /config/kiro/ (the location
// vibekit's entrypoint sets via KIRO_HOME).
var sensitivePrefixes = []sensitivePath{
	// System-level steering (base personality; must not be user-edited).
	{Path: "/config/kiro/steering/vibekit.md", IsDir: false},
	// Auto-regenerated at startup; edits are clobbered and misleading.
	{Path: "/config/kiro/steering/environment.md", IsDir: false},
	// Agent configs, session state, knowledge bases.
	{Path: "/config/kiro/agents/", IsDir: true},
	{Path: "/config/kiro/sessions/", IsDir: true},
	{Path: "/config/kiro/settings/", IsDir: true},
	{Path: "/config/home/.local/share/kiro-cli/", IsDir: true},
	// Internal vibekit runtime state.
	{Path: "/config/chats/", IsDir: true},
	{Path: "/config/push-subs.json", IsDir: false},
	{Path: "/config/vapid-keys.json", IsDir: false},
}

// isSensitive reports whether the resolved absolute path is user-blocked.
// Directory prefixes match their contents but not the directory itself;
// use isProtectedDir when the operation would affect the container.
func isSensitive(resolved string) bool {
	for _, sp := range sensitivePrefixes {
		if sp.IsDir {
			if strings.HasPrefix(resolved, sp.Path) {
				return true
			}
		} else if resolved == sp.Path {
			return true
		}
	}
	return false
}

// isProtectedDir reports whether deleting `resolved` would wipe a
// directory listed (or enclosing a path listed) in sensitivePrefixes.
// This is the "directory container" check that isSensitive deliberately
// omits — isSensitive alone lets `/config/chats` (no trailing slash)
// through while `/config/chats/foo.json` is blocked.
//
// Callers pass an already-canonicalised path (filepath.Clean has run
// either via resolvePath or explicitly). The suffix-"/" form is used
// so the trailing-slash comparison works for both trailing-slash-free
// resolved values and trailing-slash-terminated sensitive prefixes.
func isProtectedDir(resolved string) bool {
	res := strings.TrimRight(resolved, "/") + "/"
	for _, sp := range sensitivePrefixes {
		if sp.IsDir {
			// Sensitive directory entry: block the dir itself and
			// every ancestor directory that would encompass it.
			if strings.HasPrefix(sp.Path, res) || strings.HasPrefix(res, sp.Path) {
				return true
			}
			continue
		}
		// Sensitive file entry: block any directory that encloses it.
		if strings.HasPrefix(sp.Path, res) {
			return true
		}
	}
	return false
}

// enforceAccess runs the blacklist + sensitive-path check on an
// already-canonicalised absolute path. Shared by the lexical
// (resolvePath) and real-path (resolveRealPath) checks so they enforce
// identical policy — any drift would create a symlink-based bypass.
func enforceAccess(resolved string) error {
	top := strings.SplitN(strings.TrimPrefix(resolved, "/"), "/", 2)[0]
	if blacklist[top] {
		return fmt.Errorf("access denied: /%s", top)
	}
	if isSensitive(resolved) {
		return errors.New("access denied: protected path")
	}
	return nil
}

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
