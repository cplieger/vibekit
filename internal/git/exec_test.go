package git

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScrubAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// Empty / no-op
		{name: "empty", in: "", want: ""},
		{name: "no_credentials", in: "https://github.com/foo/bar.git", want: "https://github.com/foo/bar.git"},

		// urlCredPattern: scheme://user:pwd@host
		{name: "https_userinfo", in: "https://user:secret@github.com/repo.git", want: "https://github.com/repo.git"},
		{name: "http_token_only", in: "http://ghp_abc123@github.com/repo.git", want: "http://github.com/repo.git"},
		{name: "ssh_scheme_userinfo", in: "ssh://deploy:key@gitlab.com/repo.git", want: "ssh://gitlab.com/repo.git"},
		{name: "git_scheme_userinfo", in: "git://token@example.com/repo.git", want: "git://example.com/repo.git"},
		{name: "chained_userinfo", in: "http://a@b@c@host.com/path", want: "http://host.com/path"},

		// urlQueryTokenPattern: ?token=, ?access_token=, etc.
		{name: "query_token", in: "https://gitea.io/repo?token=abc123", want: "https://gitea.io/repo?token=[REDACTED]"},
		{name: "query_access_token", in: "https://host.com/r?access_token=xyz", want: "https://host.com/r?access_token=[REDACTED]"},
		{name: "query_private_token", in: "https://gl.io/r?private_token=secret", want: "https://gl.io/r?private_token=[REDACTED]"},
		{name: "query_api_key", in: "https://h.io/r?api_key=k1&other=v", want: "https://h.io/r?api_key=[REDACTED]&other=v"},
		{name: "query_apikey", in: "https://h.io/r?apikey=k2", want: "https://h.io/r?apikey=[REDACTED]"},

		// authHeaderPattern: Authorization: Bearer/Token/Basic
		{name: "auth_bearer", in: "Authorization: Bearer ghp_secret123", want: "Authorization: Bearer [REDACTED]"},
		{name: "auth_token", in: "authorization: token abc", want: "authorization: token [REDACTED]"},
		{name: "auth_basic", in: "Authorization: Basic dXNlcjpwYXNz", want: "Authorization: Basic [REDACTED]"},

		// Combined patterns
		{name: "userinfo_and_query", in: "https://user:pw@host.com/r?token=s", want: "https://host.com/r?token=[REDACTED]"},

		// Embedded in surrounding text — the shape every caller actually
		// passes (git stderr / an error message, not a bare URL).
		{
			name: "userinfo_inside_message",
			in:   "failed: https://user:pass@host.com/repo.git",
			want: "failed: https://host.com/repo.git",
		},

		// Idempotency
		{name: "already_scrubbed", in: "https://github.com/repo.git", want: "https://github.com/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scrubAuth(tt.in)
			if got != tt.want {
				t.Errorf("scrubAuth(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGitExec_Args(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cmd := gitExec(ctx, "/tmp", "status")

	if cmd.Dir != "/tmp" {
		t.Errorf("gitExec.Dir = %q, want /tmp", cmd.Dir)
	}
	// Args include the prepended -c hardening pairs, before the subcommand.
	wantArgs := []string{
		"git",
		"-c", "protocol.ext.allow=never",
		"-c", "core.fsmonitor=",
		"-c", "core.quotePath=false",
		"status",
	}
	if len(cmd.Args) != len(wantArgs) {
		t.Errorf("gitExec.Args length = %d, want %d (%v)", len(cmd.Args), len(wantArgs), cmd.Args)
	} else {
		for i, w := range wantArgs {
			if cmd.Args[i] != w {
				t.Errorf("gitExec.Args[%d] = %q, want %q", i, cmd.Args[i], w)
			}
		}
	}

	// Verify hardening env vars are set.
	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := splitEnvVar(e)
		if parts[0] != "" {
			envMap[parts[0]] = parts[1]
		}
	}

	// GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM are NOT pinned to
	// /dev/null any more — they need to be loadable so the forge
	// CLI's credential.helper line in ~/.gitconfig works for HTTPS
	// clones of private repos. The ext:: hardening moved to a
	// command-line -c flag (verified above), which beats gitconfig.
	wantEnv := map[string]string{
		"GIT_TERMINAL_PROMPT":    "0",
		"GIT_ASKPASS":            "",
		"SSH_ASKPASS":            "",
		"GIT_PROTOCOL_FROM_USER": "0",
		"GIT_CONFIG_COUNT":       "",
		"GIT_CONFIG_PARAMETERS":  "",
	}
	for k, want := range wantEnv {
		got, ok := envMap[k]
		if !ok {
			t.Errorf("env %s not set", k)
		} else if got != want {
			t.Errorf("env %s = %q, want %q", k, got, want)
		}
	}
	// Explicitly verify the gitconfig file vars are NOT pinned:
	// loading them is required so credential helpers work.
	for _, k := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if got, ok := envMap[k]; ok {
			t.Errorf("env %s = %q must not be set by gitExec (would disable credential helpers from ~/.gitconfig)", k, got)
		}
	}
}

