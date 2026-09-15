package agent

import (
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Catalog is the workspace's mode and model catalog: what KAS says this
// workspace can run, held once for the whole workspace rather than per chat. A
// chat owns only its CHOICE (CurrentModeID, Model, Effort), not the vocabulary.
//
// The stored modes are the ones KAS reported, so the shadowing between a
// workspace agent and a bundled mode of the same id arrives already resolved.
// A client must not re-derive it.
type Catalog struct {
	// versions holds the `catalog` counter, bumped under mu when either list
	// changes; nil defaults to a private registry on first use.
	versions *subject.Versions
	modes    []vibekit.SessionMode
	models   []vibekit.SessionModel
	mu       sync.Mutex
}

// registry returns the versions the catalog mints into. Callers hold mu.
func (c *Catalog) registry() *subject.Versions {
	if c.versions == nil {
		c.versions = &subject.Versions{}
	}
	return c.versions
}

// SetModes replaces the mode vocabulary, reporting whether it changed. An EMPTY
// list is ignored: session/load routinely omits the catalog while KAS resolves
// it, and modes have no repair channel (config_option_update carries models
// only), so an emptied mode list would stay empty for the whole session.
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
	c.registry().BumpCounter(subject.KindCatalog, "")
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
	c.registry().BumpCounter(subject.KindCatalog, "")
	return true
}

// ModesModelsStamped returns both lists with the `catalog` stamp, counter first
// and lists second under one lock, for the REST envelope and the resolver.
func (c *Catalog) ModesModelsStamped() (modes []vibekit.SessionMode, models []vibekit.SessionModel, stamp *vibekit.SubjectStamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	version, _ := c.registry().Current(subject.KindCatalog, "")
	return slices.Clone(c.modes), slices.Clone(c.models), vibekit.NewSubjectStamp(string(subject.KindCatalog), "", version)
}

// DefaultEffortFor returns the model's own default reasoning tier, or "" when
// the catalog does not know that model. Here rather than at the caller so a
// lookup of one field does not clone the whole catalog.
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
