// Package gitexec provides hardened git subprocess execution and
// credential scrubbing shared by the git and forges packages.
package gitexec

import (
	"context"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// PlumbingTimeout bounds a single local-only git plumbing command
// (e.g. remote get-url, symbolic-ref). 5 seconds is plenty for
// local-only operations on a healthy filesystem and tight enough
// that a wedged filesystem doesn't pin callers forever.
const PlumbingTimeout = 5 * time.Second

// Timeouts consolidates git subprocess timeout budgets into a single
// policy struct. Each consumer (git.Handler, forges.Registry) accepts
// a Timeouts value so the budget is explicit and testable.
type Timeouts struct {
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

// DefaultTimeouts returns the production timeout policy.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Plumbing: PlumbingTimeout,
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

// ScrubAuth strips credentials from a git subprocess output string.
// Idempotent: chained userinfo segments (`http://a@b@c@host`) are
// consumed until the match set stabilises. The regex strictly shrinks
// the string on every match (each iteration removes at least one
// `@segment`), so the loop is bounded by input length with no DoS risk.
func ScrubAuth(s string) string {
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

// ScrubAuthErr is a convenience wrapper for error values.
func ScrubAuthErr(err error) string {
	if err == nil {
		return ""
	}
	return ScrubAuth(err.Error())
}

// --- Hardened subprocess execution ---

// allowedSubcommands lists git subcommands that may be invoked through
// gitexec.Cmd. Any first non-flag argument outside this set causes Cmd
// to return a no-op command that exits with an error, defending against
// callers that accidentally let untrusted input choose the subcommand.
//
// CodeQL's go/command-injection rule cannot prove safety from
// validation done at HTTP-handler layer (e.g. isValidGitRef on body
// fields); declaring the allowlist at the exec boundary makes the
// guarantee local to this package.
var allowedSubcommands = map[string]struct{}{
	"add":          {},
	"branch":       {},
	"checkout":     {},
	"clone":        {},
	"commit":       {},
	"config":       {},
	"diff":         {},
	"fetch":        {},
	"init":         {},
	"log":          {},
	"ls-remote":    {},
	"merge":        {},
	"pull":         {},
	"push":         {},
	"rebase":       {},
	"remote":       {},
	"reset":        {},
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

// Cmd builds an *exec.Cmd for a git subprocess with hardening
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
// The first non-flag arg in `args` must be one of allowedSubcommands;
// otherwise Cmd returns a command rigged to fail without launching
// git. This local guarantee satisfies CodeQL's go/command-injection
// analyzer and gives defence-in-depth against future callers that
// don't validate subcommand input upstream.
//
// Callers must supply a context with an appropriate timeout.
func Cmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	sub := firstSubcommand(args)
	if _, ok := allowedSubcommands[sub]; !ok {
		// Build a synthetic command that errors out cleanly. /bin/false
		// always exits 1 and produces no output; CombinedOutput will
		// surface a sentinel via stderr we set in cmd.Args[0].
		cmd := exec.CommandContext(ctx, "/bin/false")
		cmd.Dir = dir
		// Stash a useful error string in Args so callers logging
		// CombinedOutput see why; /bin/false ignores its args.
		cmd.Args = append(cmd.Args, "gitexec: subcommand not allowed: "+sub)
		return cmd
	}
	// Prepend hardening -c flags. Command-line -c values take priority
	// over any gitconfig setting, so even a user gitconfig with
	// `[protocol "ext"] allow = always` cannot re-enable ext::.
	hardenedArgs := append([]string{
		"-c", "protocol.ext.allow=never",
	}, args...)
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

// Run executes a git subprocess and returns trimmed combined output.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := Cmd(ctx, dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ParseRemoteHost extracts the host segment from an https or scp-style
// git remote URL. Returns "" for unrecognised shapes.
//
//	https://github.com/foo/bar.git     → github.com
//	git@github.com:foo/bar.git         → github.com
//	ssh://git@gitlab.com/foo/bar.git   → gitlab.com
//
// Rejects ext:: remote-helper prefixes as a defense-in-depth measure.
func ParseRemoteHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if h, _, ok := ParseSCPStyle(raw); ok {
		return sanitizeHost(h)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return sanitizeHost(u.Hostname())
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

// ParseSCPStyle recognises git's scp-like remote syntax (user@host:path)
// and returns (host, path, true) on a successful match. Returns ok=false
// for anything else, including URLs with a :// scheme, strings without @,
// and ext:: remote-helper prefixes.
func ParseSCPStyle(raw string) (host, path string, ok bool) {
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
