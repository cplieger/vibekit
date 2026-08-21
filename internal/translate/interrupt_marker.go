package translate

// kiro-cli's tool-interruption sentinel, and why a client has to read it.
//
// kiro-cli's built-in security filter can cancel a tool call before it executes.
// When it does, it emits ONE assistant text chunk carrying a fixed sentence and
// then goes idle WITHOUT ever answering the session/prompt. Nothing else on the
// wire says the turn is over: no stopReason, no end_turn, no error frame.
//
// For vibekit that is worse than a slow turn. Bridge.Call carries no client-side
// deadline by design, so the prompt slot stays held until the bridge dies —
// until the tab is closed. Every later Send on that chat answers 409 busy, and
// the two adjacent safety nets do not reach it: command.cancelGrace arms only
// after an explicit user cancel, and the empty-turn retry needs the prompt
// response to have arrived first.
//
// Provenance, measured rather than inferred (2026-08-21, kiro-cli 2.19.0): the
// string is ABSENT from KAS's acp-server.js and present four times in the
// `kiro-cli-chat` sidecar `kiro-cli acp` re-execs, attributed there to
// crates/chat-cli-v2/src/agent/acp/acp_agent.rs. kiro-cli's own TUI carries the
// same sentence in a two-member sentinel list beside an exact matcher, which is
// what makes it a contract rather than a log line that happens to be stable.

import "strings"

// interruptSentinel is the whole text of the chunk kiro-cli sends instead of
// ending the turn.
//
// It lives here, beside the matcher, rather than in internal/vibekit: this is a
// foreign system's literal contract, the same class of thing as internal/kascap's
// table and internal/policyfile's format, and those stay with their contract test
// rather than moving to the wire-vocabulary package (see #go-rulebook C17).
const interruptSentinel = "Tool uses were interrupted, waiting for the next user prompt"

// interruptReason is what the transcript's divider says about the stop. The
// sentinel itself is already shown to the user as ordinary assistant text, so
// this adds the ATTRIBUTION the sentence leaves out: which layer stopped the
// turn, and therefore that neither the model nor vibekit failed.
const interruptReason = "Stopped by kiro-cli's tool-use security filter"

// isInterruptSentinel reports whether one assistant text delta IS the sentinel.
//
// Exact equality on the trimmed delta, never a substring or a prefix, and each
// half of that rule is load-bearing.
//
// A SUBSTRING test fires on a model quoting the sentence in prose, which is a
// turn killed mid-thought for writing about the thing that kills turns. A whole
// chunk carrying nothing but the sentence is kiro-cli's own frame with near
// certainty, because ordinary streamed deltas are token-sized.
//
// A PREFIX test would additionally catch the sentinel split across two deltas,
// and is refused: `Tool uses were` is a legal English opening, so the cost of a
// false positive is unbounded while the cost of the miss is the status quo. The
// codebase has the machinery for a correct split match if the wire ever demands
// one — steer_marker.go's bounded carry, keyed on a committing prefix — and that
// is what to reach for, on evidence, rather than loosening this test. There is no
// evidence today: this sentence is a constant kiro-cli emits as its own message,
// not model output being tokenised.
//
// The second member of kiro-cli's TUI list, "Response was interrupted by the
// user", is deliberately NOT matched. It echoes a gesture vibekit already handles
// end to end (CmdCancel clears pending permissions, kills the turn's terminals,
// sends session/cancel and arms the 10s grace for exactly the case where KAS
// never answers), so treating model text as a second trigger could only end a
// turn the cancel path is already ending — or, since the user may not have
// cancelled at all, end one on a quote. The TUI needs both because it has no
// equivalent of that grace budget.
func isInterruptSentinel(text string) bool {
	return strings.TrimSpace(text) == interruptSentinel
}
