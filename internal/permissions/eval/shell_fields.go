package eval

// ShellFields splits a command string into tokens respecting single and
// double quotes. Unmatched quotes consume to end-of-string.
func ShellFields(s string) []string {
	var tokens []string
	i := 0
	for i < len(s) {
		prev := i
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
				i++
				for i < len(s) && s[i] != '"' {
					tok = append(tok, s[i])
					i++
				}
				if i < len(s) {
					i++
				}
			case '\'':
				i++
				for i < len(s) && s[i] != '\'' {
					tok = append(tok, s[i])
					i++
				}
				if i < len(s) {
					i++
				}
			default:
				tok = append(tok, s[i])
				i++
			}
		}
		if len(tok) > 0 {
			tokens = append(tokens, string(tok))
		}
		// Defensive: a valid iteration always advances i (whitespace skip or
		// token scan consumes at least one byte). If it didn't, the scan is
		// wedged — break instead of spinning and appending unboundedly. Keeps
		// this shell-permission tokenizer from being turned into a memory
		// bomb by a non-progressing input (or a corrupted scan).
		if i == prev {
			break
		}
	}
	return tokens
}
