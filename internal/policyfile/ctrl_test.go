package policyfile

import (
	"errors"
	"testing"
	"unicode"
)

// TestSanitizeRule_RejectsC1Controls closes the gap the Unicode-17 currency
// sweep surfaced: isCtrl was `r < 0x20 || r == 0x7f`, which covers C0 and DEL and
// lets the whole C1 block (U+0080-U+009F) through, so 32 of unicode.Cc's 65
// members passed a gate whose own doc says it rejects control characters.
//
// The direction of the gap is why it matters. This is a REFUSE gate, so a missed
// rune fails OPEN and the token lands in permissions.yaml. U+0085 NEXT LINE is
// the worst of the 32: many renderers treat it as a line break, so a capability
// or match pattern carrying one displays in the permissions editor as two lines
// while the stored rule is one — and the rule governs every later tool call.
func TestSanitizeRule_RejectsC1Controls(t *testing.T) {
	for _, r := range []rune{0x0080, 0x0085, 0x009B, 0x009F} {
		t.Run(runeName(r), func(t *testing.T) {
			if _, err := SanitizeRule(&Rule{Capability: "fs_" + string(r) + "write", Effect: "ask"}); !errors.Is(err, ErrCapabilityShape) {
				t.Errorf("capability with U+%04X: err = %v, want ErrCapabilityShape", r, err)
			}
			if _, err := SanitizeRule(&Rule{
				Capability: "fs_write",
				Effect:     "ask",
				Match:      []string{"src/" + string(r) + "**"},
			}); !errors.Is(err, ErrPatternInvalid) {
				t.Errorf("pattern with U+%04X: err = %v, want ErrPatternInvalid", r, err)
			}
		})
	}
}

// TestIsCtrl_IsExactlyUnicodeCc states the predicate as an identity rather than a
// sample, which is what makes it a boundary test: every one of the 1,114,112 code
// points is checked, so a future narrowing back to an ASCII range fails here.
//
// It doubles as the version-stability claim. Cc is the fixed set U+0000-U+001F
// plus U+007F-U+009F and cannot gain members, so unlike a predicate built on a
// growing table this gate needs no per-release review.
func TestIsCtrl_IsExactlyUnicodeCc(t *testing.T) {
	population, disagreements := 0, 0
	for r := rune(0); r <= 0x10FFFF; r++ {
		cc := unicode.Is(unicode.Cc, r)
		if cc {
			population++
		}
		if isCtrl(r) != cc {
			disagreements++
		}
	}
	if population != 65 {
		t.Errorf("unicode.Cc population = %d, want 65 (C0 + DEL + C1)", population)
	}
	if disagreements != 0 {
		t.Errorf("isCtrl disagrees with unicode.Cc on %d runes, want 0", disagreements)
	}
}

func runeName(r rune) string {
	switch r {
	case 0x0080:
		return "U+0080 PAD (first C1)"
	case 0x0085:
		return "U+0085 NEXT LINE"
	case 0x009B:
		return "U+009B CSI (8-bit escape introducer)"
	default:
		return "U+009F APC (last C1)"
	}
}
