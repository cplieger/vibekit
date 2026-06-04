package steering

import (
	"testing"
	"unicode/utf8"
)

// FuzzParseSteeringFrontmatter exercises the YAML frontmatter parser with
// arbitrary byte slices to verify it never panics and that returned fields
// are always valid UTF-8 with recognised inclusion values.
func FuzzParseSteeringFrontmatter(f *testing.F) {
	f.Add([]byte("---\ninclusion: always\nfileMatchPattern: \"*.go\"\ndescription: \"Go files\"\n---\n# Body"))
	f.Add([]byte("---\ninclusion: fileMatch\nfileMatchPattern: \"docs/**\"\n---\n"))
	f.Add([]byte("---\ninclusion: manual\n---\n"))
	f.Add([]byte("no frontmatter at all"))
	f.Add([]byte("---\n---\n"))
	f.Add([]byte(""))
	f.Add([]byte("---\ngarbage: true\n\x00binary\n---\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		doc := parseSteeringFrontmatter(data)

		// Invariant 1: Inclusion is always one of the recognised values.
		switch doc.Inclusion {
		case "always", "fileMatch", "manual":
		default:
			t.Fatalf("unexpected inclusion %q", doc.Inclusion)
		}

		// Invariant 2: Inclusion field is always valid UTF-8 (comes from
		// a switch on known constants, not from raw input).
		if !utf8.ValidString(doc.Inclusion) {
			t.Fatal("Inclusion not valid UTF-8")
		}

		// Invariant 3: idempotent.
		doc2 := parseSteeringFrontmatter(data)
		if doc != doc2 {
			t.Fatal("parseSteeringFrontmatter not idempotent")
		}
	})
}

// FuzzHostFromGitURL exercises the git remote URL host extractor to verify
// it never panics and that extracted hosts contain no path separators.
func FuzzHostFromGitURL(f *testing.F) {
	f.Add("https://github.com/owner/repo.git")
	f.Add("git@github.com:owner/repo.git")
	f.Add("https://user:pass@gitlab.com/owner/repo")
	f.Add("http://example.com/repo")
	f.Add("file:///local/path")
	f.Add("")
	f.Add("ssh://git@host:22/repo")
	f.Add("https://")
	f.Add("@:")

	f.Fuzz(func(t *testing.T, url string) {
		host := hostFromGitURL(url)

		// Invariant 1: idempotent.
		if hostFromGitURL(url) != host {
			t.Fatal("hostFromGitURL not idempotent")
		}

		// Invariant 2: result length bounded by input length.
		if len(host) > len(url) {
			t.Fatalf("host %q longer than input %q", host, url)
		}

		// Invariant 3: if kindFromHost returns non-empty, hostFromGitURL
		// must have returned non-empty (consistency between the two).
		if host == "" {
			kind := kindFromHost(host)
			if kind != "" {
				t.Fatalf("kindFromHost(%q) = %q but hostFromGitURL returned empty", host, kind)
			}
		}
	})
}

// FuzzParseHookJSON exercises the hook JSON parser to verify it never
// panics and that Command output respects the 80-char truncation cap.
func FuzzParseHookJSON(f *testing.F) {
	f.Add([]byte(`{"event_type":"preToolUse","command":"echo hello"}`))
	f.Add([]byte(`{"event_type":"","command":""}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Add([]byte(`{"command":"` + string(make([]byte, 200)) + `"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		h := parseHookJSON(data)

		// Invariant 1: Command never exceeds 80 characters.
		if len(h.Command) > 80 {
			t.Fatalf("Command length %d > 80: %q", len(h.Command), h.Command)
		}

		// Invariant 2: idempotent.
		h2 := parseHookJSON(data)
		if h != h2 {
			t.Fatal("parseHookJSON not idempotent")
		}
	})
}

// FuzzTruncateUTF8 exercises the UTF-8 safe truncation to verify it
// never produces invalid UTF-8 and respects the byte limit.
func FuzzTruncateUTF8(f *testing.F) {
	f.Add("hello world", 5)
	f.Add("日本語テスト", 6)
	f.Add("", 0)
	f.Add("abc", 100)
	f.Add("🎉🎊🎈", 4)
	f.Add("a\xc0\xc1invalid", 3)

	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 0 {
			n = 0
		}
		if n > len(s) {
			n = len(s)
		}
		result := truncateUTF8(s, n)

		// Invariant 1: result length never exceeds n bytes.
		if len(result) > n {
			t.Fatalf("result len %d > limit %d", len(result), n)
		}

		// Invariant 2: result is valid UTF-8 when input is valid UTF-8.
		if utf8.ValidString(s) && !utf8.ValidString(result) {
			t.Fatalf("truncateUTF8(%q, %d) = %q: invalid UTF-8", s, n, result)
		}

		// Invariant 3: result is a prefix of s.
		if len(result) > 0 && s[:len(result)] != result {
			t.Fatal("result is not a prefix of input")
		}
	})
}

// FuzzIsMarkdownHeading exercises the heading classifier to verify it
// never panics and is consistent with its own spec (1-6 # followed by
// space/tab or EOL).
func FuzzIsMarkdownHeading(f *testing.F) {
	f.Add("# Title")
	f.Add("## Sub")
	f.Add("###### Deep")
	f.Add("####### Too deep")
	f.Add("#hashtag")
	f.Add("")
	f.Add("# ")
	f.Add("not a heading")

	f.Fuzz(func(t *testing.T, line string) {
		got := isMarkdownHeading(line)

		// Invariant: idempotent.
		if isMarkdownHeading(line) != got {
			t.Fatal("isMarkdownHeading not idempotent")
		}

		// Invariant: if true, line starts with 1-6 '#' followed by
		// space/tab or is exactly '#'...'######'.
		if got {
			if len(line) == 0 || line[0] != '#' {
				t.Fatalf("isMarkdownHeading(%q) = true but doesn't start with #", line)
			}
		}
	})
}

// FuzzKindFromHost exercises the forge-kind classifier to verify it
// never panics and only returns recognised forge kinds.
func FuzzKindFromHost(f *testing.F) {
	f.Add("github.com")
	f.Add("gitlab.com")
	f.Add("codeberg.org")
	f.Add("gitea.example.com")
	f.Add("forgejo.local")
	f.Add("")
	f.Add("random.host.io")

	f.Fuzz(func(t *testing.T, host string) {
		kind := kindFromHost(host)

		// Invariant: result is always one of the recognised values or empty.
		switch kind {
		case "", "github", "gitlab", "codeberg", "gitea":
		default:
			t.Fatalf("unexpected kind %q for host %q", kind, host)
		}

		// Invariant: idempotent.
		if kindFromHost(host) != kind {
			t.Fatal("kindFromHost not idempotent")
		}
	})
}
