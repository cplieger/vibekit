package agent

// Models satisfies models.Snapshotter, letting the git handler ask for a cheap
// model for AI commit messages without anyone shelling out.

import "github.com/cplieger/vibekit/internal/vibekit"

// Models returns the first non-empty model catalog from a live bridge. Bridges
// all see the same kiro-cli model set, so no aggregation is needed. Returns nil
// when no bridge has reported models yet.
func (rt *Runtime) Models() []vibekit.SessionModel {
	snapshot := rt.bridge.mgr.all()
	for _, sb := range snapshot {
		if ms := sb.bridge.Models(); len(ms) > 0 {
			return ms
		}
	}
	return nil
}
