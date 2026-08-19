package hub

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/modeltext"
)

// cheapestModel returns the cheapest reliable model id from the current
// catalog, or "" if nothing is live. Filters out:
//   - "auto" (task-based selection, not a real model)
//   - [Deprecated], [Legacy] (end-of-life)
//   - [Internal] (not available to all users)
//   - [Experimental] (unstable, may produce poor results)
//
// Selects by lowest RateMultiplier among eligible models. If no model
// has a rate (all zero, e.g. session/new doesn't send it), falls back
// to the first eligible entry.
func cheapestModel(_ context.Context, catalog []api.SessionModel) string {
	var bestID string
	var bestRate float64
	for _, m := range catalog {
		if m.ID == "" || m.ID == modelAuto {
			continue
		}
		if modelExcluded(m.Name) || modelExcluded(m.Description) {
			continue
		}
		switch {
		case bestID == "":
			bestID, bestRate = m.ID, m.RateMultiplier
		case m.RateMultiplier > 0 && (bestRate == 0 || m.RateMultiplier < bestRate):
			bestID, bestRate = m.ID, m.RateMultiplier
		}
	}
	return bestID
}

// excludedTags are the bracketed markers that disqualify a model from
// ambient-task selection: the hidden set plus two more. [internal] and
// [experimental] models are SHOWN in the picker and only skipped here, which is
// why this policy is hub's and modeltext.Hidden is not widened to cover it.
var excludedTags = append(modeltext.HiddenTags(), "[internal]", "[experimental]")

// modelExcluded returns true if the text contains any bracketed tag
// that marks the model as unreliable for ambient tasks.
func modelExcluded(text string) bool {
	return modeltext.HasAnyTag(text, excludedTags)
}
