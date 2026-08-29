package agent

// The one test that reads what a RELOAD reads.
//
// Every other test in this package asserts on in-memory state, and every test
// in internal/translate stops at the buffer. So the hop that the whole design
// turns on — a terminal's output reaching the chat FILE, which is the only
// thing a reload has — was untested in both halves: nothing failed if
// `tc.OutputSpans = spans` were deleted outright.
//
// This drives the real chat.Store over a temp dir, so the assertions run
// against bytes that went through MarshalIndent, an atomic rename, and a fresh
// Unmarshal. That is the reload path minus HTTP.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// hubWithRealStore builds a runtime backed by an on-disk chat store, seeded with
// chat c1, and returns the directory the chat files land in. It mirrors
// hubWithBridge but swaps the recording fake for the real thing, because a fake
// store cannot answer the question this file asks. No broadcaster is wired: the
// assertions read the file, and the store nil-checks it.
func hubWithRealStore(t *testing.T) (*Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	cs, err := chat.NewStore(dir)
	if err != nil {
		t.Fatalf("chat.NewStore: %v", err)
	}
	br := newRecordingTermBridge()
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return br }, cs)
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return h, dir
}

// seedTerminal registers a live agent terminal holding raw bytes, the way the
// output pump would have.
func seedTerminal(h *Runtime, id string, chatID vibekit.ChatID, raw string) {
	term := newAgentTerminal(nil, chatID, 1<<20)
	term.output.Write([]byte(raw))
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	h.agentTerms.terms[id] = term
	h.agentTerms.byChatID[chatID] = append(h.agentTerms.byChatID[chatID], id)
}

// sessionUpdate wraps one ACP session/update frame the way the bridge does.
func sessionUpdate(t *testing.T, raw string) *vibekit.RPCResponse {
	t.Helper()
	return &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": json.RawMessage(raw)}),
	}
}

// storedToolCall re-reads chat c1 from DISK and returns the single tool call on
// its last assistant message. Reading the file rather than calling store.Get
// keeps the assertion honest about what is actually persisted: a Get could in
// principle be served from memory, a file cannot.
func storedToolCall(t *testing.T, dir string) vibekit.ToolCall {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(dir, "c1.json"))
	if err != nil {
		t.Fatalf("read chat file (nothing was persisted): %v", err)
	}
	var stored vibekit.Chat
	if err := json.Unmarshal(blob, &stored); err != nil {
		t.Fatalf("unmarshal chat file: %v", err)
	}
	for _, m := range slices.Backward(stored.Messages) {
		if len(m.ToolCalls) > 0 {
			return m.ToolCalls[0]
		}
	}
	t.Fatalf("no persisted message carries a tool call; chat file: %s", blob)
	return vibekit.ToolCall{}
}

