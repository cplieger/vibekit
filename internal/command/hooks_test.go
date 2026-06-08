package command

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func FuzzValidateHookPayload(f *testing.F) {
	// Seed corpus: valid askAgent payload.
	f.Add([]byte(`{"name":"my-hook","event_type":"file:save","action_type":"askAgent","prompt":"do stuff"}`))
	// Valid runCommand payload.
	f.Add([]byte(`{"name":"build","event_type":"file:save","action_type":"runCommand","command":"make build"}`))
	// Oversized field.
	f.Add(make([]byte, 9000))
	// Empty name.
	f.Add([]byte(`{"name":"","event_type":"x","action_type":"askAgent","prompt":"p"}`))
	// Invalid action_type.
	f.Add([]byte(`{"name":"h","event_type":"x","action_type":"bad","prompt":"p"}`))
	// Name with path separators.
	f.Add([]byte(`{"name":"../evil","event_type":"x","action_type":"askAgent","prompt":"p"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cmd := &api.ClientCommand{Payload: json.RawMessage(data)}
		_, _, code, err := validateHookPayload(cmd)

		if err == nil && code != 0 {
			t.Errorf("err==nil but code=%d", code)
		}
		if err != nil && code == 0 {
			t.Errorf("err=%v but code=0", err)
		}
		if code == 0 && err == nil {
			var p hookCreatePayload
			if json.Unmarshal(data, &p) != nil {
				t.Error("validation passed but JSON unmarshal fails")
			}
		}
	})
}
