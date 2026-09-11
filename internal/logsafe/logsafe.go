// Package logsafe binds runesafe's single-line preset to this app's log
// surface, so every untrusted string reaches slog through one policy.
//
// # What slog already escapes, measured
//
// The threat this package was written for is log-record forgery, and the premise
// was that a request-controlled value — a file path, a query parameter, a JSON
// body field, a ref, an error text interpolating one of those — carries a raw
// newline through slog into Loki, where the reader cannot tell an injected line
// from one the server wrote. That premise is FALSE for the handler vibekit
// installs, so the value this package delivers is not the one it claimed.
//
// Measured on go1.27.1, one attribute per class:
//
//   - TextHandler quotes every value, so ALL of it is escaped: \n \t \r, ESC and
//     the C0 controls, the C1 controls, the Bidi_Control runes, U+2028/U+2029.
//   - JSONHandler escapes \n \t \r, ESC and C0, and U+2028/U+2029, and passes
//     the C1 controls and the Bidi_Control runes through RAW.
//
// vibekit runs the TEXT handler (internal/logctl calls slogx.Setup with a zero
// Options, whose Format zero value is Text), so today no class in that list
// reaches Loki unescaped through slog.
//
// # What the policy is therefore for
//
// The BOUND, which is the half no handler provides: neither one caps an
// attribute, so a hostile value pushes the useful attributes off the end of a
// log line and balloons the record past downstream pipeline limits. That is the
// durable reason to route a value through here.
//
// The rune half is conditional rather than dead, and both conditions are one
// edit away. A format flip to JSON puts C1 and bidi runes on the wire raw — bidi
// reorders what a human reads without changing what compares, C1 introduces
// terminal escape sequences to whatever renders the line. And a value that
// reaches a sink slog does not own is escaped by nothing at all. Each becomes a
// space here, so the deception shows up as visible whitespace rather than
// vanishing along with the evidence of it.
//
// # Why the bound lives here
//
// In this package rather than at each caller, which is the opposite of the
// choice internal/sanitize documents for its single-line surfaces. There the
// bound is a property of the surface and the surfaces differ (an identity row at
// 256 bytes, a display text at 512). Here there is ONE surface — a slog
// attribute — so a per-caller bound would only be a number to get wrong, and a
// single constant is what lets the policy move in one edit.
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
