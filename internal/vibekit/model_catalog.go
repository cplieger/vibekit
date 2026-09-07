package vibekit

import "slices"

// ApplyServedModels records the entitlement ids the session advertised, UNFILTERED,
// and reports whether the chat changed. Empty input means the backend supplied no
// catalog and preserves what the chat already holds, so a model the account can
// still run is never refused because one read came back silent.
//
// The DISPLAY catalog is deliberately not here: it is workspace-wide, it lives
// once in agent.Catalog, and copying it onto every chat is what made /api/chats
// 1.25 MiB (see TestChat_PersistsNoWorkspaceCatalog).
func ApplyServedModels(c *Chat, unfiltered []SessionModel) bool {
	if len(unfiltered) == 0 {
		return false
	}
	served := make([]string, 0, len(unfiltered))
	for _, model := range unfiltered {
		if model.ID == "" {
			continue
		}
		served = append(served, model.ID)
	}
	if slices.Equal(c.ServedModelIDs, served) {
		return false
	}
	c.ServedModelIDs = served
	return true
}
