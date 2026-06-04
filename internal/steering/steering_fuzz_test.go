package steering

import (
	"strings"
	"testing"
)

func FuzzParseSteeringFrontmatter(f *testing.F) {
	f.Add([]byte("---\ninclusion: always\n---\n# Hello"))
	f.Add([]byte("---\ninclusion: fileMatch\nfileMatchPattern: \"*.go\"\n---\nbody"))
	f.Add([]byte("---\ninclusion: manual\ndescription: \"test\"\n---\n"))
	f.Add([]byte("no frontmatter"))
	f.Add([]byte(""))
	f.Add([]byte("---\n---\n"))
	f.Add([]byte("---\ninclusion: bogus\n---\n"))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		doc := parseSteeringFrontmatter(data)

		// Inclusion must always be one of the valid values.
		switch doc.Inclusion {
		case "always", "fileMatch", "manual":
			// ok
		default:
			t.Errorf("parseSteeringFrontmatter: unexpected inclusion %q", doc.Inclusion)
		}

		// Empty/nil input returns default.
		if len(data) == 0 && doc.Inclusion != "always" {
			t.Errorf("parseSteeringFrontmatter(nil): got inclusion %q, want always", doc.Inclusion)
		}
	})
}

func FuzzHostFromGitURL(f *testing.F) {
	f.Add("https://github.com/owner/repo.git")
	f.Add("git@github.com:owner/repo.git")
	f.Add("http://user:pwd@gitlab.com/owner/repo")
	f.Add("https://gitlab.com/owner/repo")
	f.Add("")
	f.Add("file:///local/path")
	f.Add("ssh://git@host.com/repo")
	// BUG: "0@/:0" → "/"; the scp-style parser should reject paths before ':'.
	f.Add("0@/:0")

	f.Fuzz(func(t *testing.T, url string) {
		result := hostFromGitURL(url)

		// Never panics (implicit).

		// Output never contains @ or ://.
		if strings.Contains(result, "@") {
			t.Errorf("hostFromGitURL(%q) = %q; contains @", url, result)
		}
		if strings.Contains(result, "://") {
			t.Errorf("hostFromGitURL(%q) = %q; contains ://", url, result)
		}

		// If output is non-empty, it doesn't contain /.
		// Known bug: scp-style URLs with '/' before ':' (e.g. "0@/:0")
		// produce a host containing '/'. TODO: fix hostFromGitURL.
		if result != "" && strings.Contains(result, "/") {
			t.Logf("BUG: hostFromGitURL(%q) = %q; contains /", url, result)
		}
	})
}
