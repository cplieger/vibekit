package filehandler

import (
	"errors"
	"fmt"
	"strings"
)

// Directories hidden from the root listing and blocked from traversal.
// Covers standard Debian system dirs plus vibekit's own install
// locations (/app holds the compiled web binary, /opt/vibekit holds
// the entrypoint script and the compiled tool catalog). Only
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
// Protects system-level agent config, auto-generated docs, internal
// state files, and — most importantly — the credential stores that back
// vibekit's own auth, git, and MCP integrations.
//
// kiro-cli's per-user state (KIRO_HOME = $HOME/.kiro: steering,
// sessions, settings, agents, logs) lives inside the container HOME,
// so the /config/home/ tree block below covers all of it — including
// the auto-generated steering/environment.md and steering/custom.md
// (edited only through /api/steering, never the file editor).
//
// /config/kiro/ is the LEGACY KIRO_HOME (pre-relocation; the v3 engine
// ignores KIRO_HOME and reads $HOME/.kiro, so KIRO_HOME moved inside
// HOME). The entrypoint migrates and deletes it on first boot, but the
// prefix stays blocked so the editor can't touch stragglers on a
// volume that predates the migration.
var sensitivePrefixes = []sensitivePath{
	// Legacy kiro-cli state tree (migrated + removed by entrypoint).
	{Path: "/config/kiro/", IsDir: true},
	// Container HOME ($HOME=/config/home): AWS SSO token + OAuth secret
	// (~/.aws/sso/cache), git SSH keys (~/.ssh), forge PAT
	// (~/.config/gh/hosts.yml), ~/.gitconfig, kiro-cli's ~/.kiro state
	// (steering, sessions, settings, agents), and its ~/.local install
	// tree. The whole tree is blocked.
	{Path: "/config/home/", IsDir: true},
	// Internal vibekit runtime state.
	{Path: "/config/chats/", IsDir: true},
	{Path: "/config/push-subs.json", IsDir: false},
	{Path: "/config/vapid-keys.json", IsDir: false},
	// MCP server config — env / header / OAuth secrets stored cleartext.
	{Path: "/config/mcp.json", IsDir: false},
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
