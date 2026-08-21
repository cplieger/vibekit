// The production PRSource: whose PRs, in which repos.
//
// # The repo set, and why it is the WORKSPACE's rather than the account's
//
// ListPRs takes one repo and there is no cross-repo listing on any of the three
// CLIs, so a poller needs a repo SET. ListRepos would give the account's whole
// accessible list — owned plus member, capped at 300 on GitHub — and polling that
// every 60 seconds would be hundreds of subprocesses a minute to learn about
// repositories this box has never checked out.
//
// The workspace's own clones are the right set and the cheap one: this is a dev
// box, a PR the user opened was opened from here, and the count is a handful. The
// cost of that choice, stated: a PR opened from another machine in a repo not
// cloned here is not watched. That is the trade, and it is the one that keeps the
// decision's "costs zero with nothing pending" claim true.

package forges

import (
	"context"
	"log/slog"
	"strings"
)

// PRRepo is one repo the poller watches, in the coordinates a forge addresses.
type PRRepo struct {
	// ForgeID is the `kind:host` id of the connection that serves this repo.
	ForgeID string
	// Slug is the owner/name path (whole, so a GitLab subgroup survives).
	Slug string
}

// managerPRSource resolves the poller's question through a provider lookup and a
// repo resolver.
type managerPRSource struct {
	// provider is Manager.Provider in production. A FUNCTION rather than the
	// manager, so this is testable with a stub ForgeOps and no CLI on PATH — the
	// same reason the poller takes a PRSource instead of building one.
	provider func(id string) (ForgeOps, error)
	// repos is injected rather than computed here: the answer is git knowledge
	// (which repositories are checked out and where their origins point), and this
	// package must not grow a second copy of repo discovery.
	repos func(context.Context) []PRRepo
}

// NewManagerPRSource returns the production PRSource over a forge manager and a
// repo resolver.
func NewManagerPRSource(mgr *Manager, repos func(context.Context) []PRRepo) PRSource {
	return &managerPRSource{provider: mgr.Provider, repos: repos}
}

// OpenAuthoredPRs lists the open pull requests the connected identity authored
// across the watched repos.
//
// The author filter is CLIENT-side because there is no author filter on the wire:
// none of `gh pr list`, `glab mr list` or `tea` takes one in the shape vibekit
// calls them, so the identity comes from Whoami and the comparison happens here.
// Whoami is resolved once per forge per sweep and cached for that sweep, since
// several watched repos usually share one connection.
//
// A per-repo failure is skipped rather than returned: one archived repo, one
// permission wall or one CLI that lost its token must not stop the other repos'
// PRs from being watched. A total failure surfaces as an empty list, which the
// poller treats as "nothing pending" — the safe direction, since the alternative
// would be to notify on stale state.
func (s *managerPRSource) OpenAuthoredPRs(ctx context.Context) ([]WatchedPR, error) {
	repos := s.repos(ctx)
	if len(repos) == 0 {
		return nil, nil
	}
	logins := make(map[string]string, 2)
	var out []WatchedPR
	for _, r := range repos {
		provider, err := s.provider(r.ForgeID)
		if err != nil {
			slog.Debug("pr status: no provider for forge", "forge", r.ForgeID, "error", err)
			continue
		}
		login, resolved := logins[r.ForgeID]
		if !resolved {
			login = whoamiLogin(ctx, provider, r.ForgeID)
			logins[r.ForgeID] = login
		}
		if login == "" {
			continue // not logged in, or the CLI could not say who we are
		}
		prs, err := provider.ListPRs(ctx, r.Slug, StateOpen)
		if err != nil {
			slog.Debug("pr status: list open PRs failed",
				"forge", r.ForgeID, "repo", r.Slug, "error", err)
			continue
		}
		for i := range prs {
			pr := &prs[i]
			if !strings.EqualFold(pr.Author, login) {
				continue
			}
			out = append(out, WatchedPR{
				ForgeID: r.ForgeID,
				Repo:    r.Slug,
				Number:  pr.Number,
				Title:   pr.Title,
				Check:   pr.CheckStatus,
			})
		}
	}
	return out, nil
}

// whoamier is the identity read. Declared here because this is the only
// consumer: the author filter for open PRs is client-side (no forge CLI takes an
// author filter in the shape vibekit calls them), so the connected login has to
// be resolved before the listing can be filtered.
type whoamier interface {
	// Whoami returns the authenticated account, or an error if not
	// logged in (or the CLI is not installed).
	Whoami(ctx context.Context) (*User, error)
}

// whoamiLogin resolves a forge's connected login, answering "" when it cannot.
// Logged at Debug: a forge the user disconnected is an ordinary state here, not a
// fault, and a Warn per tick would fill the log for a box with one stale config.
func whoamiLogin(ctx context.Context, provider whoamier, forgeID string) string {
	user, err := provider.Whoami(ctx)
	if err != nil || user == nil {
		slog.Debug("pr status: whoami failed", "forge", forgeID, "error", err)
		return ""
	}
	return user.Login
}

// RepoOrigin is one checked-out repo's forge coordinates as the caller resolved
// them. It mirrors git.RepoRemote's two load-bearing fields rather than importing
// that type: this package knows about forges, and the repo-discovery direction
// belongs to the caller that owns both.
type RepoOrigin struct {
	Host string
	Slug string
}

// MatchRepos pairs each checked-out repo with the configured forge that
// serves its host.
//
// It lives here rather than in the composition root because it joins two of this
// package's own vocabularies — a forge's host and its `kind:host` id — and a wiring
// file doing the match would be a second place for that id rule to be spelled.
// Hosts compare case-insensitively: git config carries whatever the user typed,
// and a forge records what its CLI reported.
//
// A repo whose host has no CONNECTED forge is dropped, which is also the gate that
// keeps the poller quiet on a box with a stale CLI config: a `cli_missing` row is
// never probed elsewhere either.
func MatchRepos(configured []ConfiguredForge, origins []RepoOrigin) []PRRepo {
	byHost := make(map[string]string, len(configured))
	for _, f := range configured {
		if !f.Connected || f.CLIMissing {
			continue
		}
		byHost[strings.ToLower(f.Host)] = f.ID
	}
	out := make([]PRRepo, 0, len(origins))
	for _, o := range origins {
		id, ok := byHost[strings.ToLower(o.Host)]
		if !ok {
			continue
		}
		out = append(out, PRRepo{ForgeID: id, Slug: o.Slug})
	}
	return out
}
