package mcp

import (
	"encoding/json"
	"testing"
)

// FuzzServerUnmarshalJSON exercises the custom JSON unmarshaller with
// arbitrary payloads. Asserts no panic and that rejected transports
// produce an error.
func FuzzServerUnmarshalJSON(f *testing.F) {
	f.Add([]byte(`{"name":"s","transport":"stdio","command":"x"}`))
	f.Add([]byte(`{"name":"s","transport":"http","url":"https://x.io"}`))
	f.Add([]byte(`{"name":"s","transport":"sse","url":"https://x.io"}`))
	f.Add([]byte(`{"transport":"bad"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"not an object"`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var s Server
		err := json.Unmarshal(data, &s)
		if err != nil {
			return
		}
		// If transport is set and not one of the accepted values
		// (including legacy "sse"), UnmarshalJSON should have errored.
		if s.Transport != "" {
			if _, parseErr := ParseTransport(string(s.Transport)); parseErr != nil {
				t.Errorf("UnmarshalJSON accepted unknown transport %q", s.Transport)
			}
		}
	})
}
