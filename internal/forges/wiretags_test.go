package forges

// Nested-object wire-key contract.
//
// Every decode target in this package reaches at least one value through a
// NESTED object — `owner.login`, `defaultBranchRef.name`, `author.login`,
// `labels[].name`, `author.username`, `_links.self`, `user.login`,
// `head.sha`. Those inner fields carried no json tag until 2026-08-20, so
// each one bound to its wire key only through encoding/json v1's
// case-insensitive fallback. Measured on go1.27.0: the same struct decoded
// with `encoding/json/v2` (which does NO case folding, not even ASCII)
// leaves every one of them at the zero value, with no error. Since v1 is
// backed by v2 from 1.27 on, that fallback is now the only thing holding
// the contract together, and `head.sha` is the merge/rerun precondition
// pin — a silent "" there is a merge against the wrong commit.
//
// The tags are the fix. This file is why they are TRUE: every body below is
// the wire spelling the real API sends, written by hand rather than
// produced by marshalling the decode target, because a fixture built from
// the struct agrees with any key the struct happens to declare and would
// pass with all of these tags wrong.
//
// WHAT IT CATCHES AND WHAT IT CANNOT, both red-checked on go1.27.0. A wrong
// NAME fails here: retagging `owner.login` as `handle` reports
// `owner = "", want cplieger`. A wrong CASING does NOT, and no v1-only test
// can make it — retagging it `LOGIN` still passes, because v1 folds the tag
// name as readily as the field name. So the casing half of the contract is
// carried by the tag being READABLE beside the API it mirrors, not by an
// assertion; the value of the tag is that a future direct-v2 decode has
// something exact to match, since v2 would silently zero the field either way.
//
// Adding a field to one of those decode targets means adding a key here.

import (
	"strings"
	"testing"
)

// TestGitHubNestedWireKeys drives the two gh decode paths whose nested keys
// nothing else covered: ListRepos (`owner`, `defaultBranchRef`) and the
// issue reads (`author`, `labels`).
func TestGitHubNestedWireKeys(t *testing.T) {
	t.Run("ListRepos", func(t *testing.T) {
		const body = `[{"name":"vibekit","owner":{"id":"MDQ6","login":"cplieger"},` +
			`"nameWithOwner":"cplieger/vibekit","defaultBranchRef":{"name":"main"},` +
			`"description":"d","url":"https://github.com/cplieger/vibekit",` +
			`"updatedAt":"2026-08-20T10:00:00Z","isPrivate":true,"isArchived":false,"isFork":false}]`
		p, _ := newGitHubWithStub(t, `printf '%s' '`+body+`'`)
		repos, err := p.ListRepos(t.Context())
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		if len(repos) != 1 {
			t.Fatalf("ListRepos returned %d repos, want 1", len(repos))
		}
		if repos[0].Owner != "cplieger" {
			t.Errorf("owner = %q, want cplieger: the `owner.login` tag does not match gh's key", repos[0].Owner)
		}
		if repos[0].DefaultBranch != "main" {
			t.Errorf("default branch = %q, want main: the `defaultBranchRef.name` tag does not match gh's key",
				repos[0].DefaultBranch)
		}
	})

	// gh --json emits camelCase; the same fields over `gh api` are snake_case,
	// and both spellings appear in github.go. This pins the --json one.
	const issueBody = `{"number":7,"title":"t","body":"b","state":"OPEN",` +
		`"author":{"id":"MDQ6","is_bot":false,"login":"alice","name":"Alice"},` +
		`"url":"https://github.com/o/r/issues/7","labels":[{"id":"L1","name":"bug","color":"f00"}],` +
		`"createdAt":"2026-08-20T10:00:00Z","updatedAt":"2026-08-20T11:00:00Z"}`

	t.Run("ListIssues", func(t *testing.T) {
		p, _ := newGitHubWithStub(t, `printf '%s' '[`+issueBody+`]'`)
		issues, err := p.ListIssues(t.Context(), "o/r", StateOpen)
		if err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		if len(issues) != 1 {
			t.Fatalf("ListIssues returned %d issues, want 1", len(issues))
		}
		assertIssueNested(t, issues[0], "alice", "bug", "`author.login`/`labels[].name`")
	})

	t.Run("viewIssue", func(t *testing.T) {
		p, _ := newGitHubWithStub(t, `printf '%s' '`+issueBody+`'`)
		issue, err := p.viewIssue(t.Context(), "o/r", 7)
		if err != nil {
			t.Fatalf("viewIssue: %v", err)
		}
		assertIssueNested(t, *issue, "alice", "bug", "`author.login`/`labels[].name`")
	})
}

