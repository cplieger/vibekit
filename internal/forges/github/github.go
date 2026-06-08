// Package github provides the GitHub ForgeOps implementation.
package github

import "github.com/cplieger/vibekit/internal/forges"

// New returns a ForgeOps for GitHub at the given host.
func New(host string) (forges.ForgeOps, error) {
	return forges.New(forges.KindGitHub, host)
}
