package hub

// Models returns the current model catalog as reported by any live
// bridge. Bridges should all see the same kiro-cli model set so the
// first one wins; we iterate rather than index to avoid holding any
// particular chat id. Returns nil when no bridge has reported models
// yet (fresh container, no chats opened).
//
// This satisfies models.Snapshotter, letting the git handler ask for
// a cheap model for AI commit messages without anyone shelling out.

import "vibekit/internal/api"

// Models is the Snapshotter contract: the first non-empty model list
// from a live bridge. No aggregation across bridges — they all see
// the same kiro-cli catalog.
func (h *Hub) Models() []api.SessionModel {
	snapshot := h.bridge.mgr.all()
	for _, sb := range snapshot {
		if ms := sb.bridge.Models(); len(ms) > 0 {
			return ms
		}
	}
	return nil
}