// TestGitLabNestedWireKeys drives the two glab decode paths whose nested keys
// nothing else covered: the issue reads (`author.username` — GitLab spells the
// handle `username`, not `login`) and ListReleases (`_links.self`, whose
// leading underscore is the reason that field needed a tag at the OUTER level
// too).
func TestGitLabNestedWireKeys(t *testing.T) {
	const issueBody = `{"iid":7,"title":"t","description":"b","state":"opened",` +
		`"author":{"id":11,"username":"alice","name":"Alice"},` +
		`"web_url":"https://gitlab.com/g/p/-/issues/7","labels":["bug"],` +
		`"created_at":"2026-08-20T10:00:00Z","updated_at":"2026-08-20T11:00:00Z"}`

	t.Run("ListIssues", func(t *testing.T) {
		p, _ := newGitLabWithStub(t, `printf '%s' '[`+issueBody+`]'`)
		issues, err := p.ListIssues(t.Context(), "g/p", StateOpen)
		if err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		if len(issues) != 1 {
			t.Fatalf("ListIssues returned %d issues, want 1", len(issues))
		}
		assertIssueNested(t, issues[0], "alice", "bug", "`author.username`")
	})

	t.Run("viewIssue", func(t *testing.T) {
		p, _ := newGitLabWithStub(t, `printf '%s' '`+issueBody+`'`)
		issue, err := p.viewIssue(t.Context(), "g/p", 7)
		if err != nil {
			t.Fatalf("viewIssue: %v", err)
		}
		assertIssueNested(t, *issue, "alice", "bug", "`author.username`")
	})

	t.Run("ListReleases", func(t *testing.T) {
		const body = `[{"tag_name":"v1.2.3","name":"1.2.3","description":"notes",` +
			`"_links":{"self":"https://gitlab.com/g/p/-/releases/v1.2.3",` +
			`"closed_issues_url":"x"},"released_at":"2026-08-20T10:00:00Z",` +
			`"upcoming_release":true}]`
		p, _ := newGitLabWithStub(t, `printf '%s' '`+body+`'`)
		releases, err := p.ListReleases(t.Context(), "g/p")
		if err != nil {
			t.Fatalf("ListReleases: %v", err)
		}
		if len(releases) != 1 {
			t.Fatalf("ListReleases returned %d releases, want 1", len(releases))
		}
		if want := "https://gitlab.com/g/p/-/releases/v1.2.3"; releases[0].URL != want {
			t.Errorf("release URL = %q, want %q: the `_links.self` tag does not match GitLab's key",
				releases[0].URL, want)
		}
	})
}

// TestGiteaNestedWireKeys covers the two gitea decoders directly — they are
// package functions over the raw body, so no CLI stub is involved. parsePRs'
// `head`/`base` pair already has a caller-side test; what is new here is
// `user.login` on both, and `labels[].name` on the issue side.
func TestGiteaNestedWireKeys(t *testing.T) {
	t.Run("parsePRs", func(t *testing.T) {
		const body = `[{"number":4,"title":"t","body":"b","state":"open","mergeable":true,` +
			`"user":{"id":3,"login":"alice","full_name":"Alice"},` +
			`"head":{"ref":"feat","sha":"aaaaaaa1111"},"base":{"ref":"main"},` +
			`"html_url":"https://codeberg.org/o/r/pulls/4",` +
			`"created_at":"2026-08-20T10:00:00Z","updated_at":"2026-08-20T11:00:00Z"}]`
		prs, err := parsePRs([]byte(body))
		if err != nil {
			t.Fatalf("parsePRs: %v", err)
		}
		if len(prs) != 1 {
			t.Fatalf("parsePRs returned %d PRs, want 1", len(prs))
		}
		if prs[0].Author != "alice" {
			t.Errorf("author = %q, want alice: the `user.login` tag does not match Gitea's key", prs[0].Author)
		}
		// The merge/rerun precondition pin. A silent "" here merges whatever the
		// branch points at now rather than what the row was showing.
		if prs[0].HeadSHA != "aaaaaaa1111" {
			t.Errorf("head SHA = %q, want aaaaaaa1111: the `head.sha` tag does not match Gitea's key",
				prs[0].HeadSHA)
		}
		if prs[0].SourceBranch != "feat" || prs[0].TargetBranch != "main" {
			t.Errorf("branches = (%q, %q), want (feat, main): a `head.ref`/`base.ref` tag is wrong",
				prs[0].SourceBranch, prs[0].TargetBranch)
		}
	})

	t.Run("parseIssues", func(t *testing.T) {
		const body = `[{"number":7,"title":"t","body":"b","state":"open",` +
			`"user":{"id":3,"login":"alice","full_name":"Alice"},` +
			`"labels":[{"id":9,"name":"bug","color":"f00"}],` +
			`"html_url":"https://codeberg.org/o/r/issues/7",` +
			`"created_at":"2026-08-20T10:00:00Z","updated_at":"2026-08-20T11:00:00Z"}]`
		issues, err := parseIssues([]byte(body))
		if err != nil {
			t.Fatalf("parseIssues: %v", err)
		}
		if len(issues) != 1 {
			t.Fatalf("parseIssues returned %d issues, want 1", len(issues))
		}
		assertIssueNested(t, issues[0], "alice", "bug", "`user.login`/`labels[].name`")
	})
}

// assertIssueNested checks the two values an Issue reaches through a nested
// object, naming the tags that decide them so a failure says which key drifted
// rather than only that a field is empty.
func assertIssueNested(t *testing.T, got Issue, wantAuthor, wantLabel, tags string) {
	t.Helper()
	if got.Author != wantAuthor {
		t.Errorf("author = %q, want %q: a %s tag does not match the forge's key", got.Author, wantAuthor, tags)
	}
	if len(got.Labels) != 1 || got.Labels[0] != wantLabel {
		t.Errorf("labels = %v, want [%s]: a %s tag does not match the forge's key",
			got.Labels, wantLabel, tags)
	}
	if got.Number != 7 || !strings.Contains(got.URL, "7") {
		t.Errorf("issue = %+v: the flat fields decoded wrong, so the fixture is not the shape under test", got)
	}
}
