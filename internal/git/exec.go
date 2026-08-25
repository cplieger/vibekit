// Hardened git subprocess execution and credential scrubbing.
//
// This was internal/gitexec, a package named after its mechanism whose only
// importer was this one. Rolling it up here made every name below unexported,
// which is the point: the allowlist, the hardening flags and the scrubber are
// implementation detail of the git surface, not an API anyone else calls.

package git

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// plumbingTimeout bounds a single local-only git plumbing command
// (e.g. remote get-url, symbolic-ref). 5 seconds is plenty for
// local-only operations on a healthy filesystem and tight enough
// that a wedged filesystem doesn't pin callers forever.
const plumbingTimeout = 5 * time.Second

// gitTimeouts consolidates git subprocess timeout budgets into a single
// policy struct. Handler holds one so the budget is explicit and testable.
type gitTimeouts struct {
	// Plumbing bounds local-only operations: branch, status, rev-parse.
	Plumbing time.Duration
	// Fetch bounds network read-only operations: fetch --quiet.
	Fetch time.Duration
	// Push bounds network write operations: push, pull.
	Push time.Duration
	// Clone bounds full-transfer operations from the UI git handler
	// (shorter than the forges backend clone which allows 15m for
	// pre-configured credential clones of large repos).
	Clone time.Duration
}

// defaultTimeouts returns the production timeout policy.
func defaultTimeouts() gitTimeouts {
	return gitTimeouts{
		Plumbing: plumbingTimeout,
		Fetch:    5 * time.Second,
		Push:     60 * time.Second,
		Clone:    2 * time.Minute,
	}
}

// --- Credential scrubbing ---

// urlCredPattern matches `scheme://user:pwd@host` or `scheme://token@host`
// embedded in error strings. Applies to any RFC 3986 scheme (not
// just http(s)), so ssh and git:// URLs with credential-helper
// rewrites are also scrubbed.
var urlCredPattern = regexp.MustCompile(`(://)[^/]*@`)

// urlQueryTokenPattern matches secret-bearing query parameters
// (?token=, ?access_token=, ?private_token=, ?api_key=, ?apikey=)
// that self-hosted Gitea/Forgejo and GitHub's legacy OAuth app flow
// sometimes emit. The replacement keeps the key name so debug
// context survives.
var urlQueryTokenPattern = regexp.MustCompile(`([?&](?:token|access_token|private_token|api_key|apikey)=)[^&\s]+`)

// authHeaderPattern matches Authorization: Bearer/Token/Basic
// headers echoed back in error bodies that reflect request
// headers. Case-insensitive on the header name only.
var authHeaderPattern = regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|token|basic)\s+)\S+`)

// scrubAuth strips credentials from a git subprocess output string.
// Idempotent: chained userinfo segments (`http://a@b@c@host`) are
// consumed until the match set stabilises. The regex strictly shrinks
// the string on every match (each iteration removes at least one
// `@segment`), so the loop is bounded by input length with no DoS risk.
func scrubAuth(s string) string {
	if s == "" {
		return ""
	}
	for {
		out := urlCredPattern.ReplaceAllString(s, "${1}")
		if out == s {
			break
		}
		s = out
	}
	s = urlQueryTokenPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = authHeaderPattern.ReplaceAllString(s, "${1}[REDACTED]")
	return s
}

// --- Hardened subprocess execution ---

// allowedSubcommands lists git subcommands that may be invoked through
// gitExec. Any first non-flag argument outside this set causes gitExec
// to return a no-op command that exits with an error, defending against
// callers that accidentally let untrusted input choose the subcommand.
//
// CodeQL's go/command-injection rule cannot prove safety from
// validation done at HTTP-handler layer (e.g. isValidGitRef on body
// fields); declaring the allowlist at the exec boundary makes the
// guarantee local to this package.
// Subcommand names this package builds argv from in more than one place, so the
// allowlist entry and every call site are the same token by construction.
const (
	subAdd      = "add"
	subCheckout = "checkout"
	subClean    = "clean"
	subFetch    = "fetch"
	subRemote   = "remote"
	subReset    = "reset"
)

var allowedSubcommands = map[string]struct{}{
	subAdd:         {},
	"branch":       {},
	subCheckout:    {},
	subClean:       {},
	"clone":        {},
	"commit":       {},
	"config":       {},
	"diff":         {},
	subFetch:       {},
	"init":         {},
	"log":          {},
	"ls-remote":    {},
	"merge":        {},
	"pull":         {},
	"push":         {},
	"rebase":       {},
	subRemote:      {},
	subReset:       {},
	"rev-list":     {},
	"rev-parse":    {},
	"show":         {},
	"show-ref":     {},
	"stash":        {},
	"status":       {},
	"submodule":    {},
	"switch":       {},
	"symbolic-ref": {},
	"tag":          {},
	"update-ref":   {},
	"worktree":     {},
}

