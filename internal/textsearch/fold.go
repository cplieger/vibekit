package textsearch

import (
	"strings"
	"unicode"
)

// Fold lowercases for a case-insensitive scan with Unicode's simple, per-rune
// mapping (no Final_Sigma, no locale). Rune-count-preserving by construction:
// strings.Map with a rune-to-rune function, an invalid byte becoming one
// U+FFFD. So a rune index into Fold(s) is a rune index into s. Byte offsets
// are NOT preserved; Occurrences reports the original's.
func Fold(s string) string {
	return strings.Map(unicode.ToLower, s)
}
