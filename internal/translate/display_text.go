package translate

// One rule for every string this package puts on a human-read surface.

import "github.com/cplieger/runesafe"

// maxDisplayTextBytes bounds one upstream string on its way to a banner, a
// permission card, or a question card. Nothing on the wire bounds any of them:
// KAS's notification params, a tool call's title and the agent's own question
// text all arrive length-free, and each lands in an SSE payload the server also
// logs. 512 bytes is comfortably above every legitimate value (the longest real
// tool-call title measured in this repo's fixtures is under 60) and below the
// point where one card can push the rest of the dock off screen.
const maxDisplayTextBytes = 512

// displayText prepares one upstream string for a single-line human-read surface:
// runesafe's single-line preset, capped on a rune boundary.
//
// The preset REPLACES rather than deletes: C0/C1 controls, DEL, the twelve
// Bidi_Control runes and the paragraph separators each become a space, so a
// deception becomes visible whitespace instead of silently vanishing. That is
// the whole point at a decision surface. Measured on runesafe v1.4.2 with a
// title reading `Run <RLO>dnuof-emaN- ecapskrow/ fr- mr<PDF>` — which renders as
// an innocuous find command while `rm -rf /workspace` is what gets approved — the
// sanitized form carries 0 of its 2 Bidi controls and the reversal is on screen.
//
// It is NOT sanitize.Output. That one strips ANSI and DELETES hidden runes, which
// is right for tool output headed into a transcript and wrong here twice over:
// these are JSON string fields rather than terminal bytes, and a deletion leaves
// the deception in place with its evidence removed. A newline is likewise a
// defect on a single-line surface rather than something to preserve.
//
// Measured cost, same version: of eleven legitimate label shapes — ASCII,
// accents, Hebrew, Arabic, CJK, Korean, Thai, Devanagari, emoji, and ASCII mixed
// with Hebrew — ten are byte-identical. Pure RTL survives untouched because the
// Unicode bidi algorithm derives direction from strong characters alone. The one
// that changes is a label that mixes scripts AND carries explicit direction marks
// (`Write <LRM>שלום<LRM> now`), whose marks become spaces. That is the entire
// price, and it is paid only by a label that could also be the attack.
//
// Sibling rule in another package: auth.identityText, same preset at a 256-byte
// bound for an identity row.
func displayText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxDisplayTextBytes)
}
