package steering

import (
	"testing"
)

func FuzzParseHookJSON(f *testing.F) {
	f.Add([]byte(`{"event_type":"preToolUse","command":"npm test"}`))
	f.Add([]byte(`{"event_type":"postToolUse","command":"` + string(make([]byte, 100)) + `"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(""))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		h := parseHookJSON(data)

		// Command must be at most 80 characters.
		if len(h.Command) > 80 {
			t.Errorf("parseHookJSON: command length %d > 80", len(h.Command))
		}

		// Never panics (implicit).
	})
}
