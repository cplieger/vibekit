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
		tok, i = scanToken(s, i)
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

// scanToken reads one whitespace-delimited token starting at index i,
// honoring single and double quotes. It returns the decoded token bytes
// and the index just past the token.
func scanToken(s string, i int) (tok []byte, next int) {
	for i < len(s) && s[i] != ' ' && s[i] != '\t' {
		switch s[i] {
		case '"':
			tok, i = scanQuoted(s, i+1, '"', tok)
		case '\'':
			tok, i = scanQuoted(s, i+1, '\'', tok)
		default:
			tok = append(tok, s[i])
			i++
		}
	}
	return tok, i
}

// scanQuoted appends the contents of a quoted span to tok, starting at
// index i (just past the opening quote) and stopping at the matching
// quote or end-of-string. It returns the updated token and the index
// just past the closing quote (or end-of-string for an unmatched quote).
func scanQuoted(s string, i int, quote byte, tok []byte) (out []byte, next int) {
	for i < len(s) && s[i] != quote {
		tok = append(tok, s[i])
		i++
	}
	if i < len(s) {
		i++
	}
	return tok, i
}
