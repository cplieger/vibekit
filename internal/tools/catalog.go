package tools

import (
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
)

// loadCatalog reads the compiled tool catalog baked into the image. A
// missing or unreadable catalog degrades gracefully: search returns
// nothing and only manual/ecosystem sources install.
func loadCatalog(path string) *Catalog {
	empty := &Catalog{Entries: map[string]CatalogEntry{}}
	if path == "" {
		return empty
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("tools: catalog unavailable", "path", path, "error", err)
		return empty
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Error("tools: catalog unreadable", "path", path, "error", err)
		return empty
	}
	if c.Entries == nil {
		c.Entries = map[string]CatalogEntry{}
	}
	slog.Info("tools: catalog loaded", "entries", len(c.Entries))
	return &c
}

// Lookup finds a catalog entry by name or alias.
func (c *Catalog) Lookup(name string) (CatalogEntry, bool) {
	if e, ok := c.Entries[name]; ok {
		return e, true
	}
	for k := range c.Entries {
		if slices.Contains(c.Entries[k].Aliases, name) {
			return c.Entries[k], true
		}
	}
	return CatalogEntry{}, false
}

// searchLimit caps catalog search responses.
const searchLimit = 25

// Search ranks catalog entries against a query: exact name, name
// prefix, alias, name substring, then description substring. Empty
// query returns the featured set.
func (c *Catalog) Search(query string) []CatalogEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return c.Featured()
	}
	type scored struct {
		e     CatalogEntry
		score int
	}
	var hits []scored
	for name := range c.Entries {
		e := c.Entries[name]
		score := matchScore(name, &e, q)
		if score == 0 {
			continue
		}
		hits = append(hits, scored{e, score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].e.Name < hits[j].e.Name
	})
	lim := min(len(hits), searchLimit)
	out := make([]CatalogEntry, 0, lim)
	for i := range hits[:lim] {
		out = append(out, hits[i].e)
	}
	return out
}

func matchScore(name string, e *CatalogEntry, q string) int {
	ln := strings.ToLower(name)
	switch {
	case ln == q:
		return 100
	case strings.HasPrefix(ln, q):
		return 80
	}
	for _, a := range e.Aliases {
		la := strings.ToLower(a)
		if la == q {
			return 90
		}
		if strings.HasPrefix(la, q) {
			return 70
		}
	}
	if strings.Contains(ln, q) {
		return 50
	}
	if strings.Contains(strings.ToLower(e.Description), q) {
		return 20
	}
	return 0
}

// Featured returns the curated starter set (empty-state content),
// sorted by name.
func (c *Catalog) Featured() []CatalogEntry {
	var out []CatalogEntry
	for k := range c.Entries {
		if c.Entries[k].Featured {
			out = append(out, c.Entries[k])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > searchLimit {
		out = out[:searchLimit]
	}
	return out
}
