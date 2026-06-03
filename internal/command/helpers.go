package command

import "unicode/utf8"

// TruncateRunes truncates s to at most n runes, returning a valid
// byte-prefix of s (preserving original bytes, not re-encoding).
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	pos := 0
	for i := 0; i < n && pos < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[pos:])
		pos += size
	}
	return s[:pos]
}
