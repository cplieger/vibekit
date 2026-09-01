package filebrowse

import "strings"

// Access model: an ALLOW-LIST of granted roots (mounts) plus the
// sensitive-path deny-list below. Mount matching lives in paths.go
// (resolvePath); this file keeps the second layer, a deny-list by
// necessity: the sensitive entries live INSIDE the granted /config
// mount, and an os.Root cannot enforce sub-path denial.
//
// The prefix tests below are deliberately NOT pathinside calls: IsSensitive
// must EXCLUDE the listed directory itself so isProtectedDir can answer for
// it separately under a different rule (see its doc), and isProtectedDir
// asks in BOTH directions — does the candidate enclose a sensitive path, or
// does a sensitive path enclose it — which no single pathinside function
// expresses.

// sensitivePath describes a single blocked path entry with explicit
// match semantics: IsDir=true means "directory prefix" (blocks all
// contents), IsDir=false means "exact file match".
type sensitivePath struct {
	Path  string
	IsDir bool
}

// Specific paths or path prefixes blocked from the file-editor surface:
// system-level agent config, auto-generated docs, internal state files,
// and the credential stores backing vibekit's own auth, git, and MCP
// integrations.
//
// /config/kiro/ is the LEGACY KIRO_HOME (pre-relocation; the v3 engine
// ignores KIRO_HOME and reads $HOME/.kiro). The entrypoint migrates and
// deletes it on first boot, but the prefix stays blocked so the editor
// can't touch stragglers on a volume that predates the migration.
var sensitivePrefixes = []sensitivePath{
	// Legacy kiro-cli state tree (migrated + removed by entrypoint).
	{Path: "/config/kiro/", IsDir: true},
	// Container HOME ($HOME=/config/home): AWS SSO token + OAuth secret,
	// git SSH keys, forge PAT, ~/.gitconfig, kiro-cli's ~/.kiro state, and
	// its ~/.local install tree. The whole tree is blocked.
	{Path: "/config/home/", IsDir: true},
	// Internal vibekit runtime state.
	{Path: "/config/chats/", IsDir: true},
	{Path: "/config/push-subs.json", IsDir: false},
	{Path: "/config/vapid-keys.json", IsDir: false},
	// MCP server config — env / header / OAuth secrets stored cleartext.
	{Path: "/config/mcp.json", IsDir: false},
	// The OAuth credentials KAS asks vibekit to hold for it (opaque blobs,
	// refresh tokens, PKCE verifiers whose 0600 is the whole protection).
	{Path: "/config/mcp-secrets.json", IsDir: false},
}

// IsSensitive reports whether the resolved absolute path is user-blocked.
// Directory prefixes match their contents but not the directory itself;
// use isProtectedDir when the operation would affect the container.
//
// EXPORTED because internal/server's `.kiro` docs scanner calls this same
// function rather than keeping its own denylist — two scanners disagreeing
// about it is the inconsistency that becomes a real leak the next time a
// root is widened. The caller must pass an already-resolved absolute
// container path (symlinks followed): the entries below are absolute
// `/config/...` paths, so an unresolved path cannot match one.
func IsSensitive(resolved string) bool {
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
// This is the "directory container" check that IsSensitive deliberately
// omits — IsSensitive alone lets `/config/chats` (no trailing slash)
// through while `/config/chats/foo.json` is blocked. Callers pass an
// already-canonicalised path.
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
