package gitexec

import (
	"strings"
	"testing"
)

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
		// Idempotent
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
