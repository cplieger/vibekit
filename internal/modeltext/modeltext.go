// Package modeltext reads the strings a MODEL authored and answers what vibekit
// needs from them: whether a catalog entry's description marks the model hidden,
// and what a completion says once its markdown wrapper is off.
//
// Every symbol here takes text vibekit did not write and did not ask to be
// shaped that way, and returns the part that is usable. That is one job read
// over two inputs — the model's description of itself, and the model's answer.
//
// It exists because this is BEHAVIOUR, and it used to sit in internal/vibekit beside
// the wire and domain TYPES. Five packages call it (bridge, agent and translate
// for the tags; git and server for the fences), none of which owns the others.
// internal/git was the obvious candidate for the fence half, holding 3 of its 4
// production call sites — declined because internal/server holds the fourth and
// does NOT import git today, so that home would put a markdown helper behind a
// dependency on a package of HTTP handlers.
//
// HiddenTags is a function rather than the exported slice it used to be. A
// package-level []string is writable by every importer, and one of them already
// had to defensively copy it before extending.
package modeltext

import (
	"slices"
	"strings"
)

// hiddenTags are the bracketed markers used by the UI model picker and the
// bridge to hide end-of-life models from the user.
//
// Deliberately NARROWER than internal/agent's ambient-selection set, which adds
// [internal] and [experimental]: those models are SHOWN in the picker and merely
// excluded from ambient-task selection, so the two policies are not the same
// list and must not be merged into one.
var hiddenTags = []string{
	"[deprecated]",
	"[legacy]",
}

// HiddenTags returns the hidden-model markers as a fresh slice, for a caller
// composing a wider policy on top. A caller that only needs the standard rule
// should use Hidden instead.
func HiddenTags() []string {
	return slices.Clone(hiddenTags)
}

// Hidden reports whether a model's description marks it hidden from the user.
// This is the rule the picker and the bridge apply; it is a named policy rather
// than a tag list four call sites happen to pass the same way.
func Hidden(description string) bool {
	return HasAnyTag(description, hiddenTags)
}

// HasAnyTag reports whether text contains any of the given bracketed tags.
// The tags must already be lowercase; text is folded before comparison.
func HasAnyTag(text string, tags []string) bool {
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
