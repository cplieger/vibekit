// Package logsafe binds runesafe's single-line preset to this app's log
// surface, so every untrusted string reaches slog through one policy.
//
// The threat is log-record forgery. A request-controlled value — a file path,
// a query parameter, a JSON body field, a ref, an error text that interpolates
// one of those — carries a raw newline straight through slog's text handler
// into Loki, where the reader cannot tell an injected line from one the server
// wrote. The other three runesafe classes ride the same channel: C1 controls
// and ESC introduce terminal escape sequences that retitle a terminal or write
// to its clipboard, Bidi_Control runes reorder what a human reads without
// changing what compares, and U+2028/U+2029 split a record for a JavaScript
// viewer. Each becomes a space here, so the deception shows up as visible
// whitespace rather than vanishing along with the evidence of it.
//
// The bound lives in this package rather than at each caller, which is the
// opposite of the choice internal/sanitize documents for its single-line
// surfaces. There the bound is a property of the surface and the surfaces
// differ (an identity row at 256 bytes, a display text at 512). Here there is
// ONE surface — a slog attribute — so a per-caller bound would only be a
// number to get wrong, and a single constant is what lets the policy move in
// one edit.
//
// Sibling of internal/sanitize, which defuses MULTI-LINE agent output for a
// transcript a human reads, and of internal/logctl, which owns the level.
// Reach for this one at any slog attribute whose value the app did not write.
package logsafe

import "github.com/cplieger/runesafe/v2"

// MaxFieldBytes bounds one sanitized attribute. Long enough for a workspace
// path or an upstream error sentence, short enough that a hostile value cannot
// push the useful attributes off the end of a log line.
const MaxFieldBytes = 256

// Field prepares one untrusted string for a slog attribute: runesafe's
// single-line preset, then a cap on a rune boundary with a "..." marker.
//
// Route every untrusted attribute through it rather than picking the ones that
// look dangerous. A reader of a handler should not have to prove which of four
// attributes is safe, and the cost is one call.
func Field(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, MaxFieldBytes)
}
