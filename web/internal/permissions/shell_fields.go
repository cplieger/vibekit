package permissions

// shellFields splits a command string into tokens respecting single and
// double quotes. Unmatched quotes consume to end-of-string (same as
// POSIX shell behavior for unterminated quotes in non-interactive mode).
// Backslash escaping inside double quotes is NOT supported — this is a
// simplified tokenizer for permission evaluation, not a full shell parser.
//
// Examples:
//
//	shellFields(`grep "hello world" file.txt`) → ["grep", "hello world", "file.txt"]
//	shellFields(`echo 'it'\''s fine'`)         → ["echo", "it's fine"]  (no — this is simplified)
//	shellFields(`cmd -o`)                      → ["cmd", "-o"]
//
// The quotes are stripped from the output tokens. Empty input returns nil.
func shellFields(s string) []string {
	var tokens []string
	i := 0
	for i < len(s) {
		// Skip whitespace between tokens.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var tok []byte
		for i < len(s) && s[i] != ' ' && s[i] != '\t' {
			switch s[i] {
			case '"':
				i++ // skip opening quote
				for i < len(s) && s[i] != '"' {
					tok = append(tok, s[i])
					i++
				}
				if i < len(s) {
					i++ // skip closing quote
				}
			case '\'':
				i++ // skip opening quote
				for i < len(s) && s[i] != '\'' {
					tok = append(tok, s[i])
					i++
				}
				if i < len(s) {
					i++ // skip closing quote
				}
			default:
				tok = append(tok, s[i])
				i++
			}
		}
		if len(tok) > 0 {
			tokens = append(tokens, string(tok))
		}
	}
	return tokens
}