// core.fsmonitor names a command git runs on status and diff, and a repo's own
// .git/config can set it. A command-line -c always beats gitconfig, so clearing
// it centrally is what makes it unreachable — on EVERY subcommand, since the
// caller's argv is what varies and the hardening is not per call site.
//
// Asserted as a POSITION rather than mere membership: the pairs must precede the
// subcommand, because `git status -c x=y` is not a config flag at all, it is an
// argument to status.
func TestGitExec_ClearsConfigDrivenExecution(t *testing.T) {
	t.Parallel()

	for _, sub := range []string{"status", "diff", "log", "commit", "push"} {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()
			args := gitExec(t.Context(), "/tmp", sub, "--porcelain").Args
			var found bool
			for i := 0; i+1 < len(args); i++ {
				if args[i] != "-c" || args[i+1] != "core.fsmonitor=" {
					continue
				}
				found = true
				if idx := indexOf(args, sub); idx >= 0 && idx < i {
					t.Errorf("core.fsmonitor= at %d is after the subcommand at %d: %v", i, idx, args)
				}
			}
			if !found {
				t.Errorf("Args = %v, want a `-c core.fsmonitor=` pair", args)
			}
		})
	}
}

// Without core.quotePath=false git C-quotes every path holding a byte above
// 0x80, so café.txt reaches a caller as "caf\303\251.txt". Independent of the
// locale: the image's LANG=C.UTF-8 does not change git's path quoting.
//
// The status parser is unaffected either way — it reads -z, which emits paths
// verbatim — so what this pins is the non--z output handlers_ai.go folds into a
// prompt, where an escaped filename ends up written into a commit message.
//
// Positioned like the pair above, and for the same reason: after the subcommand
// it is an argument to it rather than a config flag.
func TestGitExec_DisablesPathQuoting(t *testing.T) {
	t.Parallel()

	for _, sub := range []string{"status", "diff", "log"} {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()
			args := gitExec(t.Context(), "/tmp", sub, "--porcelain").Args
			var found bool
			for i := 0; i+1 < len(args); i++ {
				if args[i] != "-c" || args[i+1] != "core.quotePath=false" {
					continue
				}
				found = true
				if idx := indexOf(args, sub); idx >= 0 && idx < i {
					t.Errorf("core.quotePath=false at %d is after the subcommand at %d: %v", i, idx, args)
				}
			}
			if !found {
				t.Errorf("Args = %v, want a `-c core.quotePath=false` pair", args)
			}
		})
	}
}

// A refused subcommand must not gain the hardening flags: it never launches git
// at all, so a flag there would only make the rigged-to-fail command look like a
// real invocation.
func TestGitExec_RefusedSubcommandGetsNoHardening(t *testing.T) {
	t.Parallel()

	args := gitExec(t.Context(), "/tmp", "cat-file", "-p", "HEAD").Args
	for _, a := range args {
		if a == "core.fsmonitor=" || a == "protocol.ext.allow=never" {
			t.Errorf("Args = %v, want no hardening on the refusal path", args)
		}
	}
}

