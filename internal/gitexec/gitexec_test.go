package gitexec

import (
	"context"
	"testing"
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
			got := ScrubAuth(tt.in)
			if got != tt.want {
				t.Errorf("ScrubAuth(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCmd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := Cmd(ctx, "/tmp", "status")

	if cmd.Dir != "/tmp" {
		t.Errorf("Cmd.Dir = %q, want /tmp", cmd.Dir)
	}
	// Args now include the prepended -c protocol.ext.allow=never
	// hardening flags. Shape: [git -c protocol.ext.allow=never status].
	wantArgs := []string{"git", "-c", "protocol.ext.allow=never", "status"}
	if len(cmd.Args) != len(wantArgs) {
		t.Errorf("Cmd.Args length = %d, want %d (%v)", len(cmd.Args), len(wantArgs), cmd.Args)
	} else {
		for i, w := range wantArgs {
			if cmd.Args[i] != w {
				t.Errorf("Cmd.Args[%d] = %q, want %q", i, cmd.Args[i], w)
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
			t.Errorf("env %s = %q must not be set by Cmd (would disable credential helpers from ~/.gitconfig)", k, got)
		}
	}
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

// ParseRemoteHost extracts the host from a well-formed https remote URL.
func TestParseRemoteHost_extractsHostFromURL(t *testing.T) {
	t.Parallel()
	const in = "https://github.com/foo/bar.git"
	if got := ParseRemoteHost(in); got != "github.com" {
		t.Errorf("ParseRemoteHost(%q) = %q, want %q", in, got, "github.com")
	}
}

// A URL that url.Parse rejects (a NUL control byte routes past the
// scp-style branch into url.Parse, which errors) yields "" rather than
// a panic on a nil *url.URL.
func TestParseRemoteHost_parseErrorReturnsEmpty(t *testing.T) {
	t.Parallel()
	const in = "http://ho\x00st.com"
	if got := ParseRemoteHost(in); got != "" {
		t.Errorf("ParseRemoteHost(%q) = %q, want empty", in, got)
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

// A leading '@' means an empty user, which ParseSCPStyle must reject.
func TestParseSCPStyle_rejectsEmptyUser(t *testing.T) {
	t.Parallel()
	const in = "@host:path"
	if _, _, ok := ParseSCPStyle(in); ok {
		t.Errorf("ParseSCPStyle(%q) ok = true, want false (leading '@' = empty user)", in)
	}
}

// ParseSCPStyle splits user@host:path, returning the host and path after
// the '@' for a valid scp-style remote.
func TestParseSCPStyle_extractsHostAfterAt(t *testing.T) {
	t.Parallel()
	const in = "git@github.com:foo"
	host, path, ok := ParseSCPStyle(in)
	if !ok {
		t.Fatalf("ParseSCPStyle(%q) ok = false, want true", in)
	}
	if host != "github.com" {
		t.Errorf("ParseSCPStyle(%q) host = %q, want %q", in, host, "github.com")
	}
	if path != "foo" {
		t.Errorf("ParseSCPStyle(%q) path = %q, want %q", in, path, "foo")
	}
}
