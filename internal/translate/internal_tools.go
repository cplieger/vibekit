package translate

import "sync"

// Internal engine bookkeeping announced as tool calls. KAS emits these through
// its deterministicToolCalls channel during session machinery — not as agent
// work — and its own TUI never renders them, so vibekit drops the frames at
// translate (user decision, 2026-08-31). The one member today is the
// cloud-config fetch that runs during session creation; keying on
// _meta.kiro.toolId rather than the display title keeps the match machine-exact.
//
// Dropping is load-bearing beyond noise: the fetch runs BEFORE the prompt's
// turn opens, so its tool_call used to open a wireTurnStart turn the prompt then
// displaced — persisting a fragment assistant message with TurnOutcome "unknown"
// and a forever-in_progress tool card, which split the user's first turn in two
// on every fresh session. The client keeps a title-keyed twin
// (static-src/tool-schema.ts INTERNAL_TOOL_TITLES) only for transcripts
// persisted before this suppression existed.
func isInternalTool(toolID string) bool {
	return toolID == "fetch_cloud_config"
}

// suppressedTools remembers the ids of dropped internal tool_call frames until
// their follow-up tool_call_update is dropped too. A set rather than relying on
// the id-not-buffered fallback because TurnFoldTarget OPENS a wire turn for any
// frame it is asked about — the completion arriving alone would re-create the
// fragment turn as an empty one.
//
// take removes on hit, so the set is bounded by the number of in-flight
// internal tools (one per session creation in practice). An update that never
// arrives leaks one string per session, which the bridge's lifetime bounds.
type suppressedTools struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func newSuppressedTools() *suppressedTools {
	return &suppressedTools{ids: make(map[string]struct{})}
}

func (s *suppressedTools) add(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = struct{}{}
}

// take reports whether id was suppressed, forgetting it on the way: one
// tool_call has at most one terminal update on this wire.
func (s *suppressedTools) take(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	if ok {
		delete(s.ids, id)
	}
	return ok
}