// firstSubcommand walks args looking for the first token that doesn't
// start with '-' (i.e. the git subcommand). Returns "" if no token is
// found, which means the caller passed only flags — also rejected.
func firstSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			// Skip values for the limited set of -c/-C-style flags we
			// know take a separate argument. For our hardened-cmd usage
			// callers don't pass -c themselves, so this branch is
			// defensive.
			if a == "-c" || a == "-C" {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// gitExec builds an *exec.Cmd for a git subprocess with hardening
// applied: protocol.ext.allow=never on the command line (so ext::
// transports stay blocked even if user gitconfig tries to enable
// them — `-c` always wins over gitconfig), no terminal/askpass
// prompts (so credential failures bubble up as errors instead of
// hanging), and runtime GIT_CONFIG_* env injection cleared so a
// malicious parent process can't inject inline gitconfig.
//
// IMPORTANT: this DOES allow the user's ~/.gitconfig and the
// system /etc/gitconfig to load. That's deliberate. The forge
// CLIs (gh auth setup-git, glab auth git-credential, etc.) write
// `credential.helper` lines into ~/.gitconfig so HTTPS clones of
// private repos can authenticate. A previous version of this
// function pinned GIT_CONFIG_GLOBAL=/dev/null which disabled the
// credential helper alongside the ext:: hardening — clones of
// public repos worked, but private clones failed with "terminal
// prompts disabled". The cmdline -c approach is a more surgical
// fix: it blocks ext:: explicitly without throwing out the rest
// of the user's git config.
//
// # What git will still execute for a repo nobody here wrote, and why
//
// Loading gitconfig FILES means loading a REPO's `.git/config` too, and
// several config keys name a command git then runs. The two with no
// legitimate use on this surface are neutralised on the command line
// above (core.fsmonitor) and per call site (diff.<driver>.textconv, via
// --no-textconv on the diff family). Two classes are deliberately left
// live, and neither is closed by anything in this package:
//
//   - `filter.<driver>.clean` / `.smudge` CANNOT be disabled. git offers
//     no --no-filter flag and no wildcard config form (`filter.*.clean=`
//     is not a thing git reads), so a repo carrying both a .gitconfig
//     entry and a .gitattributes line that selects the driver still runs
//     that command on a checkout, a stage, or `git diff` of a filtered
//     path. Clearing GIT_CONFIG_COUNT does not touch it — that only
//     blocks INLINE config from a parent process, and this one is on
//     disk. The exposure is real and stated rather than papered over:
//     opening an untrusted repo in this app can execute code from it.
//
//   - HOOKS stay ON, deliberately. `git commit` and `git push` run
//     pre-commit, commit-msg and pre-push, which is what makes the git
//     panel's commit equivalent to the user's own — this fleet's repos
//     use hooks for formatting and secret scanning, and a UI that
//     silently skipped them would produce commits CI then rejects.
//     Nothing here passes --no-verify or core.hooksPath.
//
// Both classes require a repo already checked out into the workspace,
// which is a decision the user made, so the answer is not to break the
// tooling for every ordinary repo — it is to know that this is what
// "open somebody else's repo" costs.
//
// The first non-flag arg in `args` must be one of allowedSubcommands;
// otherwise gitExec returns a command rigged to fail without launching
// git. This local guarantee satisfies CodeQL's go/command-injection
// analyzer and gives defence-in-depth against future callers that
// don't validate subcommand input upstream.
//
// Callers must supply a context with an appropriate timeout.
func gitExec(ctx context.Context, dir string, args ...string) *exec.Cmd {
	if _, ok := allowedSubcommand(args); !ok {
		// Build a synthetic command that fails without launching git. /bin/false
		// always exits 1, and this branch deliberately spawns no shell: giving it
		// an argv it could interpolate would hand a command-injection taint path
		// to the exact boundary this allowlist exists to close.
		//
		// It also produces no OUTPUT, which used to be the whole diagnostic
		// problem — a caller composing the output into its own message rendered a
		// bare "clean:" naming no cause. That is fixed one layer up rather than
		// here: gitCmd runs the same check and returns a real error, so the only
		// callers that can reach this branch are the two that pass gitExec a
		// literal subcommand, for which it is pure defence-in-depth.
		cmd := exec.CommandContext(ctx, "/bin/false")
		cmd.Dir = dir
		return cmd
	}
	// Prepend hardening -c flags. Command-line -c values take priority
	// over any gitconfig setting, so even a user gitconfig with
	// `[protocol "ext"] allow = always` cannot re-enable ext::.
	hardenedArgs := append([]string{
		"-c", "protocol.ext.allow=never",
		// core.fsmonitor names a command git runs on status and diff — the
		// two subcommands the git panel calls most — and an empty value is
		// the documented "no monitor" setting. It is cleared CENTRALLY
		// rather than per call site because it is a config key, so it costs
		// nothing on the subcommands that ignore it, and because there is
		// no legitimate use for it here at all: this container has no
		// fsmonitor daemon, so any value present came from a repo's own
		// .git/config.
		"-c", "core.fsmonitor=",
	}, args...)
	//nolint:gosec // G702: the subcommand is checked against allowedSubcommands above and the binary name is the literal "git"; every remaining argv element is a separate token to execve with no shell, and the ref/path-shaped ones are validated at the handler boundary (isValidGitRef, validateFilePath, resolveRepoDir)
	cmd := exec.CommandContext(ctx, "git", hardenedArgs...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_PROTOCOL_FROM_USER=0",
		// Clear runtime GIT_CONFIG_* injection: a malicious parent
		// could otherwise set GIT_CONFIG_COUNT + GIT_CONFIG_KEY_n /
		// GIT_CONFIG_VALUE_n to inject arbitrary inline config that
		// overrides our cmdline hardening. (gitconfig FILES on disk
		// are still loaded — that's where credential helpers live.)
		"GIT_CONFIG_COUNT=",
		"GIT_CONFIG_PARAMETERS=",
	)
	return cmd
}

// gitCmd executes a git subprocess and returns trimmed combined output.
// allowedSubcommand reports the subcommand `args` names and whether it is
// allowlisted. Shared by gitExec (which refuses to launch) and gitCmd (which
// reports WHY), so the two can never disagree about what is permitted.
func allowedSubcommand(args []string) (string, bool) {
	sub := firstSubcommand(args)
	_, ok := allowedSubcommands[sub]
	return sub, ok
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	// Checked here as well as in gitExec, because only this layer can return a
	// message. gitExec's refusal is a command that exits 1 in silence, and a
	// caller composing that silence into its own error produced a string ending
	// at its own colon — which is how a missing allowlist entry presented as
	// "Couldn't discard 6 files: clean:" and named nothing. No subprocess is
	// spawned on this path at all.
	if sub, ok := allowedSubcommand(args); !ok {
		return "", fmt.Errorf("git: subcommand not allowed: %s", sub)
	}
	out, err := gitExec(ctx, dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// parseRemoteHost extracts the host segment from an https or scp-style
// git remote URL. Returns "" for unrecognised shapes.
//
//	https://github.com/foo/bar.git     → github.com
//	git@github.com:foo/bar.git         → github.com
//	ssh://git@gitlab.com/foo/bar.git   → gitlab.com
//
// Rejects ext:: remote-helper prefixes as a defense-in-depth measure.
func parseRemoteHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if h, _, ok := parseSCPStyle(raw); ok {
		return sanitizeHost(h)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return sanitizeHost(u.Hostname())
}

// cloneDirName derives the directory `git clone <url>` creates, mirroring
// git's own guess_dir_name for the two URL shapes this surface accepts
// (https:// and scp-style git@host:path): the last path component with a
// trailing ".git" removed.
//
// Returns "" when the answer is not one ordinary directory component,
// which is the signal for the caller to let git derive the destination
// itself rather than act on a guess.
func cloneDirName(raw string) string {
	s := strings.TrimSpace(raw)
	// Neither a query nor a fragment is part of a repository path.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if _, p, ok := parseSCPStyle(s); ok {
		s = p
	} else {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Path
	}
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	name := strings.TrimSuffix(path.Base(s), ".git")
	// A traversal component, a nested path, a flag-shaped name, or git's
	// own metadata directory is never acted on here.
	switch name {
	case "", ".", "..", ".git":
		return ""
	}
	if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "-") {
		return ""
	}
	return name
}

// sanitizeHost returns "" if host contains control characters or is empty.
func sanitizeHost(h string) string {
	for _, c := range h {
		if c < 0x20 || c == 0x7f || c == '@' || c == ':' || c == '/' {
			return ""
		}
	}
	return h
}

// parseSCPStyle recognises git's scp-like remote syntax (user@host:path)
// and returns (host, repoPath, true) on a successful match. Returns ok=false
// for anything else, including URLs with a :// scheme, strings without @,
// and ext:: remote-helper prefixes.
//
// The second result is NOT named `path`: this file uses the path package,
// and a result named after an imported package shadows it for the whole
// function body.
func parseSCPStyle(raw string) (host, repoPath string, ok bool) {
	if strings.Contains(raw, "://") {
		return "", "", false
	}
	at := strings.Index(raw, "@")
	if at <= 0 {
		return "", "", false
	}
	user := raw[:at]
	if strings.Contains(user, "::") {
		return "", "", false
	}
	rest := raw[at+1:]
	h, p, found := strings.Cut(rest, ":")
	if !found || h == "" || strings.ContainsAny(h, "/?#") {
		return "", "", false
	}
	return h, p, true
}
