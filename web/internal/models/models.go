// Package models answers "what model should we use for ambient tasks
// (e.g. AI commit messages)?" The answer is the cheapest-by-rate
// model the running ACP bridges expose, or "" if no bridge is live.
package models

import (
	"context"

	"vibekit/internal/api"
)

// CheapestModel returns the cheapest reliable model id from the current
// catalog, or "" if nothing is live. Filters out:
//   - "auto" (task-based selection, not a real model)
//   - [Deprecated], [Legacy] (end-of-life)
//   - [Internal] (not available to all users)
//   - [Experimental] (unstable, may produce poor results)
//
// Selects by lowest RateMultiplier among eligible models. If no model
// has a rate (all zero, e.g. session/new doesn't send it), falls back
// to the first eligible entry.
//
// The ctx parameter is accepted for call-site symmetry but is not
// honoured: the implementation reads an in-memory snapshot and never
// blocks.
func CheapestModel(_ context.Context, catalog []api.SessionModel) string {
	var bestID string
	var bestRate float64
	for _, m := range catalog {
		if m.ID == "" || m.ID == "auto" {
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
// ambient-task selection.
var excludedTags = func() []string {
	return append(append([]string{}, api.HiddenTags...), "[internal]", "[experimental]")
}()

// modelExcluded returns true if the text contains any bracketed tag
// that marks the model as unreliable for ambient tasks.
func modelExcluded(text string) bool {
	return api.TagExcluded(text, excludedTags)
}
