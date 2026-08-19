package api

import (
	"testing"
)

// Tests for sanitize.go: StripANSI, SanitizeUnicode, isHiddenUnicode and the
// SanitizeOutput composition. The JSON-writer tests that used to share this
// file moved with their subject to internal/httpwire.

// --- StripANSI ---

func TestStripANSI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mbold green\x1b[0m", "bold green"},
		{"\x1b]0;title\x07rest", "rest"},
		{"no escapes", "no escapes"},
		{"", ""},
	}
	for _, tt := range tests {
		got := StripANSI(tt.in)
		if got != tt.want {
			t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripANSI_edge_cases(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"multiple", "\x1b[31ma\x1b[32mb\x1b[0m", "ab"},
		{"unicode", "\x1b[31méñ\x1b[0m", "éñ"},
		{"bare escape", "before\x1bafter", "before\x1bafter"},
		{"OSC no terminator", "\x1b]0;titlewithoutbell", "\x1b]0;titlewithoutbell"},
		{"OSC BEL", "\x1b]0;title\x07after", "after"},
		{"OSC ST", "\x1b]0;title\x1b\\rest", "rest"},
		{"semicolons", "\x1b[1;31;4mx\x1b[0m", "x"},
		{"empty params", "\x1b[mreset", "reset"},
		{"CSI private mode", "\x1b[?25lhidden\x1b[?25h", "hidden"},
		{"charset G0 select", "\x1b(Btext", "text"},
		{"charset G1 line-draw", "\x1b)0box", "box"},
		{"save and restore cursor", "\x1b7pos\x1b8", "pos"},
		{"RIS reset", "\x1bcreset", "reset"},
		{"SS2 / SS3", "\x1bNa\x1bOb", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripANSI_is_idempotent(t *testing.T) {
	inputs := []string{
		"", "plain", "\x1b[31mred\x1b[0m",
		"mix\x1b[1;32mbold\x1b[0mplain\x1b]0;t\x07end",
		"\x1b[mno-params", "bare\x1b-escape",
		"\x1b]0;title\x1b\\done",
		"\x1b(Btext\x1b)0line",
	}
	for _, in := range inputs {
		once := StripANSI(in)
		twice := StripANSI(once)
		if once != twice {
			t.Errorf("StripANSI not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

// --- SanitizeUnicode / isHiddenUnicode / SanitizeOutput ---

func TestIsHiddenUnicode_classifies_every_branch(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"ascii letter", 'a', false},
		{"ascii digit", '0', false},
		{"ascii space", ' ', false},
		{"accented latin", 'é', false},
		{"cjk", '漢', false},
		{"emoji", '😀', false},
		{"tab (visible whitespace)", '\t', false},
		{"newline", '\n', false},
		{"TAG block lower bound", 0xE0000, true},
		{"TAG mid", 0xE0040, true},
		{"TAG upper bound", 0xE007F, true},
		{"just below TAG block", 0xDFFFF, false},
		{"just above TAG block", 0xE0080, false},
		{"soft hyphen", 0x00AD, true},
		{"zero-width space", 0x200B, true},
		{"zero-width non-joiner", 0x200C, true},
		{"zero-width joiner", 0x200D, true},
		{"LTR mark", 0x200E, true},
		{"RTL mark", 0x200F, true},
		{"BOM / zwnbsp", 0xFEFF, true},
		{"word joiner", 0x2060, true},
		{"function application (invisible math)", 0x2061, true},
		{"invisible times", 0x2062, true},
		{"invisible separator", 0x2063, true},
		{"invisible plus", 0x2064, true},
		{"bidi ALM singleton", 0x061C, true},
		{"bidi embedding lower", 0x202A, true},
		{"bidi embedding mid", 0x202C, true},
		{"bidi embedding upper", 0x202E, true},
		{"just below bidi embedding", 0x2029, false},
		{"just above bidi embedding", 0x202F, false},
		{"bidi isolate lower", 0x2066, true},
		{"bidi isolate mid", 0x2067, true},
		{"bidi isolate upper", 0x2069, true},
		{"just below bidi isolate", 0x2065, false},
		{"just above bidi isolate", 0x206A, false},
		{"NUL (not stripped)", '\x00', false},
		{"nbsp (visible whitespace, must not be stripped)", 0x00A0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHiddenUnicode(tt.r); got != tt.want {
				t.Errorf("isHiddenUnicode(U+%04X) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

func TestSanitizeUnicode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain ascii", "hello world", "hello world"},
		{"preserves newlines and tabs", "line1\n\tline2", "line1\n\tline2"},
		{"strips zero-width space", "a\u200Bb", "ab"},
		{"strips zero-width joiner", "a\u200Db", "ab"},
		{"strips BOM", "\uFEFFhello", "hello"},
		{"strips soft hyphen", "he\u00ADllo", "hello"},
		{"strips word joiner", "a\u2060b", "ab"},
		{"strips LTR mark", "\u200Ehello", "hello"},
		{"strips RTL mark", "hello\u200F", "hello"},
		{"strips bidi embedding range", "a\u202Ab\u202Ec", "abc"},
		{"strips bidi isolate range", "a\u2066b\u2069c", "abc"},
		{"strips TAG characters", "visible\U000E0041\U000E0062hidden", "visiblehidden"},
		{"preserves nbsp (visible)", "a\u00A0b", "a\u00A0b"},
		{"preserves accented characters", "café", "café"},
		{"preserves emoji", "hi 😀", "hi 😀"},
		{"multiple hidden in a row", "a\u200B\u200C\u200D\u2060b", "ab"},
		{"only hidden characters → empty", "\u200B\u200C\u200D\u2060", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeUnicode(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeUnicode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeUnicode_is_idempotent(t *testing.T) {
	inputs := []string{
		"", "plain text", "a\u200Bb\u200Cc", "\uFEFFhello",
		"visible\U000E0041\U000E0062hidden", "café\u00AD résumé",
		"\u202Abidi\u202E", "\u200B\u200C\u200D\u2060",
	}
	for _, in := range inputs {
		once := SanitizeUnicode(in)
		twice := SanitizeUnicode(once)
		if once != twice {
			t.Errorf("SanitizeUnicode not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

func TestSanitizeOutput_composes_strip_ANSI_then_unicode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text passthrough", "hello world", "hello world"},
		{"ansi only", "\x1b[31mred\x1b[0m", "red"},
		{"hidden unicode only", "a\u200Bb\uFEFFc", "abc"},
		{"both ansi and hidden unicode", "\x1b[1;32mclean\u200Btext\x1b[0m\uFEFFafter", "cleantextafter"},
		{"hidden unicode inside ANSI payload is removed", "\x1b[31mred\u200Btext\x1b[0m", "redtext"},
		{"TAG characters embedded between ANSI runs", "\x1b[32mA\x1b[0m\U000E0041\x1b[31mB\x1b[0m", "AB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeOutput(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeOutput_is_idempotent(t *testing.T) {
	inputs := []string{
		"", "plain", "\x1b[31mred\x1b[0m", "a\u200Bb",
		"\x1b[1;32mgreen\u200Btext\x1b[0m\uFEFFtail",
		"visible\U000E0041\x1b[0m\u200Btext",
	}
	for _, in := range inputs {
		once := SanitizeOutput(in)
		twice := SanitizeOutput(once)
		if once != twice {
			t.Errorf("SanitizeOutput not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

func TestSanitizeOutput_strips_ansi_before_unicode(t *testing.T) {
	tricky := []string{
		"\x1b[31m\u200Bred\u200B\x1b[0m",
		"\x1b]0;title\u200B\x07rest",
		"before\x1b[1;32m\uFEFFbold\x1b[0mafter",
	}
	for _, in := range tricky {
		got := SanitizeOutput(in)
		want := SanitizeUnicode(StripANSI(in))
		if got != want {
			t.Errorf("SanitizeOutput(%q) = %q, want SanitizeUnicode(StripANSI(_)) = %q", in, got, want)
		}
	}
}
