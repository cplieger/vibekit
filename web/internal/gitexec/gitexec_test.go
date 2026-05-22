package gitexec

import (
	"context"
	"errors"
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

func TestScrubAuthErr(t *testing.T) {
	t.Parallel()

	t.Run("nil_error", func(t *testing.T) {
		t.Parallel()
		if got := ScrubAuthErr(nil); got != "" {
			t.Errorf("ScrubAuthErr(nil) = %q, want empty", got)
		}
	})

	t.Run("error_with_credentials", func(t *testing.T) {
		t.Parallel()
		err := errors.New("failed: https://user:pass@host.com/repo.git")
		got := ScrubAuthErr(err)
		want := "failed: https://host.com/repo.git"
		if got != want {
			t.Errorf("ScrubAuthErr(%v)\n got: %q\nwant: %q", err, got, want)
		}
	})
}

func TestCmd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := Cmd(ctx, "/tmp", "status")

	if cmd.Dir != "/tmp" {
		t.Errorf("Cmd.Dir = %q, want /tmp", cmd.Dir)
	}
	if cmd.Args[0] != "git" || cmd.Args[1] != "status" {
		t.Errorf("Cmd.Args = %v, want [git status]", cmd.Args)
	}

	// Verify hardening env vars are set.
	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := splitEnvVar(e)
		if parts[0] != "" {
			envMap[parts[0]] = parts[1]
		}
	}

	wantEnv := map[string]string{
		"GIT_TERMINAL_PROMPT":    "0",
		"GIT_ASKPASS":            "",
		"SSH_ASKPASS":            "",
		"GIT_PROTOCOL_FROM_USER": "0",
		"GIT_CONFIG_COUNT":       "",
		"GIT_CONFIG_GLOBAL":      "/dev/null",
		"GIT_CONFIG_SYSTEM":      "/dev/null",
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
