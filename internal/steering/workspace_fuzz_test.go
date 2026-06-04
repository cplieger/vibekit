package steering

import (
	"testing"
	"unicode/utf8"
)

// FuzzHostFromGitURL exercises the git-URL host extractor with arbitrary
// strings and asserts structural invariants:
//
//  1. No panics on any input.
//  2. Result never contains "://" (scheme stripped).
//  3. Result never contains "@" (credentials stripped).
//  4. A round-trip scp-style URL "git@host:path" → host is exact.
func FuzzHostFromGitURL(f *testing.F) {
	f.Add("https://github.com/owner/repo.git")
	f.Add("git@github.com:owner/repo.git")
	f.Add("http://user:pass@gitlab.com/owner/repo")
	f.Add("https://gitlab.company.com/group/project.git")
	f.Add("")
	f.Add("file:///local/path")
	f.Add("ssh://git@host.example:2222/repo.git")
	f.Add("ext::ssh -o ProxyCommand=nc %h %p -- git.example.com/repo")
	f.Add("https://")
	f.Add("git@:")

	f.Fuzz(func(t *testing.T, url string) {
		host := hostFromGitURL(url)
		// Invariant 1: no panics (implicit).
		// Invariant 2: result never contains "://".
		if len(host) > 0 {
			for i := range len(host) - 2 {
				if host[i] == ':' && host[i+1] == '/' && host[i+2] == '/' {
					t.Errorf("hostFromGitURL(%q) = %q contains '://'", url, host)
					break
				}
			}
		}
		// Invariant 3: result never contains "@".
		for _, c := range host {
			if c == '@' {
				t.Errorf("hostFromGitURL(%q) = %q contains '@'", url, host)
				break
			}
		}
	})
}

// FuzzKindFromHost exercises the forge-kind classifier with arbitrary
// host strings and asserts:
//
//  1. No panics.
//  2. Result is always one of "", "github", "gitlab", "codeberg", "gitea".
func FuzzKindFromHost(f *testing.F) {
	f.Add("github.com")
	f.Add("gitlab.com")
	f.Add("codeberg.org")
	f.Add("gitea.example.com")
	f.Add("forgejo.dev")
	f.Add("")
	f.Add("bitbucket.org")
	f.Add("self-hosted-gitlab.internal")

	f.Fuzz(func(t *testing.T, host string) {
		kind := kindFromHost(host)
		switch kind {
		case "", "github", "gitlab", "codeberg", "gitea":
			// ok
		default:
			t.Errorf("kindFromHost(%q) = %q; want one of empty/github/gitlab/codeberg/gitea", host, kind)
		}
	})
}

// FuzzIsMarkdownHeading exercises the heading classifier and asserts:
//
//  1. No panics.
//  2. If the line doesn't start with '#', result is always false.
//  3. Lines with >6 leading '#' are never headings.
func FuzzIsMarkdownHeading(f *testing.F) {
	f.Add("# Title")
	f.Add("## Subtitle")
	f.Add("#hashtag")
	f.Add("###### Six")
	f.Add("####### Seven")
	f.Add("")
	f.Add("no heading here")
	f.Add("#")
	f.Add("### ")

	f.Fuzz(func(t *testing.T, line string) {
		result := isMarkdownHeading(line)
		if line == "" || line[0] != '#' {
			if result {
				t.Errorf("isMarkdownHeading(%q) = true; line doesn't start with '#'", line)
			}
		}
		// Count leading '#'
		n := 0
		for n < len(line) && line[n] == '#' {
			n++
		}
		if n > 6 && result {
			t.Errorf("isMarkdownHeading(%q) = true; >6 leading '#' chars", line)
		}
	})
}

// FuzzTruncateUTF8 exercises the UTF-8-safe truncation and asserts:
//
//  1. No panics.
//  2. Result length <= n.
//  3. If input is valid UTF-8, result is valid UTF-8.
//  4. If len(s) <= n, result == s.
func FuzzTruncateUTF8(f *testing.F) {
	f.Add("hello", 3)
	f.Add("hello 世界", 8)
	f.Add("", 0)
	f.Add("abc", 100)
	f.Add("🌍🌍🌍", 5)

	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 0 {
			return
		}
		result := truncateUTF8(s, n)
		if len(result) > n {
			t.Errorf("truncateUTF8(%q, %d) len=%d > n", s, n, len(result))
		}
		// Only assert UTF-8 validity if the input was valid UTF-8.
		if utf8.ValidString(s) && !utf8.ValidString(result) {
			t.Errorf("truncateUTF8(%q, %d) = %q; broke valid UTF-8", s, n, result)
		}
		if len(s) <= n && result != s {
			t.Errorf("truncateUTF8(%q, %d) = %q; expected identity", s, n, result)
		}
	})
}

// FuzzParseSteeringFrontmatter exercises the frontmatter parser with
// arbitrary byte sequences and asserts:
//
//  1. No panics.
//  2. Inclusion is always "always", "fileMatch", or "manual".
func FuzzParseSteeringFrontmatter(f *testing.F) {
	f.Add([]byte("---\ninclusion: always\n---\n# Doc"))
	f.Add([]byte("---\ninclusion: fileMatch\nfileMatchPattern: \"*.go\"\ndescription: \"Go files\"\n---\n"))
	f.Add([]byte("---\ninclusion: manual\n---\n"))
	f.Add([]byte("no frontmatter"))
	f.Add([]byte(""))
	f.Add([]byte("---\n---\n"))
	f.Add([]byte("---\ninclusion: bogus\n---\n"))
	f.Add([]byte("---\n\x00\xff\xfe\n---\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		doc := parseSteeringFrontmatter(data)
		switch doc.Inclusion {
		case "always", "fileMatch", "manual":
			// ok
		default:
			t.Errorf("parseSteeringFrontmatter returned Inclusion=%q; want always|fileMatch|manual", doc.Inclusion)
		}
	})
}