// A refused subcommand must fail with a message that NAMES it. The refusal
// path used to run /bin/false, which exits 1 and writes nothing to either
// stream, so a caller composing the output into its own message produced a
// string ending at its own colon ("clean: ") that named no cause and read as
// truncated. That is exactly how the missing `clean` entry presented, so the
// output is asserted rather than only the exit status.
func TestGitCmd_RefusedSubcommandSaysWhy(t *testing.T) {
	t.Parallel()

	out, err := gitCmd(t.Context(), t.TempDir(), "cat-file", "-p", "HEAD")
	if err == nil {
		t.Fatalf("gitCmd(cat-file) err = nil, want a refusal (out %q)", out)
	}
	// The reason travels in the ERROR, and no subprocess is spawned: giving the
	// refusal branch a shell it could interpolate the name into would hand a
	// command-injection taint path to the boundary the allowlist exists to close.
	if !strings.Contains(err.Error(), "cat-file") {
		t.Errorf("gitCmd(cat-file) err = %v, want it to name the refused subcommand", err)
	}
	// And cmdFailure is what turns it into something a user can read, since the
	// subprocess output is empty on this path.
	if got := cmdFailure(out, err); !strings.Contains(got, "cat-file") {
		t.Errorf("cmdFailure(%q, %v) = %q, want it to name the subcommand", out, err, got)
	}
}

// cmdFailure stands in the exit status when the subprocess said nothing, so a
// caller that interpolates it can never render a message ending at its colon.
func TestCmdFailure_FallsBackToExitStatus(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("exit status 1")
	cases := map[string]struct{ out, want string }{
		"output wins":           {"fatal: pathspec did not match", "fatal: pathspec did not match"},
		"empty falls back":      {"", "exit status 1"},
		"whitespace falls back": {"  \n\t ", "exit status 1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := cmdFailure(tc.out, sentinel); got != tc.want {
				t.Errorf("cmdFailure(%q, %v) = %q, want %q", tc.out, sentinel, got, tc.want)
			}
		})
	}
}

// Every subcommand this package builds argv from must be allowlisted, or
// gitExec substitutes a failing command and the feature is silently dead.
// `clean` shipped missing, which broke "Discard all" for untracked files.
func TestAllowedSubcommands_CoversEveryConstant(t *testing.T) {
	t.Parallel()

	for _, sub := range []string{subAdd, subCheckout, subClean, subFetch, subRemote, subReset} {
		if _, ok := allowedSubcommands[sub]; !ok {
			t.Errorf("allowedSubcommands is missing %q; gitExec refuses it", sub)
		}
	}
}

func indexOf(hay []string, needle string) int {
	for i, s := range hay {
		if s == needle {
			return i
		}
	}
	return -1
}

// splitEnvVar splits "KEY=VALUE" into [KEY, VALUE]. Handles empty values.
func splitEnvVar(s string) [2]string {
	i := 0
	for i < len(s) && s[i] != '=' {
		i++
	}
	if i == len(s) {
		return [2]string{s, ""}
	}
	return [2]string{s[:i], s[i+1:]}
}

