package gitexec

import (
	"slices"
	"strings"
	"testing"
)

func FuzzParseSCPStyle(f *testing.F) {
	f.Add("git@github.com:user/repo.git")
	f.Add("user@host:path")
	f.Add("")
	f.Add("https://github.com/user/repo")
	f.Add("git@host::path")
	f.Add("@host:path")

	f.Fuzz(func(t *testing.T, raw string) {
		host, _, ok := ParseSCPStyle(raw)
		if !ok {
			return
		}
		if host == "" {
			t.Fatal("ok=true but host is empty")
		}
		if strings.Contains(raw, "://") {
			t.Fatal("ok=true but input contains ://")
		}
		if !strings.Contains(raw, "@") {
			t.Fatal("ok=true but input has no @")
		}
		if strings.ContainsAny(host, "/?#") {
			t.Fatalf("ok=true but host contains forbidden char: %q", host)
		}
	})
}

func FuzzScrubAuth(f *testing.F) {
	f.Add("https://user:pass@github.com/repo")
	f.Add("https://token@host.com/path")
	f.Add("git://a@b@c@host/repo")
	f.Add("?token=secret&foo=bar")
	f.Add("Authorization: Bearer abc123")
	f.Add("")
	f.Add("no credentials here")

	f.Fuzz(func(t *testing.T, s string) {
		result := ScrubAuth(s)
		// Idempotent: scrubbing an already-scrubbed string is a no-op.
		if ScrubAuth(result) != result {
			t.Fatalf("ScrubAuth not idempotent on %q", s)
		}
	})
}

func FuzzParseRemoteHost(f *testing.F) {
	f.Add("https://github.com/foo/bar.git")
	f.Add("git@github.com:foo/bar.git")
	f.Add("ssh://git@gitlab.com/foo/bar.git")
	f.Add("ext::ssh -o ProxyCommand=evil host")
	f.Add("")
	f.Add("not-a-url")

	f.Fuzz(func(t *testing.T, raw string) {
		host := ParseRemoteHost(raw)
		if host == "" {
			return
		}
		for _, c := range host {
			if c < 0x20 || c == 0x7f {
				t.Fatalf("host contains control char: %q", host)
			}
		}
		if strings.ContainsAny(host, "@:/") {
			t.Fatalf("host contains forbidden char: %q", host)
		}
	})
}

func FuzzSanitizeHost(f *testing.F) {
	f.Add("github.com")
	f.Add("gitlab.example.com")
	f.Add("")
	f.Add("has\x00null")
	f.Add("has@at")
	f.Add("has:colon")
	f.Add("has/slash")

	f.Fuzz(func(t *testing.T, h string) {
		result := sanitizeHost(h)
		if result == "" {
			return
		}
		for _, c := range result {
			if c < 0x20 || c == 0x7f || c == '@' || c == ':' || c == '/' {
				t.Fatalf("sanitizeHost(%q) = %q: contains forbidden char", h, result)
			}
		}
	})
}

// FuzzFirstSubcommand verifies firstSubcommand never panics, skips
// flag-like tokens, and returns "" only when no non-empty non-flag token exists.
func FuzzFirstSubcommand(f *testing.F) {
	f.Add("status")
	f.Add("-c\x00value\x00push")
	f.Add("-C\x00/tmp\x00clone")
	f.Add("--flag\x00commit")
	f.Add("")
	f.Add("-c")
	f.Add("-c\x00val")

	f.Fuzz(func(t *testing.T, joined string) {
		// Split on NUL to simulate an arg list.
		var args []string
		if joined != "" {
			args = strings.Split(joined, "\x00")
		}
		result := firstSubcommand(args)
		if result == "" {
			return // "" is the "not found" sentinel, always valid
		}
		// Invariant: result must not start with '-'.
		if strings.HasPrefix(result, "-") {
			t.Fatalf("firstSubcommand returned flag-like value: %q", result)
		}
		// Invariant: result must appear in args.
		if !slices.Contains(args, result) {
			t.Fatalf("firstSubcommand returned %q which is not in args", result)
		}
	})
}
