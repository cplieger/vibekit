package filebrowse

import "strings"

// Access model: an ALLOW-LIST of granted roots (mounts) plus the
// sensitive-path deny-list below.
//
// The old model was the inverse — the handler was rooted at the
// container root with a blacklist of system directories, which was
// fail-open: every new path outside the enumerated list was browsable
// by default. Mount matching lives in paths.go (resolvePath); this
// file keeps the second layer, the sensitive-path list, which is still
// a deny-list by necessity: the sensitive entries live INSIDE the
// granted /config mount, and an os.Root cannot enforce sub-path denial.
//
// The prefix tests below are deliberately NOT pathinside calls, and the
// reason is the deny-list inversion. pathinside's containment predicates
// count the root as inside, while IsSensitive must EXCLUDE the listed
// directory itself so isProtectedDir can answer for it separately under a
// different rule (see its doc). isProtectedDir is not a containment test
// at all: it asks in BOTH directions — does the candidate enclose a
// sensitive path, or does a sensitive path enclose it — which no single
// pathinside function expresses. The entries also carry their own
// trailing separator, so the sibling-lookalike failure that motivates the
// library ("/config/homework" against "/config/home/") does not arise
// here. Containment in this package IS pathinside: the mount lookup uses
// Inside (paths.go mountFor), backed by one os.Root per mount for the
// kernel-enforced half.

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
	// The OAuth credentials KAS asks vibekit to hold for it: opaque
	// blobs, refresh tokens and PKCE verifiers whose 0600 is the whole
	// protection. Its sibling above was blocked from the start and this
	// one was not, which left the more sensitive of the pair readable.
	{Path: "/config/mcp-secrets.json", IsDir: false},
}

// IsSensitive reports whether the resolved absolute path is user-blocked.
// Directory prefixes match their contents but not the directory itself;
// use isProtectedDir when the operation would affect the container.
//
// EXPORTED for one reason: internal/server's `.kiro` docs scanner reads files
// too, and it must agree with this handler about what is off limits. It calls
// THIS function rather than keeping a list of its own — two scanners disagreeing
// about the denylist is the inconsistency that becomes a real leak the next time
// a root is widened, and only one of them would be updated. The caller passes an
// already-resolved absolute container path, symlinks followed, which is the only
// form this predicate is meaningful on: the entries below are absolute
// `/config/...` paths, so a path that has not been resolved cannot match one.
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