// firstSubcommand must skip the value argument that follows a -c/-C flag,
// so the value is never mistaken for the git subcommand.
func TestFirstSubcommand_skipsFlagValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
		args []string
	}{
		// "-c" consumes its value ("status"); the real subcommand is "commit".
		{name: "dash_c_skips_value", args: []string{"-c", "status", "commit"}, want: "commit"},
		// "-C" consumes its value ("somedir"); the real subcommand is "log".
		{name: "dash_C_skips_value", args: []string{"-C", "somedir", "log"}, want: "log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := firstSubcommand(tt.args); got != tt.want {
				t.Errorf("firstSubcommand(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// parseRemoteHost extracts the host from a well-formed https remote URL.
func TestParseRemoteHost_extractsHostFromURL(t *testing.T) {
	t.Parallel()
	const in = "https://github.com/foo/bar.git"
	if got := parseRemoteHost(in); got != "github.com" {
		t.Errorf("parseRemoteHost(%q) = %q, want %q", in, got, "github.com")
	}
}

// A URL that url.Parse rejects (a NUL control byte routes past the
// scp-style branch into url.Parse, which errors) yields "" rather than
// a panic on a nil *url.URL.
func TestParseRemoteHost_parseErrorReturnsEmpty(t *testing.T) {
	t.Parallel()
	const in = "http://ho\x00st.com"
	if got := parseRemoteHost(in); got != "" {
		t.Errorf("parseRemoteHost(%q) = %q, want empty", in, got)
	}
}

// sanitizeHost returns "" when the host contains any control byte, DEL,
// '@', ':' or '/'; a space (0x20) and an otherwise-clean host pass through.
func TestSanitizeHost_rejectsForbiddenChars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "control_low", in: "\x01", want: ""},
		{name: "space_allowed", in: "a b", want: "a b"},
		{name: "del_0x7f", in: "\x7f", want: ""},
		{name: "at_sign", in: "@", want: ""},
		{name: "colon", in: ":", want: ""},
		{name: "slash", in: "/", want: ""},
		{name: "clean_host", in: "github.com", want: "github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeHost(tt.in); got != tt.want {
				t.Errorf("sanitizeHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A leading '@' means an empty user, which parseSCPStyle must reject.
func TestParseSCPStyle_rejectsEmptyUser(t *testing.T) {
	t.Parallel()
	const in = "@host:path"
	if _, _, ok := parseSCPStyle(in); ok {
		t.Errorf("parseSCPStyle(%q) ok = true, want false (leading '@' = empty user)", in)
	}
}

// parseSCPStyle splits user@host:path, returning the host and path after
// the '@' for a valid scp-style remote.
func TestParseSCPStyle_extractsHostAfterAt(t *testing.T) {
	t.Parallel()
	const in = "git@github.com:foo"
	host, path, ok := parseSCPStyle(in)
	if !ok {
		t.Fatalf("parseSCPStyle(%q) ok = false, want true", in)
	}
	if host != "github.com" {
		t.Errorf("parseSCPStyle(%q) host = %q, want %q", in, host, "github.com")
	}
	if path != "foo" {
		t.Errorf("parseSCPStyle(%q) path = %q, want %q", in, path, "foo")
	}
}

// Every operation class in the default policy gets a usable budget, and the
// budget widens with how much work the operation does: a zero or collapsed
// budget would abort the subprocess before git had a chance to run at all.
func TestDefaultTimeouts_budgetsEachOperationClass(t *testing.T) {
	t.Parallel()
	policy := defaultTimeouts()
	tests := []struct {
		field string
		got   time.Duration
		want  time.Duration
	}{
		{field: "Plumbing", got: policy.Plumbing, want: 5 * time.Second},
		{field: "Fetch", got: policy.Fetch, want: 5 * time.Second},
		{field: "Push", got: policy.Push, want: 60 * time.Second},
		{field: "Clone", got: policy.Clone, want: 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("defaultTimeouts().%s = %v, want %v", tt.field, tt.got, tt.want)
			}
		})
	}
}

// splitRemote returns the host and the normalized repository path from one
// parse, for both remote spellings this surface accepts. The ".git" suffix and
// the surrounding slashes are the forge's business, not the clone URL's.
func TestSplitRemote_normalizesHostAndPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		wantHost string
		wantPath string
		wantOK   bool
	}{
		{name: "https_dot_git", in: "https://github.com/foo/bar.git", wantHost: "github.com", wantPath: "foo/bar", wantOK: true},
		{name: "https_no_suffix", in: "https://codeberg.org/foo/bar", wantHost: "codeberg.org", wantPath: "foo/bar", wantOK: true},
		{name: "https_trailing_slash", in: "https://github.com/foo/bar/", wantHost: "github.com", wantPath: "foo/bar", wantOK: true},
		{name: "scp_style", in: "git@github.com:foo/bar.git", wantHost: "github.com", wantPath: "foo/bar", wantOK: true},
		{name: "ssh_scheme", in: "ssh://git@gitlab.com/foo/bar.git", wantHost: "gitlab.com", wantPath: "foo/bar", wantOK: true},
		{name: "nested_group", in: "https://gitlab.com/grp/sub/bar.git", wantHost: "gitlab.com", wantPath: "grp/sub/bar", wantOK: true},
		{name: "host_only", in: "https://github.com", wantHost: "github.com", wantPath: "", wantOK: true},
		{name: "empty", in: "", wantHost: "", wantPath: "", wantOK: false},
		{name: "whitespace_only", in: "   ", wantHost: "", wantPath: "", wantOK: false},
		{name: "unparseable", in: "http://ho\x00st.com", wantHost: "", wantPath: "", wantOK: false},
		{name: "ext_helper", in: "ext::sh -c payload", wantHost: "", wantPath: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, repoPath, ok := splitRemote(tt.in)
			if host != tt.wantHost || repoPath != tt.wantPath || ok != tt.wantOK {
				t.Errorf("splitRemote(%q) = (%q, %q, %t), want (%q, %q, %t)",
					tt.in, host, repoPath, ok, tt.wantHost, tt.wantPath, tt.wantOK)
			}
		})
	}
}

