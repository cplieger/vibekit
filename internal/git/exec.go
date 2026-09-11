// Hardened git subprocess execution and credential scrubbing.
//
// This was internal/gitexec, a package named after its mechanism whose only
// importer was this one. Rolling it up here made every name below unexported,
// which is the point: the allowlist, the hardening flags and the scrubber are
// implementation detail of the git surface, not an API anyone else calls.

package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/systembin"
)

// errGitUnavailable is gitCmd's named refusal when the git binary is absent
// from internal/systembin's trusted directories. Distinct from a subcommand
// refusal: that one names a caller's mistake, this one names the image.
var errGitUnavailable = errors.New("git: not available")

// resolveGitBinary is the package's one resolution of the git binary, and the
// reassignable func-var seam its tests stage a fake git through. Production
// never reassigns it.
//
// The seam is required rather than convenient: this package's clone and transfer
// tests used to shadow git by PREPENDING a fake to PATH, and the pin above is
// precisely what makes PATH unreachable — so without a seam those tests would
// silently start driving the real git against a real remote. Two of them did,
// for 25 seconds each, before this existed.
var resolveGitBinary = func() (string, bool) { return systembin.Resolve("git") }

// gitTimeouts consolidates git subprocess timeout budgets into a single
// policy struct. Handler holds one so the budget is explicit and testable.
//
// There is no Plumbing budget here, and its absence is the measured state
// rather than an omission. The field existed until 2026-09 with a
// plumbingTimeout const behind it and ZERO readers: nothing applied it, so a
// local-only command (`remote get-url`, `status --porcelain`, `show`) is bounded
// by its CALLER's context. Declaring a budget nothing enforces is worse than
// declaring none, because a reader costs a change out on the belief that raising
// the number changes behaviour. It was deleted rather than wired: a filesystem
// slow enough to need a bound here is one a 5s refusal turns into a git panel
// that reports nothing, and the two operations that genuinely wait on something
// remote have their own budgets below. Applying one is a decision with a
// user-visible cost, not a tidy-up.
//
// That caller is NOT always an HTTP request, and the difference is why this
// closes rather than merely being deferred: every path that DETACHES from the
// request carries its own budget instead of inheriting nothing — the status scan
// (`statusScanBudget`), pull-all (`pullAllBudget`) and the forge list cache
// (`forges.ListTimeout`) each wrap `context.WithoutCancel` in a `WithTimeout`. So
// there is no unbounded plumbing path to close, and a per-command budget added
// here would be a second bound over paths that already have one.
type gitTimeouts struct {
	// Fetch bounds network read-only operations: fetch --quiet.
	Fetch time.Duration
	// Push bounds network write operations: push, pull.
	Push time.Duration
}

// There is no Clone budget here any more: a clone's liveness is its own
// progress stream (runTransfer's stall watchdog), bounded overall by
// cloneCeiling — a fixed transfer budget kills a large repo that is
// downloading fine, which is the defect that motivated the change.

