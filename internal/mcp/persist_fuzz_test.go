package mcp

import (
	"encoding/json"
	"testing"
)

func FuzzServerUnmarshalJSON(f *testing.F) {
	f.Add([]byte(`{"name":"test","transport":"stdio","command":"bash"}`))
	f.Add([]byte(`{"name":"x","transport":"http","url":"https://a.com"}`))
	f.Add([]byte(`{"name":"x","transport":"sse","url":"https://a.com"}`))
	f.Add([]byte(`{"transport":"unknown"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{"name":123}`))
	f.Add([]byte(`{"transport":[]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var s Server
		err := s.UnmarshalJSON(data)
		if err != nil {
			return
		}
		// If unmarshal succeeded, transport must be empty or parseable.
		if s.Transport != "" {
			if _, perr := ParseTransport(string(s.Transport)); perr != nil {
				t.Fatalf("unparseable transport after successful unmarshal: %q", s.Transport)
			}
		}
		// Round-trip: Marshal must not panic.
		json.Marshal(&s)
	})
}