// commitURLPrefix builds the forge location a commit hash appends to, or ""
// when it cannot build a well-formed https URL. GitLab's extra "/-/" segment
// is the one shape difference, and it is classified off the host the way
// prRefShape classifies the same two families.
func TestCommitURLPrefix_perForgeShapeElseEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "github_https", in: "https://github.com/foo/bar.git", want: "https://github.com/foo/bar/commit/"},
		{name: "github_scp", in: "git@github.com:foo/bar.git", want: "https://github.com/foo/bar/commit/"},
		{name: "gitea_no_suffix", in: "https://codeberg.org/foo/bar", want: "https://codeberg.org/foo/bar/commit/"},
		{name: "gitlab_ssh", in: "ssh://git@gitlab.com/foo/bar.git", want: "https://gitlab.com/foo/bar/-/commit/"},
		{name: "self_hosted_gitlab", in: "https://gitlab.example.com/grp/sub/bar.git", want: "https://gitlab.example.com/grp/sub/bar/-/commit/"},
		// An http remote still yields an https page: the forge web UI is not
		// the transport, and no forge serves its pages over cleartext.
		{name: "http_remote_upgrades", in: "http://gitea.internal/foo/bar.git", want: "https://gitea.internal/foo/bar/commit/"},
		// Credentials live in the userinfo segment, which neither parse keeps.
		{name: "userinfo_dropped", in: "https://user:tok@github.com/foo/bar.git", want: "https://github.com/foo/bar/commit/"},
		{name: "no_remote", in: "", want: ""},
		{name: "host_only", in: "https://github.com", want: ""},
		{name: "host_with_trailing_slash", in: "https://github.com/", want: ""},
		{name: "ext_helper", in: "ext::sh -c payload", want: ""},
		// sanitizeHost admits a space; url.Parse does not, so the round-trip
		// check is what refuses this rather than the host filter.
		{name: "space_in_host", in: "git@ev il.com:foo/bar.git", want: ""},
		// A "#" or "?" in the path would truncate the href so the link pointed
		// somewhere other than the commit.
		{name: "fragment_in_path", in: "git@github.com:foo/bar#x.git", want: ""},
		{name: "query_in_path", in: "git@github.com:foo/bar?x.git", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := commitURLPrefix(tt.in); got != tt.want {
				t.Errorf("commitURLPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
