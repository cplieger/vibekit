package translate

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzToAvailableCommands pins structural invariants of the wire→typed
// command conversion across arbitrary JSON shapes: exactly one output
// command per input entry, the reserved "name"/"description" keys are
// lifted to typed fields (never left in the Meta passthrough), and a
// string name/description is copied verbatim.
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
		out := toAvailableCommands(in)
		// One typed command per input entry (empty input yields nil / len 0).
		if len(out) != len(in) {
			t.Fatalf("toAvailableCommands: len(out)=%d, want len(in)=%d", len(out), len(in))
		}
		for i := range out {
			if _, bad := out[i].Meta[api.JSONKeyName]; bad {
				t.Errorf("out[%d].Meta retains reserved key %q (must be lifted to Name)", i, api.JSONKeyName)
			}
			if _, bad := out[i].Meta["description"]; bad {
				t.Errorf("out[%d].Meta retains reserved key %q (must be lifted to Description)", i, "description")
			}
			if name, ok := in[i][api.JSONKeyName].(string); ok && out[i].Name != name {
				t.Errorf("out[%d].Name = %q, want %q (string name must be lifted verbatim)", i, out[i].Name, name)
			}
			if desc, ok := in[i]["description"].(string); ok && out[i].Description != desc {
				t.Errorf("out[%d].Description = %q, want %q (string description must be lifted verbatim)", i, out[i].Description, desc)
			}
		}
	})
}
