package steering

import (
	"testing"
	"unicode/utf8"
)

// FuzzTruncateUTF8 verifies truncateUTF8 never splits multi-byte runes and
// always returns valid UTF-8 for valid UTF-8 input.
//
// Bug class: off-by-one in continuation byte walk producing invalid UTF-8.
func FuzzTruncateUTF8(f *testing.F) {
	f.Add("hello", 3)
	f.Add("日本語テスト", 6)
	f.Add("", 0)
	f.Add("abc", 100)
	f.Add("🎉🎊🎈", 4)
	f.Add("\xff\xfe\xfd", 2)

	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 0 {
			return
		}
		result := truncateUTF8(s, n)

		// Invariant 1: length never exceeds n.
		if len(result) > n {
			t.Fatalf("truncateUTF8(%q, %d) = %q; len %d > %d", s, n, result, len(result), n)
		}

		// Invariant 2: if input is valid UTF-8, output must be valid UTF-8.
		if utf8.ValidString(s) && !utf8.ValidString(result) {
			t.Fatalf("truncateUTF8(%q, %d) = %q; invalid UTF-8 from valid input", s, n, result)
		}

		// Invariant 3: result is a prefix of s.
		if len(result) > 0 && s[:len(result)] != result {
			t.Fatalf("truncateUTF8(%q, %d) = %q; not a byte prefix", s, n, result)
		}

		// Invariant 4: if len(s) <= n, return s unchanged.
		if len(s) <= n && result != s {
			t.Fatalf("truncateUTF8(%q, %d) = %q; should be unchanged", s, n, result)
		}
	})
}

// FuzzIsMarkdownHeading verifies isMarkdownHeading never panics and returns
// true only for valid ATX headings (1-6 # followed by space/tab/EOL).
//
// Bug class: index out of range on multi-byte first rune, false positives.
func FuzzIsMarkdownHeading(f *testing.F) {
	f.Add("# Heading")
	f.Add("## Sub")
	f.Add("###### Deep")
	f.Add("####### TooMany")
	f.Add("")
	f.Add("#")
	f.Add("not a heading")
	f.Add("#\t")

	f.Fuzz(func(t *testing.T, line string) {
		result := isMarkdownHeading(line)

		// Invariant 1: empty string is always false.
		if line == "" && result {
			t.Fatal("isMarkdownHeading(\"\") should be false")
		}

		// Invariant 2: if true, must start with # and have 1-6 # chars.
		if result {
			if line[0] != '#' {
				t.Fatalf("isMarkdownHeading(%q) = true but doesn't start with #", line)
			}
			hashes := 0
			for hashes < len(line) && line[hashes] == '#' {
				hashes++
			}
			if hashes > 6 {
				t.Fatalf("isMarkdownHeading(%q) = true with %d hashes", line, hashes)
			}
		}
	})
}

// FuzzKindFromHost verifies kindFromHost returns one of the known forge kinds
// or empty string, and never panics.
//
// Bug class: false positive classifications from overlapping substring matches.
func FuzzKindFromHost(f *testing.F) {
	f.Add("github.com")
	f.Add("gitlab.com")
	f.Add("codeberg.org")
	f.Add("gitea.example.com")
	f.Add("")
	f.Add("example.com")
	f.Add("mygithubclone.org")

	f.Fuzz(func(t *testing.T, host string) {
		result := kindFromHost(host)

		// Invariant: result is one of the known values.
		switch result {
		case "", "github", "gitlab", "codeberg", "gitea":
			// ok
		default:
			t.Fatalf("kindFromHost(%q) = %q; not a known kind", host, result)
		}
	})
}
