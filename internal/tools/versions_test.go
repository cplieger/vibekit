package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestLatestGitHubTag_PaginatesAndVersionCompares reproduces the
// golang/go shape that broke first-tag selection: page 1 holds only
// ancient non-matching tags (weekly.*), the real go1* tags appear on a
// later page, and response order is NOT newest-first. The resolver
// must paginate and pick the version-maximum, never the first match.
func TestLatestGitHubTag_PaginatesAndVersionCompares(t *testing.T) {
	type tag struct {
		Name string `json:"name"`
	}
	pages := map[int][]tag{}
	// Page 1: 100 ancient weekly tags (no go1* match).
	for i := range 100 {
		pages[1] = append(pages[1], tag{Name: "weekly.2012-" + strconv.Itoa(i)})
	}
	// Page 2: go tags in scrambled order — go1.9 BEFORE go1.24 (a
	// lexicographic or first-match pick would return the wrong one).
	pages[2] = []tag{
		{Name: "go1.9"},
		{Name: "go1.24rc1"}, // filtered out by the version_filter
		{Name: "go1.23.4"},
		{Name: "go1.24.0"},
		{Name: "go1.22beta2"}, // filtered out
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		_ = json.NewEncoder(w).Encode(pages[page])
	}))
	defer srv.Close()

	v := newVersionResolver(srv.Client())
	// Point the GitHub API base at the test server by monkeying the
	// request URL through a RoundTripper rewrite.
	v.client = &http.Client{Transport: rewriteHost{target: srv.URL}}

	aq := &AquaPackage{
		Type: "http", RepoOwner: "golang", RepoName: "go",
		VersionSource: "github_tag",
		VersionFilter: `Version startsWith "go" and not (Version contains "rc" or Version contains "beta")`,
	}
	got, err := v.latestGitHubTag(context.Background(), "golang", "go", aq)
	if err != nil {
		t.Fatal(err)
	}
	if got != "go1.24.0" {
		t.Fatalf("latest = %q, want go1.24.0", got)
	}
}

// rewriteHost redirects every request to the test server, preserving
// path+query.
type rewriteHost struct{ target string }

func (rw rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	nu := rw.target + req.URL.Path
	if req.URL.RawQuery != "" {
		nu += "?" + req.URL.RawQuery
	}
	clone := req.Clone(req.Context())
	u, err := clone.URL.Parse(nu)
	if err != nil {
		return nil, err
	}
	clone.URL = u
	clone.Host = u.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestLatestGitHubTag_NoMatchErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"name":"nope"}]`)
	}))
	defer srv.Close()
	v := newVersionResolver(&http.Client{Transport: rewriteHost{target: srv.URL}})
	aq := &AquaPackage{VersionFilter: `Version startsWith "go"`}
	if _, err := v.latestGitHubTag(context.Background(), "o", "r", aq); err == nil {
		t.Fatal("want error when nothing matches")
	}
}

func TestMaxVersionTag(t *testing.T) {
	cases := []struct {
		candidates []string
		prefix     string
		want       string
	}{
		{[]string{"v1.9.0", "v1.24.0", "v1.10.1"}, "", "v1.24.0"},
		{[]string{"go1.9", "go1.24.0", "go1.23.4"}, "", "go1.24.0"},
		{[]string{"jq-1.7.1", "jq-1.8.2"}, "", "jq-1.8.2"},
		{[]string{"2025-01-06", "2024-12-01"}, "", "2025-01-06"},
		{[]string{"only"}, "", "only"},
	}
	for _, c := range cases {
		if got := maxVersionTag(c.candidates, c.prefix); got != c.want {
			t.Errorf("maxVersionTag(%v) = %q, want %q", c.candidates, got, c.want)
		}
	}
}

func TestConstraint_AquaParity(t *testing.T) {
	// checkConstraint is a direct port of aqua's
	// pkg/expr/version_compare.go compare(): six operators over direct
	// go-version comparisons (NOT Constraints.Check, whose prerelease
	// gating aqua doesn't use), comma = AND, commit hashes never match.
	cases := []struct {
		constraint, ver string
		want            bool
	}{
		// Prereleases compare by semver precedence in plain ranges —
		// the divergence from Masterminds that motivated the port.
		{"<= 24.10.0", "24.9.0-rc.1", true},
		{"<= 24.10.0", "24.11.0-rc.1", false},
		{"< 1.0.0", "1.0.0-beta.1", true}, // prerelease sorts below release
		// Operator set.
		{">= 1.0, < 2.0", "1.5.0", true},
		{">= 1.0, < 2.0", "2.0.0", false},
		{"!= 1.2.3", "1.2.4", true},
		{"!= 1.2.3", "1.2.3", false},
		{"= 2.27.0", "2.27.0", true},
		{"> 0.4.5", "0.4.5", false},
		// Commit hashes never match (aqua guard).
		{">= 0.0.1", strings.Repeat("a1", 20), false},
		// Unparseable pieces are false (aqua would panic; we fail soft).
		{"garbage", "1.0.0", false},
		{"~> 1.0", "1.5.0", false}, // pessimistic operator: not in aqua's set
		{">= not-a-version", "1.0.0", false},
	}
	for _, c := range cases {
		if got := checkConstraint(c.constraint, c.ver); got != c.want {
			t.Errorf("checkConstraint(%q, %q) = %v, want %v", c.constraint, c.ver, got, c.want)
		}
	}
}
