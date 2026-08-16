package git

// ParseRemoteSlug is the boundary between "what git config says" and "what a forge
// CLI is asked about", so its refusals matter as much as its successes: the slug
// travels into a subprocess argv and a URL path.

import "testing"

func TestParseRemoteSlug(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		host string
		slug string
	}{
		{
			name: "SCPStyle", raw: "git@github.com:cplieger/vibekit.git",
			host: "github.com", slug: "cplieger/vibekit",
		},
		{
			name: "SCPStyleNoSuffix", raw: "git@github.com:cplieger/vibekit",
			host: "github.com", slug: "cplieger/vibekit",
		},
		{
			name: "HTTPS", raw: "https://github.com/cplieger/vibekit.git",
			host: "github.com", slug: "cplieger/vibekit",
		},
		{
			name: "HTTPSNoSuffix", raw: "https://github.com/cplieger/vibekit",
			host: "github.com", slug: "cplieger/vibekit",
		},
		{
			name: "SSHURL", raw: "ssh://git@gitlab.com/group/project.git",
			host: "gitlab.com", slug: "group/project",
		},
		{
			// A GitLab subgroup is part of the project's address; truncating to two
			// segments would produce a slug the forge cannot find.
			name: "GitLabSubgroupKeptWhole", raw: "https://gitlab.com/group/sub/deeper/project.git",
			host: "gitlab.com", slug: "group/sub/deeper/project",
		},
		{
			name: "TrailingSlash", raw: "https://github.com/cplieger/vibekit/",
			host: "github.com", slug: "cplieger/vibekit",
		},
		{
			name: "LeadingAndTrailingSpace", raw: "  git@github.com:a/b.git\n",
			host: "github.com", slug: "a/b",
		},
		{
			name: "SelfHostedGitea", raw: "https://git.example.test/team/thing.git",
			host: "git.example.test", slug: "team/thing",
		},
		{
			name: "PortIsNotPartOfTheHost", raw: "https://git.example.test:8443/team/thing.git",
			host: "git.example.test", slug: "team/thing",
		},

		// Refusals.
		{name: "Empty", raw: "", host: "", slug: ""},
		{name: "LocalPath", raw: "/srv/git/thing.git", host: "", slug: ""},
		{name: "RelativePath", raw: "../sibling", host: "", slug: ""},
		{name: "SingleSegment", raw: "https://github.com/onlyowner", host: "", slug: ""},
		{name: "SingleSegmentSCP", raw: "git@github.com:onlyowner.git", host: "", slug: ""},
		{name: "DotDotSegment", raw: "https://github.com/a/../b", host: "", slug: ""},
		{name: "RemoteHelper", raw: "ext::sh -c whoami", host: "", slug: ""},
		{name: "ControlCharInHost", raw: "https://git\x01hub.com/a/b", host: "", slug: ""},

		// Percent-encoded refusals. url.Parse DECODES the path, so each of these
		// reaches cleanSlug as the real byte and would otherwise travel into an
		// argv (where a NUL fails exec every sweep) or into a log line raw.
		{name: "EncodedNULInPath", raw: "https://github.com/a/b%00c", host: "", slug: ""},
		{name: "EncodedBELInPath", raw: "https://github.com/a/%07b", host: "", slug: ""},
		{name: "EncodedEscapeSequence", raw: "https://github.com/a/%1b]0;pwned%07b", host: "", slug: ""},
		{name: "EncodedDELInPath", raw: "https://github.com/a/b%7f", host: "", slug: ""},
		{name: "EncodedSpaceInPath", raw: "https://github.com/a/b%20c", host: "", slug: ""},
		// Backslash is not a path separator in any forge slug vocabulary, so
		// accepting it only widens the accepted language.
		{name: "EncodedBackslashInPath", raw: "https://github.com/a%5cb/c", host: "", slug: ""},
		{name: "LiteralBackslashSCP", raw: "git@github.com:a/b\\c.git", host: "", slug: ""},
		{name: "LiteralNULSCP", raw: "git@github.com:a/b\x00c.git", host: "", slug: ""},
		{
			// Not a refusal: url.Parse puts the query in RawQuery, so it never
			// reaches the slug. Kept as a case because the alternative — reading
			// RawPath or the whole URL string — would carry it into an argv.
			name: "QueryIsNotPartOfThePath", raw: "https://github.com/a/b?x=1",
			host: "github.com", slug: "a/b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, slug := ParseRemoteSlug(tc.raw)
			if host != tc.host || slug != tc.slug {
				t.Errorf("ParseRemoteSlug(%q) = (%q, %q), want (%q, %q)",
					tc.raw, host, slug, tc.host, tc.slug)
			}
		})
	}
}

// FuzzParseRemoteSlug pins the invariant that matters at this boundary: whatever a
// remote URL contains, an ACCEPTED slug is a plain multi-segment path holding no
// traversal, no C0 control, no DEL, no space, no backslash and no URL delimiter —
// because it becomes a subprocess argument and a URL path component.
//
// The forbidden set is spelled OUT here rather than by calling the production
// predicate. A fuzz invariant that reuses the implementation's own rule asserts
// nothing: the earlier version listed the same four whitespace characters the
// production check listed, so it blessed every NUL, DEL and backslash the check let
// through.
func FuzzParseRemoteSlug(f *testing.F) {
	for _, seed := range []string{
		"git@github.com:cplieger/vibekit.git",
		"https://gitlab.com/group/sub/project.git",
		"ssh://git@host/a/b",
		"ext::sh -c whoami",
		"https://github.com/a/../b",
		"/srv/git/thing.git",
		"",
		// Percent-encoded, because url.Parse DECODES the path: these arrive at
		// cleanSlug as real NUL, BEL, ESC, DEL and backslash bytes.
		"https://github.com/a/b%00c",
		"https://github.com/a/%07b",
		"https://github.com/a/%1b]0;pwned%07b",
		"https://github.com/a/b%7f",
		"https://github.com/a%5cb/c",
		"https://github.com/a/b%20c",
		"git@github.com:a/b\\c.git",
		"git@github.com:a/b\x00c.git",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		host, slug := ParseRemoteSlug(raw)
		if host == "" || slug == "" {
			if host != "" || slug != "" {
				t.Fatalf("ParseRemoteSlug(%q) returned a half answer (%q, %q)", raw, host, slug)
			}
			return
		}
		if slug[0] == '/' || slug[len(slug)-1] == '/' {
			t.Fatalf("slug %q from %q has a boundary slash", slug, raw)
		}
		segs := 0
		for _, seg := range splitSlug(slug) {
			segs++
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("slug %q from %q holds segment %q", slug, raw, seg)
			}
		}
		if segs < 2 {
			t.Fatalf("slug %q from %q is not a repository address", slug, raw)
		}
		for _, r := range slug {
			switch {
			case r < 0x20:
				t.Fatalf("slug %q from %q holds C0 control %#U", slug, raw, r)
			case r == 0x20:
				t.Fatalf("slug %q from %q holds a space", slug, raw)
			case r == 0x7F:
				t.Fatalf("slug %q from %q holds DEL", slug, raw)
			case r == '\\':
				t.Fatalf("slug %q from %q holds a backslash", slug, raw)
			case r == '?' || r == '#':
				t.Fatalf("slug %q from %q holds URL delimiter %q", slug, raw, r)
			}
		}
	})
}

// splitSlug is the fuzz target's own splitter, kept local so the invariant is
// asserted against the output rather than re-derived with the production helper.
func splitSlug(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
