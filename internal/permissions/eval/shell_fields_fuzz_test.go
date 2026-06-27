package eval

import (
	"strings"
	"testing"
)

func FuzzShellFields(f *testing.F) {
	f.Add("ls -la")
	f.Add(`git commit -m "hello world"`)
	f.Add(`echo 'single quoted'`)
	f.Add(`"unterminated`)
	f.Add("")
	f.Add("\t\t  ")
	f.Add("\x00\x01\x02")
	f.Add(strings.Repeat("a", 10000))

	f.Fuzz(func(t *testing.T, s string) {
		tokens := ShellFields(s)

		// No token should be empty.
		for i, tok := range tokens {
			if tok == "" {
				t.Errorf("ShellFields(%q): token[%d] is empty", s, i)
			}
		}

		// Every token byte must come from the input.
		joined := strings.Join(tokens, "")
		for _, b := range []byte(joined) {
			if !strings.ContainsRune(s, rune(b)) && !containsByte(s, b) {
				t.Errorf("ShellFields(%q): output contains byte %q not in input", s, b)
				break
			}
		}

		// Bounded output: tokens only ever hold input bytes (quote
		// delimiters and whitespace separators are dropped), so the
		// concatenated token length can never exceed the input length.
		// This pins the no-progress guard that stops a corrupted or
		// mutated scan from appending unboundedly (the gremlins-OOM
		// memory-bomb class observed at shell_fields.go).
		if len(joined) > len(s) {
			t.Errorf("ShellFields(%q): total token bytes %d exceed input length %d",
				s, len(joined), len(s))
		}
	})
}

func containsByte(s string, b byte) bool {
	for i := range len(s) {
		if s[i] == b {
			return true
		}
	}
	return false
}

func FuzzHasWriteOption(f *testing.F) {
	f.Add("ls -la")
	f.Add("curl -o output.txt http://example.com")
	f.Add("grep --output=file pattern")
	f.Add("")

	f.Fuzz(func(t *testing.T, s string) {
		// Must not panic.
		_ = HasWriteOption(s)
	})
}
