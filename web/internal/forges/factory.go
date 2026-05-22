// ForgeOps factory: construct a provider from a kind + host pair.

package forges

import "fmt"

// New returns a ForgeOps for the given kind + host. host="" maps to
// the kind's default host (or returns an error for self-hosted Gitea
// where no default exists).
func New(kind Kind, host string) (ForgeOps, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("forges: unknown kind %q", kind)
	}
	if host == "" {
		host = kind.DefaultHost()
	}
	if host == "" {
		return nil, fmt.Errorf("forges: kind %q requires a host", kind)
	}
	switch kind {
	case KindGitHub:
		return newGitHub(host), nil
	case KindGitLab:
		return newGitLab(host), nil
	case KindGitea, KindCodeberg:
		return newGitea(kind, host), nil
	}
	return nil, fmt.Errorf("forges: unhandled kind %q", kind)
}