// defaultTimeouts returns the production timeout policy.
func defaultTimeouts() gitTimeouts {
	return gitTimeouts{
		Fetch: 5 * time.Second,
		Push:  60 * time.Second,
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

// --- Credential redaction, and the three destinations it composes with ---
//
// redactCredentials on its own is UNBOUNDED and NOT single-line, so its result
// must never reach a sink directly. The three helpers below are the only callers,
// and the ORDER inside each is the substance: a sanitizer that runs BEFORE the
// redactor can defeat it, because redaction is a byte-exact pattern match and a
// transform on either side moves the bytes. Nothing outside this file can obtain
// redacted-but-unbounded text, which is what makes the wrong order inexpressible
// rather than merely discouraged.
//
// Redaction stays at the EMIT site and is deliberately NOT pushed down into
// gitCmd or runTransfer, even though a producer-side redaction would remove the
// chance of forgetting one: `git remote get-url` output is PARSED
// (commitURLPrefix, prRemoteHost) as well as displayed, and redacting at the
// producer would corrupt the parse. So this closes the ORDER class and not the
// OMISSION class.
//
// THE OMISSION CLASS IS CLOSED AS ACCEPTED, on a measurement rather than on
// effort. The obvious answer is to make it unrepresentable — have gitCmd return a
// type whose value cannot reach a sink without picking one of the three helpers —
// and it fails on the shape of the population: this package has 41 producer calls
// (36 gitCmd, 5 runTransfer) against 25 destination-helper calls, because most git
// output here is consumed internally (a branch name compared, an ahead/behind
// count parsed, a rev resolved) and never emitted at all. For that majority
// redaction is WRONG, so they would each take the wrapper's raw-value escape — and
// a type whose escape hatch is the common case constrains nothing while costing
// every call site. A lint allowlist is refused for its own reason: it is a denylist
// of spellings over a package whose next emit site has a spelling nobody has
// written yet.
//
// What WOULD close it is a narrower producer than a wrapper: the emit population is
// git's own FAILURE text plus `remote get-url`, so a variant returning output
// already bound for a sink would make the choice at the few sites that need it
// rather than at all 41. That is a shape change, not a guard, and it is not made
// on a package with no observed omission.

// maxClientOutputBytes bounds a multi-line git output block sent to a client.
// Generous: it is a transcript a human reads, and git's own failure messages
// carry the diagnosis in the last lines.
const maxClientOutputBytes = 64 * 1024

// maxRemoteURLBytes bounds a single-line value a human makes a decision from —
// the remote URL in the Sources row. Short, because anything longer is not a
// remote URL.
const maxRemoteURLBytes = 512

// clientOutputTruncated marks a client block the cap cut.
const clientOutputTruncated = "\n[output truncated]"

// logField prepares git output for a slog attribute: redact, then the app's ONE
// slog door (internal/logsafe's single-line preset plus its byte cap).
//
// One redaction pass is enough here, and the asymmetry with clientBlock is the
// point. logsafe's preset REPLACES an unsafe rune with a space, so it can only
// ever shorten or break a match — never build a `://` that was not there.
func logField(s string) string {
	return logsafe.Field(redactCredentials(s))
}

// clientBlock prepares multi-line git output for a client that renders it as a
// transcript: redact, defuse, redact AGAIN, then cap with a marker.
//
// The SECOND pass is required here and nowhere else. sanitize.Output DELETES
// hidden runes rather than replacing them, so it can CONSTRUCT a match the first
// pass could not see: `https:/<U+200B>/user:tok@host/x` carries no literal `://`
// until the zero-width space is removed, and then it does. Do not "align" the
// three helpers by adding or removing a pass — each one's count follows from what
// its sanitizer does to the bytes.
//
// Multi-line is preserved deliberately: git output is legitimately several lines
// and the client renders it as such, so flattening it here would damage the
// payload rather than protect anything.
func clientBlock(s string) string {
	out := sanitize.Output(redactCredentials(s))
	out = redactCredentials(out)
	if len(out) <= maxClientOutputBytes {
		return out
	}
	return runesafe.CapBytes(out, maxClientOutputBytes) + clientOutputTruncated
}

// clientLine prepares a single-line value a human makes a decision from — a
// remote URL — for a client: redact, flatten, cap.
//
// One pass, for logField's reason: runesafe's single-line preset replaces rather
// than deletes.
func clientLine(s string) string {
	return runesafe.SanitizeSingleLineBounded(redactCredentials(s), maxRemoteURLBytes)
}

// redactCredentials strips credentials from a git subprocess output string.
// Idempotent: chained userinfo segments (`http://a@b@c@host`) are
// consumed until the match set stabilises. The regex strictly shrinks
// the string on every match (each iteration removes at least one
// `@segment`), so the loop is bounded by input length with no DoS risk.
//
// All three patterns are confined to a single LINE by their real producer — git
// prints a URL on one line, and neither a '\r' nor a '\n' appears inside one — so
// every truncation in this package must land on a line boundary or a pattern can
// straddle the cut and the credential survives. cappedBuffer holds that invariant
// on the transfer path; see its doc comment.
//
// Not called directly anywhere but the three helpers above: its result is
// neither bounded nor single-line, and a sink needs both.
func redactCredentials(s string) string {
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
//   - `filter.<driver>.clean` / `.smudge` CANNOT be disabled generically. A
//     driver can be cleared BY NAME (`git -c filter.pwn.smudge=` does suppress
//     it, measured on git 2.47.3), but there is no --no-filter flag and no
//     wildcard form — `git -c 'filter.*.smudge='` still runs the driver, also
//     measured — so nothing here can neutralise a name it does not know. A repo
//     carrying both a .git/config entry and a .gitattributes line that selects
//     the driver runs that command. Clearing GIT_CONFIG_COUNT does not touch it
//     — that only blocks INLINE config from a parent process, and this one is on
//     disk. The exposure is real and stated rather than papered over: opening an
//     untrusted repo in this app can execute code from it.
//
//     WHICH operations trigger it, measured rather than assumed, because the
//     answer decides whether a user action is needed: `git status` runs the
//     clean filter on a filtered path whose stat info has changed, `git diff`
//     runs it (twice), `git add` runs it, and a checkout runs the smudge side.
//     `git show <rev>:<path>` does NOT — it hands back the stored blob. So the
//     dashboard's own periodic status poll is enough; this is not gated on the
//     user opening a diff, which is how an earlier version of this comment read.
//
//     A probe-and-refuse was considered and DECLINED (2026-09). It buys no
//     boundary: the only principal that can write a workspace .git/config in
//     this container already holds unrestricted shell execution at the same uid
//     through `!cmd`, reached from the same caller that posts to this surface.
//     And it costs a git panel that refuses status, diff, commit and checkout
//     for a legitimately configured repo with no manual fallback — `git lfs
//     install --local` writes exactly these three keys.
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
		return refuseExec(ctx, dir)
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
		// Without this git C-quotes any path holding a non-ASCII byte, so
		// café.txt reaches a caller as "caf\303\251.txt". The status parser is
		// unaffected either way (it reads -z, which emits paths verbatim — see
		// parseGitStatusOutput), so what this actually repairs is the non--z
		// output that reaches the model: handlers_ai.go builds commit messages,
		// PR descriptions and branch names from `status --porcelain` and
		// `diff --no-textconv`, and an escaped filename there ends up written
		// into a commit message the user keeps.
		//
		// It does not make quoting unreachable: a path containing a literal
		// double quote, a newline or a control byte is still quoted, so no
		// parser may drop its quote handling on the strength of this flag.
		"-c", "core.quotePath=false",
	}, args...)
	// argv[0] is an absolute path from internal/systembin's fixed system-directory
	// set, NOT the bare name: PATH[0] in this image is the toolbelt engine's link
	// directory on the persistent volume, so a bare `git` would let a file planted
	// there be executed by the SERVER on its own timers — outside Cedar, with no
	// user present, surviving container recreation. A miss refuses rather than
	// falling back, because one site's fallback voids the pin everywhere.
	gitBin, ok := resolveGitBinary()
	if !ok {
		slog.Error("git binary not found in the trusted system directories; refusing to spawn")
		return refuseExec(ctx, dir)
	}
	// The directive below also suppresses the unused-directive check: golangci-lint's
	// gosec integration reports this line nondeterministically (at the pinned 2.13.1
	// G702 appeared on 6 of 8 cold runs, and a silent run fails the build for an
	// unused directive). Standalone gosec is deterministic here, so the instability
	// is the integration's, and deleting the directive only swaps which half of the
	// flip goes red. Never open a comment line with the token that names that check:
	// gocritic's whyNoLint reads it as a second directive.
	//nolint:gosec,nolintlint // G702: the subcommand is checked against allowedSubcommands above and argv[0] is an absolute path from a fixed system-directory set that reads no environment; every remaining argv element is a separate token to execve with no shell, and the ref/path-shaped ones are validated at the handler boundary (isValidGitRef, validateFilePath, resolveRepoDir)
	cmd := exec.CommandContext(ctx, gitBin, hardenedArgs...)
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

// refuseExec builds a command that fails without launching git, shared by
// gitExec's two refusals (a disallowed subcommand, and a git binary absent from
// the trusted system directories). /bin/false always exits 1, and this
// deliberately spawns no shell: giving it an argv it could interpolate would
// hand a command-injection taint path to the boundary the refusal exists for.
//
// It produces no OUTPUT, which is why both refusals are ALSO reported one layer
// up in gitCmd: a caller composing empty output into its own message rendered a
// bare "clean:" naming no cause.
func refuseExec(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/false")
	cmd.Dir = dir
	return cmd
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
	// Reported here for the same reason as the subcommand refusal: gitExec's own
	// refusal is a silent exit 1, so without this a caller renders a message
	// naming no cause. No path is echoed — an operator reads the Error line
	// gitExec logs, and the response body says only that git is unavailable.
	if _, ok := resolveGitBinary(); !ok {
		return "", errGitUnavailable
	}
	out, err := gitExec(ctx, dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// splitRemote extracts the host and repository path from an https or
// scp-style git remote URL. The path is normalized to the forge's own
// spelling of a repository: no leading or trailing slash, no ".git".
//
//	https://github.com/foo/bar.git     → github.com, foo/bar
//	git@github.com:foo/bar.git         → github.com, foo/bar
//	ssh://git@gitlab.com/grp/sub/bar   → gitlab.com, grp/sub/bar
//
// Both halves come from ONE parse deliberately: a caller building a web URL
// needs them to describe the same remote, and two independent parses can
// disagree about which branch a given string took.
//
// ok is false for a shape neither parseSCPStyle nor url.Parse recognises and
// for a host sanitizeHost rejects. repoPath may still be empty when ok is
// true (a remote naming only a host), so a caller that needs it checks.
func splitRemote(raw string) (host, repoPath string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	h, p, scp := parseSCPStyle(raw)
	if !scp {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}
		h, p = u.Hostname(), u.Path
	}
	h = sanitizeHost(h)
	if h == "" {
		return "", "", false
	}
	return h, strings.TrimSuffix(strings.Trim(p, "/"), ".git"), true
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
	host, _, _ := splitRemote(raw)
	return host
}

// commitURLPrefix derives the forge web location a commit hash appends to,
// from a repository's origin remote. The result always ends in "/", so a
// caller builds a commit link by appending the hash and nothing else.
//
//	https://github.com/foo/bar.git     → https://github.com/foo/bar/commit/
//	git@github.com:foo/bar.git         → https://github.com/foo/bar/commit/
//	ssh://git@gitlab.com/foo/bar.git   → https://gitlab.com/foo/bar/-/commit/
//
// Returns "" when no https location can be derived: no remote, a shape
// splitRemote does not recognise, no repository path, or a result that is not
// a well-formed https URL. The client renders a plain hash then, which is the
// honest answer — a guess is a link to the wrong page.
func commitURLPrefix(remote string) string {
	host, repoPath, ok := splitRemote(remote)
	if !ok || repoPath == "" {
		return ""
	}
	// GitLab nests every repository page under a "/-/" separator, so a group
	// path can never be read as a page name. Classified the way prRefShape
	// classifies the same two families; keep the two in step rather than
	// growing a second host table.
	shape := "/commit/"
	if strings.Contains(host, "gitlab") {
		shape = "/-/commit/"
	}
	prefix := "https://" + host + "/" + repoPath + shape
	// Round-trip what is about to be handed to a browser. sanitizeHost admits
	// characters url.Parse refuses in a host, and a path carrying "?" or "#"
	// would silently truncate the link so it pointed somewhere other than the
	// commit — a wrong link is worse than no link.
	u, err := url.Parse(prefix)
	if err != nil || u.Scheme != "https" || u.Host != host || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return prefix
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
