package api

import "strings"

// HiddenTags are the bracketed markers used by the UI model picker and
// the bridge to hide end-of-life models from the user. This is a subset
// of the models package's excludedTags: [Internal] and [Experimental]
// models are shown in the picker but excluded from ambient-task selection.
var HiddenTags = []string{
	"[deprecated]",
	"[legacy]",
}

// TagExcluded returns true if text contains any of the given bracketed
// tags (case-insensitive).
func TagExcluded(text string, tags []string) bool {
	if text == "" {
		return false
	}
	low := strings.ToLower(text)
	for _, tag := range tags {
		if strings.Contains(low, tag) {
			return true
		}
	}
	return false
}

// StripCodeFence removes a single leading/trailing markdown code fence
// from model output. Models sometimes wrap their answer in ```lang ... ```
// despite explicit instructions; this is a low-cost safety net that
// matters because the output goes straight into the editor buffer or
// commit message.
//
// Semantics: strips one wrapping fence (the outermost). Intentional
// fences inside the content (e.g. a PR description containing code
// blocks) are preserved. This is the correct behaviour for both the
// git commit-message and server utility-bridge paths.
func StripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (```lang or ```).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	// Drop trailing ``` (optionally followed by whitespace).
	s = strings.TrimRight(s, " \t\n\r")
	if strings.HasSuffix(s, "```") {
		s = strings.TrimRight(s[:len(s)-3], " \t\n\r")
	}
	return s
}
