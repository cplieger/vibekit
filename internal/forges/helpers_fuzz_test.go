package forges

import (
	"strings"
	"testing"
	"time"
)

func FuzzParseRepo(f *testing.F) {
	f.Add("owner/repo")
	f.Add("")
	f.Add("/repo")
	f.Add("owner/")
	f.Add("no-slash")
	f.Add("  owner/repo  ")

	f.Fuzz(func(t *testing.T, s string) {
		owner, name, err := ParseRepo(s)
		if err != nil {
			return
		}
		if owner == "" || name == "" {
			t.Fatal("err==nil but owner or name is empty")
		}
		want := owner + "/" + name
		if want != strings.TrimSpace(s) {
			t.Fatalf("reconstruction mismatch: got %q, want %q", want, strings.TrimSpace(s))
		}
	})
}

func FuzzExtractPRNumberFromURL(f *testing.F) {
	f.Add("https://github.com/owner/repo/pull/123")
	f.Add("https://gitlab.com/g/p/-/merge_requests/45")
	f.Add("https://codeberg.org/u/r/pulls/7")
	f.Add("")
	f.Add("no-match")

	f.Fuzz(func(t *testing.T, url string) {
		n := extractPRNumberFromURL(url)
		if n < 0 {
			t.Fatalf("returned negative: %d", n)
		}
	})
}

func FuzzExtractIssueNumberFromURL(f *testing.F) {
	f.Add("https://github.com/owner/repo/issues/42")
	f.Add("")
	f.Add("no-match")

	f.Fuzz(func(t *testing.T, url string) {
		n := extractIssueNumberFromURL(url)
		if n < 0 {
			t.Fatalf("returned negative: %d", n)
		}
	})
}

func FuzzParseRFC3339Millis(f *testing.F) {
	f.Add("2024-01-15T10:30:00Z")
	f.Add("2024-01-15T10:30:00+02:00")
	f.Add("2024-01-15T10:30:00")
	f.Add("")
	f.Add("not-a-date")

	f.Fuzz(func(t *testing.T, s string) {
		result := parseRFC3339Millis(s)
		if result != 0 {
			// Verify at least one parse path succeeds
			_, err1 := time.Parse(time.RFC3339, s)
			_, err2 := time.Parse("2006-01-02T15:04:05", s)
			if err1 != nil && err2 != nil {
				t.Fatalf("result!=0 but neither parse succeeds for %q", s)
			}
		}
	})
}

func FuzzNormalizePRState(f *testing.F) {
	f.Add("open")
	f.Add("CLOSED")
	f.Add("merged")
	f.Add("draft")
	f.Add("")
	f.Add("unknown-state")

	f.Fuzz(func(t *testing.T, s string) {
		result := normalizePRState(s)
		if result != strings.ToLower(result) {
			t.Fatalf("output not lowercase: %q", result)
		}
		// Idempotency check
		if normalizePRState(result) != result {
			t.Fatalf("not idempotent: normalizePRState(%q)=%q, normalizePRState(%q)=%q",
				s, result, result, normalizePRState(result))
		}
	})
}

func FuzzNormalizeIssueState(f *testing.F) {
	f.Add("open")
	f.Add("CLOSED")
	f.Add("close")
	f.Add("opened")
	f.Add("")
	f.Add("random")

	f.Fuzz(func(t *testing.T, s string) {
		result := normalizeIssueState(s)
		if result != strings.ToLower(result) {
			t.Fatalf("output not lowercase: %q", result)
		}
		// Idempotency check
		if normalizeIssueState(result) != result {
			t.Fatalf("not idempotent: normalizeIssueState(%q)=%q, normalizeIssueState(%q)=%q",
				s, result, result, normalizeIssueState(result))
		}
	})
}
