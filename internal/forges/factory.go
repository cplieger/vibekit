// ForgeOps factory: construct a provider from a kind + host pair.

package forges

import "fmt"

// New returns a ForgeOps for the given kind + host. host="" maps to
// the kind's default host (or returns an error for self-hosted Gitea
// where no default exists).
func New(kind Kind, host string) (ForgeOps, error) {
	m, ok := kindMeta[kind]
	if !ok {
		return nil, fmt.Errorf("forges: unknown kind %q", kind)
	}
	if host == "" {
		host = m.DefaultHost
	}
	if host == "" {
		return nil, fmt.Errorf("forges: kind %q requires a host", kind)
	}
	return m.NewProvider(kind, host), nil
}
