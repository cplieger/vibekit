package uistate

import (
	"maps"
	"slices"
)

// Bounds on the arrangement. Each is an outer wall rather than a product
// decision: a real strip is tens of tabs, and the point is that a browser bug or
// a hostile client cannot grow the document without limit.
const (
	MaxTabs             = 500
	MaxEditorFiles      = 500
	MaxDismissedBanners = 200
	MaxFoldChats        = 500
	MaxFoldTurnsPerChat = 2000
	MaxStringLen        = 4096
)

// Sanitize returns a copy with every field validated and bounded.
//
// It runs on the way IN (a client wrote it) and on the way OUT of the file (the
// previous process wrote it, or a person hand-edited it). Both directions matter:
// this document feeds the boot path, and a tab list with a 10 MB string in it
// would take the strip down on every load with no way to reach the setting that
// clears it.
//
// Invalid entries are DROPPED individually rather than failing the document,
// because the arrangement's failure mode should be a missing tab, not a blank
// app. The one field that is validated as a whole is Theme, which has three
// legal values and no partial reading.
func Sanitize(s *State) State {
	out := State{
		TabOrder:         boundedIDs(s.TabOrder, MaxTabs),
		PinnedTabs:       boundedIDs(s.PinnedTabs, MaxTabs),
		EditorFiles:      boundedIDs(s.EditorFiles, MaxEditorFiles),
		DismissedBanners: boundedIDs(s.DismissedBanners, MaxDismissedBanners),
		FBPath:           boundedString(s.FBPath),
		TurnFolds:        boundedFolds(s.TurnFolds),
	}
	switch s.Theme {
	case "dark", "light", "system":
		out.Theme = s.Theme
	default:
		// Includes "": no recorded choice, which is a legal state.
		out.Theme = ""
	}
	// A pin naming a tab that is not open is dead weight the strip cannot show.
	// Dropped here rather than in the client so the file cannot accumulate pins
	// for tabs closed months ago.
	open := make(map[string]struct{}, len(out.TabOrder))
	for _, id := range out.TabOrder {
		open[id] = struct{}{}
	}
	out.PinnedTabs = slices.DeleteFunc(out.PinnedTabs, func(id string) bool {
		_, ok := open[id]
		return !ok
	})
	return out
}

// boundedIDs drops empty and oversized entries, dedups, and truncates to max.
// Order is preserved: for TabOrder it IS the display order.
func boundedIDs(in []string, limit int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(in), limit))
	seen := make(map[string]struct{}, min(len(in), limit))
	for _, s := range in {
		if s == "" || len(s) > MaxStringLen {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
		if len(out) == limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundedString(s string) string {
	if len(s) > MaxStringLen {
		return ""
	}
	return s
}

// boundedFolds validates the nested fold map, dropping at each level rather than
// failing the whole document — the same discipline the client-side reader used.
func boundedFolds(in map[string]map[string]bool) map[string]map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]bool, min(len(in), MaxFoldChats))
	for chatID, byTurn := range in {
		if len(out) == MaxFoldChats {
			break
		}
		if chatID == "" || len(chatID) > MaxStringLen {
			continue
		}
		if turns := boundedTurns(byTurn); len(turns) > 0 {
			out[chatID] = turns
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// boundedTurns validates one chat's fold entries. Not maps.Copy: the cap has to
// be enforced while copying, and a copy that ignores it is the thing this bounds.
func boundedTurns(byTurn map[string]bool) map[string]bool {
	if len(byTurn) == 0 {
		return nil
	}
	turns := make(map[string]bool, min(len(byTurn), MaxFoldTurnsPerChat))
	for turnID, open := range byTurn {
		if len(turns) == MaxFoldTurnsPerChat {
			break
		}
		if turnID == "" || len(turnID) > MaxStringLen {
			continue
		}
		turns[turnID] = open
	}
	return turns
}

// cloneState deep-copies, so a caller cannot reorder the store's own state
// through the value it was handed. The slices are the reason: a shallow copy
// shares their backing arrays, and TabOrder is exactly the field a client
// reorders in place.
func cloneState(s *State) State {
	out := State{
		TabOrder:         slices.Clone(s.TabOrder),
		PinnedTabs:       slices.Clone(s.PinnedTabs),
		EditorFiles:      slices.Clone(s.EditorFiles),
		DismissedBanners: slices.Clone(s.DismissedBanners),
		FBPath:           s.FBPath,
		Theme:            s.Theme,
	}
	if s.TurnFolds != nil {
		out.TurnFolds = make(map[string]map[string]bool, len(s.TurnFolds))
		for chatID, byTurn := range s.TurnFolds {
			out.TurnFolds[chatID] = maps.Clone(byTurn)
		}
	}
	return out
}
