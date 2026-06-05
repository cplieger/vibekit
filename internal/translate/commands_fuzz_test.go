package translate

import (
	"encoding/json"
	"testing"
)

func FuzzToAvailableCommands(f *testing.F) {
	f.Add([]byte(`[{"name":"help","description":"Show help"}]`))
	f.Add([]byte(`[{"name":123}]`))
	f.Add([]byte(`[{}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[{"name":"x","extra":{"nested":true}}]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var in []map[string]any
		if json.Unmarshal(data, &in) != nil {
			return
		}
		// Must not panic for any valid JSON shape.
		_ = toAvailableCommands(in)
	})
}
