// Package gitea provides the Gitea/Codeberg ForgeOps implementation.
package gitea

import "vibekit/internal/forges"

// New returns a ForgeOps for Gitea at the given host.
func New(host string) (forges.ForgeOps, error) {
	return forges.New(forges.KindGitea, host)
}
