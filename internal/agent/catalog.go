package agent

import (
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// Catalog is the workspace's mode and model catalog: what KAS says this
// workspace can run, held once.
//
// It exists because the same two lists used to be stamped onto every chat
// record and onto every ChatHeader. Per-field measurement of the 1.25 MiB
// /api/chats response: available_modes 1,236,118 B (93.1%), available_models
// 73,090 B (5.5%), everything else under 1% — 59 modes repeated across 29
// chats, identical in all of them, fetched twice per boot. They are a
// workspace fact wearing a per-chat costume.
//
// What stays per-chat is the chat's CHOICE: CurrentModeID, Model, Effort. Those
// differ between chats; the vocabulary they are chosen from does not.
//
// The stored modes are the ones KAS reported, so the shadowing between a
// workspace agent and a bundled mode of the same id arrives already resolved.
// A client must not re-derive that.
type Catalog struct {
	modes  []vibekit.SessionMode
	models []vibekit.SessionModel
	mu     sync.Mutex
}

// SetModes replaces the mode vocabulary, reporting whether it changed.
//
// An EMPTY list is ignored, and that is the same rule applyLoadedSessionFacts
// needed when these lived on the chat: session/load omits the catalog routinely
// (KAS resolves it asynchronously), and modes have no repair channel — a live
// config_option_update carries models, never modes. So an emptied mode list
// would stay empty for the rest of the session.
func (c *Catalog) SetModes(modes []vibekit.SessionMode) bool {
	if len(modes) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Equal(c.modes, modes) {
		return false
	}
	c.modes = slices.Clone(modes)
	return true
}

// SetModels replaces the model catalog, reporting whether it changed. Empty is
// ignored for SetModes's reason.
func (c *Catalog) SetModels(models []vibekit.SessionModel) bool {
	if len(models) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Equal(c.models, models) {
		return false
	}
	c.models = slices.Clone(models)
	return true
}

// Modes returns the mode vocabulary, or nil before anything has reported one.
//
// A clone, because the caller is a JSON encoder or a picker and the holder's
// slice must not be reachable from either. SessionMode holds only strings, so
// one level of copy is the whole value.
func (c *Catalog) Modes() []vibekit.SessionMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.modes)
}

// Models returns the model catalog, or nil before anything has reported one.
// A clone, for Modes's reason.
func (c *Catalog) Models() []vibekit.SessionModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.models)
}

// DefaultEffortFor returns the model's own default reasoning tier, or "" when
// the catalog does not know that model.
//
// Here rather than at the caller because the catalog is the only thing that
// knows: the caller (a model switch) has an id and needs the tier that id runs
// at, and reaching into a cloned slice to look it up would copy the whole
// catalog to read one field.
func (c *Catalog) DefaultEffortFor(model string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.models {
		if c.models[i].ID == model {
			return c.models[i].DefaultEffortLevel
		}
	}
	return ""
}
