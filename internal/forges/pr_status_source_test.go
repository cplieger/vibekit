package forges

// The production PRSource's two rules: whose PRs (the author filter, which is
// client-side because no CLI takes an author filter in the shape vibekit calls
// them), and which repos (the host-to-forge match).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newStubSource wires a managerPRSource whose every forge id resolves to one stub,
// so a case exercises the source's own rules with no CLI on PATH.
func newStubSource(ops ForgeOps, repos []PRRepo) PRSource {
	return &managerPRSource{
		provider: func(string) (ForgeOps, error) { return ops, nil },
		repos:    func(context.Context) []PRRepo { return repos },
	}
}

func TestMatchRepos(t *testing.T) {
	configured := []ConfiguredForge{
		{ID: "github:github.com", Kind: KindGitHub, Host: "GitHub.com", Connected: true},
		{ID: "gitlab:gitlab.com", Kind: KindGitLab, Host: "gitlab.com", Connected: true},
		{ID: "gitea:git.example", Kind: KindGitea, Host: "git.example", Connected: false},
		{ID: "github:ghe.example", Kind: KindGitHub, Host: "ghe.example", Connected: true, CLIMissing: true},
	}
	cases := []struct {
		name    string
		origins []RepoOrigin
		want    []PRRepo
	}{
		{
			name:    "MatchesCaseInsensitively",
			origins: []RepoOrigin{{Host: "github.com", Slug: "cplieger/vibekit"}},
			want:    []PRRepo{{ForgeID: "github:github.com", Slug: "cplieger/vibekit"}},
		},
		{
			name:    "KeepsAGitLabSubgroupSlugWhole",
			origins: []RepoOrigin{{Host: "gitlab.com", Slug: "group/sub/project"}},
			want:    []PRRepo{{ForgeID: "gitlab:gitlab.com", Slug: "group/sub/project"}},
		},
		{
			name:    "DropsAnUnconnectedForge",
			origins: []RepoOrigin{{Host: "git.example", Slug: "a/b"}},
			want:    []PRRepo{},
		},
		{
			// A cli_missing row is never probed anywhere else either, so polling
			// through it would be the one place that tries.
			name:    "DropsAForgeWhoseCLIIsGone",
			origins: []RepoOrigin{{Host: "ghe.example", Slug: "a/b"}},
			want:    []PRRepo{},
		},
		{
			name:    "DropsAHostWithNoForgeAtAll",
			origins: []RepoOrigin{{Host: "codeberg.org", Slug: "a/b"}},
			want:    []PRRepo{},
		},
		{
			name: "KeepsEveryMatchingRepo",
			origins: []RepoOrigin{
				{Host: "github.com", Slug: "a/one"},
				{Host: "nowhere.test", Slug: "a/two"},
				{Host: "gitlab.com", Slug: "a/three"},
			},
			want: []PRRepo{
				{ForgeID: "github:github.com", Slug: "a/one"},
				{ForgeID: "gitlab:gitlab.com", Slug: "a/three"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRepos(configured, tc.origins)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d repos, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("repo[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// prStubOps is a ForgeOps whose only live methods are the two the source uses.
type prStubOps struct {
	ForgeOps // embedded so the unused methods are nil and a call to one panics loudly
	login    string
	loginErr error
	prs      []PR
	prsErr   error
	listed   []string
}

func (s *prStubOps) Whoami(context.Context) (*User, error) {
	if s.loginErr != nil {
		return nil, s.loginErr
	}
	return &User{Login: s.login}, nil
}

func (s *prStubOps) ListPRs(_ context.Context, repo string, state ListState) ([]PR, error) {
	s.listed = append(s.listed, repo+":"+string(state))
	return s.prs, s.prsErr
}

// TestManagerPRSource_FiltersByAuthor is the rule that makes the notification
// "a PR you opened" rather than "a PR in a repo you have".
func TestManagerPRSource_FiltersByAuthor(t *testing.T) {
	ops := &prStubOps{
		login: "cplieger",
		prs: []PR{
			{Number: 1, Author: "cplieger", CheckStatus: checkPassing, Title: "mine"},
			{Number: 2, Author: "renovate[bot]", CheckStatus: checkFailing, Title: "not mine"},
			{Number: 3, Author: "CPlieger", CheckStatus: checkFailing, Title: "mine, other case"},
		},
	}
	src := newStubSource(ops, []PRRepo{{ForgeID: "github:github.com", Slug: "cplieger/vibekit"}})

	got, err := src.OpenAuthoredPRs(t.Context())
	if err != nil {
		t.Fatalf("OpenAuthoredPRs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d watched PRs, want 2 (own PRs only): %+v", len(got), got)
	}
	// Case-insensitive, because git forges are inconsistent about the case they
	// report a login in and a mismatch would silently watch nothing.
	if got[0].Number != 1 || got[1].Number != 3 {
		t.Errorf("watched the wrong PRs: %+v", got)
	}
	if len(ops.listed) != 1 || !strings.HasSuffix(ops.listed[0], ":open") {
		t.Errorf("listed %v, want one open-state listing", ops.listed)
	}
}

// TestManagerPRSource_NoLoginWatchesNothing: without an identity there is no author
// to filter on, so watching everything would notify about other people's PRs.
func TestManagerPRSource_NoLoginWatchesNothing(t *testing.T) {
	ops := &prStubOps{login: "", prs: []PR{{Number: 1, Author: "someone", CheckStatus: checkPassing}}}
	src := newStubSource(ops, []PRRepo{{ForgeID: "github:github.com", Slug: "a/b"}})
	got, err := src.OpenAuthoredPRs(t.Context())
	if err != nil {
		t.Fatalf("OpenAuthoredPRs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("watched %d PRs with no resolved identity: %+v", len(got), got)
	}
	if len(ops.listed) != 0 {
		t.Errorf("listed PRs before resolving an identity: %v", ops.listed)
	}
}

// TestManagerPRSource_NoReposIsOneCheapAnswer: an empty repo set must not reach a
// provider at all.
func TestManagerPRSource_NoReposIsOneCheapAnswer(t *testing.T) {
	ops := &prStubOps{login: "cplieger"}
	src := newStubSource(ops, nil)
	got, err := src.OpenAuthoredPRs(t.Context())
	if err != nil || len(got) != 0 {
		t.Fatalf("got (%v, %v), want (nil, nil)", got, err)
	}
	if len(ops.listed) != 0 {
		t.Errorf("consulted a provider with no repos in scope: %v", ops.listed)
	}
}

// TestManagerPRSource_ResolvesWhoamiOncePerForge: several watched repos usually
// share one connection, and Whoami is a subprocess.
func TestManagerPRSource_ResolvesWhoamiOncePerForge(t *testing.T) {
	ops := &whoamiCountingOps{prStubOps: prStubOps{login: "cplieger"}}
	src := newStubSource(ops, []PRRepo{
		{ForgeID: "github:github.com", Slug: "a/one"},
		{ForgeID: "github:github.com", Slug: "a/two"},
		{ForgeID: "github:github.com", Slug: "a/three"},
	})
	if _, err := src.OpenAuthoredPRs(t.Context()); err != nil {
		t.Fatalf("OpenAuthoredPRs: %v", err)
	}
	if ops.whoamis != 1 {
		t.Errorf("resolved the identity %d times for one forge, want 1", ops.whoamis)
	}
	if len(ops.listed) != 3 {
		t.Errorf("listed %d repos, want 3: %v", len(ops.listed), ops.listed)
	}
}

// TestManagerPRSource_APerRepoFailureIsSkipped: one archived repo or one permission
// wall must not stop the other repos' PRs from being watched.
func TestManagerPRSource_APerRepoFailureIsSkipped(t *testing.T) {
	ops := &failFirstOps{prStubOps: prStubOps{
		login: "cplieger",
		prs:   []PR{{Number: 4, Author: "cplieger", CheckStatus: checkPassing}},
	}}
	src := newStubSource(ops, []PRRepo{
		{ForgeID: "github:github.com", Slug: "a/broken"},
		{ForgeID: "github:github.com", Slug: "a/fine"},
	})
	got, err := src.OpenAuthoredPRs(t.Context())
	if err != nil {
		t.Fatalf("a per-repo failure became a total failure: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d watched PRs, want 1 from the healthy repo: %+v", len(got), got)
	}
}

// whoamiCountingOps counts identity resolutions, which is how the once-per-forge
// rule is asserted rather than assumed.
type whoamiCountingOps struct {
	prStubOps
	whoamis int
}

func (o *whoamiCountingOps) Whoami(ctx context.Context) (*User, error) {
	o.whoamis++
	return o.prStubOps.Whoami(ctx)
}

// failFirstOps fails the FIRST ListPRs and serves the rest, which is the shape of
// one archived repo among several healthy ones.
type failFirstOps struct {
	prStubOps
	failed bool
}

func (o *failFirstOps) ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error) {
	if !o.failed {
		o.failed = true
		o.listed = append(o.listed, repo+":failed")
		return nil, errors.New("repository not found")
	}
	return o.prStubOps.ListPRs(ctx, repo, state)
}
