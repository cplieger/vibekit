package steering

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzParseHookDoc(f *testing.F) {
	f.Add([]byte(`{"version":"v1","hooks":[{"name":"Lint","trigger":"PostFileSave","action":{"type":"command","command":"npm test"}}]}`))
	f.Add([]byte(`{"version":"v1","hooks":[{"name":"Ctx","trigger":"SessionStart","action":{"type":"agent","prompt":"` + strings.Repeat("p", 100) + `"}}]}`))
	f.Add([]byte(`{"version":"v1","hooks":[{"name":"e\nvil","trigger":"Pre` + "`" + `ToolUse","action":{"type":"command","command":"` + strings.Repeat("\u00e9", 60) + `"}}]}`))
	f.Add([]byte(`{"event_type":"preToolUse","command":"legacy shape"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(""))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, h := range parseHookDoc(data) {
			// Action preview must be at most 80 bytes and valid UTF-8
			// (the truncation must never split a rune).
			if len(h.Command) > 80 {
				t.Errorf("parseHookDoc: command length %d > 80", len(h.Command))
			}
			if !utf8.ValidString(h.Command) {
				t.Errorf("parseHookDoc: command %q is not valid UTF-8", h.Command)
			}
			// Sanitisation invariant: no field may carry characters that
			// break out of the steering file's code spans or list lines.
			for _, field := range []string{h.Name, h.Trigger, h.Command} {
				if strings.ContainsAny(field, "\n\r\t`") {
					t.Errorf("parseHookDoc: field %q carries newline/tab/backtick", field)
				}
			}
		}
		// Never panics (implicit).
	})
}