// runTerminalTurn drives a complete terminal-backed tool call to turn end:
// the tool call opens, the caller's frames run, and the turn is finalized (which
// is what writes the message to disk).
func runTerminalTurn(t *testing.T, h *Runtime, frames ...string) {
	t.Helper()
	epoch := h.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	h.translateACPEvent("c1", sessionUpdate(t,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending"}`))
	for _, f := range frames {
		h.translateACPEvent("c1", sessionUpdate(t, f))
	}
	h.SettleTurnOnResponse(t.Context(), "c1", epoch, 0, &vibekit.RPCResponse{})
}

const completedWithTerminal = `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1",` +
	`"status":"completed","content":[{"type":"terminal","terminalId":"term-1"}]}`

// TestPersistedToolCall_CarriesTerminalOutputAndSpans is the reload test. A
// coloured command runs, KAS releases the terminal before reporting the result
// (which is the real ordering — measured at ~3ms on a live run), the turn ends,
// and the chat FILE must carry both the plain text and the style spans.
//
// The spans half is the part no other test covers: without it the card reloads
// as unstyled text, which looks plausible and would ship.
func TestPersistedToolCall_CarriesTerminalOutputAndSpans(t *testing.T) {
	h, dir := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "\x1b[31mred\x1b[0m output\n")

	epoch := h.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	h.translateACPEvent("c1", sessionUpdate(t,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending"}`))
	// KAS releases the terminal before it reports the result. That ordering is
	// the reason retired records exist, so the test has to reproduce it.
	if _, released := h.agentTerms.release("term-1"); !released {
		t.Fatal("release reported the terminal was not present")
	}
	h.translateACPEvent("c1", sessionUpdate(t, completedWithTerminal))
	h.SettleTurnOnResponse(t.Context(), "c1", epoch, 0, &vibekit.RPCResponse{})

	tc := storedToolCall(t, dir)
	if tc.Output != "red output\n" {
		t.Errorf("persisted Output = %q, want %q (a reload renders this and nothing else)",
			tc.Output, "red output\n")
	}
	if len(tc.OutputSpans) == 0 {
		t.Fatal("persisted OutputSpans is empty: the reloaded card renders unstyled, " +
			"and the escape sequences are already gone from the text so nothing can recover them")
	}
	// The offsets must address the text that shipped beside them, or the client
	// paints the wrong range. "red" is the styled run.
	s := tc.OutputSpans[0]
	if s.Start != 0 || s.End != 3 {
		t.Errorf("first span = [%d,%d), want [0,3) covering %q", s.Start, s.End, "red")
	}
	if s.End > len(tc.Output) {
		t.Errorf("span end %d exceeds persisted text length %d", s.End, len(tc.Output))
	}
}

// TestPersistedToolCall_AdoptsWhenLinkAndCompletionShareAFrame pins the fold
// ORDER against the file. One frame can carry both the terminal link and
// `completed`; with the status folded first, adoption looked up an id the tool
// call did not have yet and the output was lost for good — the update carrying
// the link had already gone by. In-memory tests cover the fold, but the cost of
// getting it wrong is a persisted empty card, so it is worth pinning here.
func TestPersistedToolCall_AdoptsWhenLinkAndCompletionShareAFrame(t *testing.T) {
	h, dir := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "one frame\n")

	runTerminalTurn(t, h, completedWithTerminal)

	if got := storedToolCall(t, dir).Output; got != "one frame\n" {
		t.Errorf("persisted Output = %q, want %q (the link must be adopted before the status fold)",
			got, "one frame\n")
	}
}

// TestPersistedToolCall_TerminalOutputBeatsAContentFragment pins which side
// wins when both have text. An earlier content block is a FRAGMENT of what the
// terminal holds in full, so preferring the tool call persists the fragment and
// silently truncates the record.
func TestPersistedToolCall_TerminalOutputBeatsAContentFragment(t *testing.T) {
	h, dir := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "line 1\nline 2\nline 3\n")

	runTerminalTurn(t, h,
		// A progress line arrives first, naming the terminal.
		`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress",`+
			`"content":[{"type":"terminal","terminalId":"term-1"},`+
			`{"type":"content","content":{"text":"line 1"}}]}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}`)

	if got := storedToolCall(t, dir).Output; got != "line 1\nline 2\nline 3\n" {
		t.Errorf("persisted Output = %q, want the terminal's full output; "+
			"a fragment here means the record was truncated to the progress line", got)
	}
}

// TestPersistedToolCall_SurvivesTheTurnBoundary pins the eviction timing. The
// retired record is dropped when the turn's own closer finalizes it, so adoption
// has exactly until turn end to happen — and turn end is also when the message is
// written. If the eviction ever moved earlier, this is the test that would catch
// it, because the file is written from the same call that evicts.
func TestPersistedToolCall_SurvivesTheTurnBoundary(t *testing.T) {
	h, dir := hubWithRealStore(t)
	seedTerminal(h, "term-1", "c1", "kept\n")
	// KAS's own release, which lands about 3ms after create and long before the
	// completion that needs the bytes. Without it nothing ever enters `retired`,
	// and the eviction assertion at the end of this test is vacuous — it was,
	// which is why the boundary's eviction had no failing test.
	if _, ok := h.agentTerms.release("term-1"); !ok {
		t.Fatal("Setup: release found no terminal, so nothing is retired to evict")
	}

	runTerminalTurn(t, h, completedWithTerminal)

	if got := storedToolCall(t, dir).Output; got != "kept\n" {
		t.Errorf("persisted Output = %q, want %q", got, "kept\n")
	}
	// And the record is gone afterwards: the turn is closed, so nothing can
	// adopt it a second time and the map does not grow without bound.
	h.agentTerms.mu.Lock()
	remaining := len(h.agentTerms.retired)
	h.agentTerms.mu.Unlock()
	if remaining != 0 {
		t.Errorf("retired records after turn end = %d, want 0 (the finalizer must evict)", remaining)
	}
}
