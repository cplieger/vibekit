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
var urlCredPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`)

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

// Cmd builds an *exec.Cmd for a git subprocess with a scrubbed
// environment that prevents git from blocking on credential prompts,
// from using askpass GUIs, from allowing user-controlled remote
// helpers (ext::, file://), and from inheriting GIT_CONFIG_* injection
// that would re-enable the ext:: transport class via runtime gitconfig
// overrides.
//
// Callers must supply a context with an appropriate timeout.
func Cmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_CONFIG_COUNT=",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
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
		return h
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
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
