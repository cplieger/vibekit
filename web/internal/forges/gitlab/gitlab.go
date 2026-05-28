// Package gitlab provides the GitLab ForgeOps implementation.
package gitlab

import "vibekit/internal/forges"

// New returns a ForgeOps for GitLab at the given host.
func New(host string) (forges.ForgeOps, error) {
	return forges.New(forges.KindGitLab, host)
}
