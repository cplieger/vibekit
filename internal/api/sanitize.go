package api

import (
	"regexp"
	"strings"
)

// --- Terminal helpers ---

// ansiRe matches the ANSI escape sequences produced by kiro-cli and its
// subprocesses. Covered forms: CSI (`ESC [ ... letter`), OSC
// (`ESC ] ... BEL` or `ESC ] ... ESC \`), charset-select
// (`ESC ( x` / `ESC ) x`), and single-character controls
// (`ESC 7`, `ESC 8`, `ESC c`, `ESC N`, `ESC O`, `ESC P`, `ESC X`,
// `ESC ^`, `ESC _`, `ESC =`, `ESC >`).
// 8-bit C1 controls (raw bytes 0x9b..0x9f) are not matched because Go's
// regexp engine operates on runes over UTF-8 and those raw bytes are
// invalid UTF-8; kiro-cli always uses the 7-bit ESC-prefixed forms.
var ansiRe = regexp.MustCompile(
	`\x1b\[[0-9;?]*[a-zA-Z]` + // CSI
		`|\x1b\][\s\S]*?(?:\x07|\x1b\\)` + // OSC
		`|\x1b[()][A-Za-z0-9]` + // charset select
		`|\x1b[NOPX^_78=>c]`, // SS2/SS3/DCS/SOS/PM/APC/save/restore/RIS/keypad
)

// StripANSI removes ANSI escape sequences from a string.
func StripANSI(s string) string {
	for {
		out := ansiRe.ReplaceAllString(s, "")
		if out == s {
			return out
		}
		s = out
	}
}

// SanitizeUnicode strips hidden Unicode characters that could be used
// for prompt injection via tool output. Covers TAG characters
// (U+E0000-E007F), zero-width spaces/joiners, format controls, and
// other invisible codepoints that Q Developer CLI's ExecuteCmd also
// strips. Apply alongside StripANSI on all tool output before
// persisting to chat files.
func SanitizeUnicode(s string) string {
	return strings.Map(func(r rune) rune {
		if isHiddenUnicode(r) {
			return -1
		}
		return r
	}, s)
}

// isHiddenUnicode reports whether r is an invisible Unicode codepoint
// used for prompt injection: TAG characters, zero-width spaces/joiners,
// bidi controls, format controls, and soft hyphens.
func isHiddenUnicode(r rune) bool {
	if r >= 0xE0000 && r <= 0xE007F {
		return true // TAG characters
	}
	switch r {
	case 0x00AD, // soft hyphen
		0x200B, 0x200C, 0x200D, // zero-width space/non-joiner/joiner
		0x200E, 0x200F, // LTR/RTL marks
		0xFEFF,                                 // BOM / zero-width no-break space
		0x2060, 0x2061, 0x2062, 0x2063, 0x2064: // word joiner + invisible math
		return true
	}
	if r >= 0x202A && r <= 0x202E {
		return true // bidi embedding/override
	}
	if r >= 0x2066 && r <= 0x2069 {
		return true // bidi isolate
	}
	return false
}

// SanitizeOutput applies both ANSI stripping and Unicode sanitization,
// iterating to a fixed point. A single pass is not enough: removing a
// hidden Unicode char (e.g. a zero-width space inside "\x1b(\u200b0")
// can complete an escape sequence that the next StripANSI pass then
// strips. Iterating guarantees the result is fully sanitized — no
// residual escapes an attacker hid behind zero-width chars — and makes
// the function idempotent.
//
// Termination: SanitizeUnicode normalizes any invalid UTF-8 byte to a
// single U+FFFD rune (via strings.Map), so after the first pass the
// string is valid UTF-8 and every subsequent pass only removes runes.
// The rune count is therefore non-increasing and the fixed point is
// reached in O(len(s)) passes. (Byte length is NOT monotone — an
// invalid byte expands to a 3-byte U+FFFD on the first pass — so the
// guarantee is stated in runes, not bytes.) The output is always valid
// UTF-8, safe to persist to JSON chat files or echo to clients.
func SanitizeOutput(s string) string {
	for {
		out := SanitizeUnicode(StripANSI(s))
		if out == s {
			return out
		}
		s = out
	}
}
