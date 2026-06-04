package api

import (
	"encoding/json"
	"testing"
)

func FuzzClientCommandUnmarshal(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"type":"prompt","chat_id":"abc","payload":{"text":"hi"}}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"type":"","request_id":"r1"}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{0x00, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		var cmd ClientCommand
		if json.Unmarshal(data, &cmd) != nil {
			return
		}
		encoded, err := json.Marshal(&cmd)
		if err != nil {
			t.Fatalf("Marshal failed after successful Unmarshal: %v", err)
		}
		var cmd2 ClientCommand
		if err := json.Unmarshal(encoded, &cmd2); err != nil {
			t.Fatalf("round-trip Unmarshal failed: %v", err)
		}
		if cmd2.Type != cmd.Type {
			t.Errorf("Type mismatch: %q vs %q", cmd.Type, cmd2.Type)
		}
		if cmd2.ChatID != cmd.ChatID {
			t.Errorf("ChatID mismatch: %q vs %q", cmd.ChatID, cmd2.ChatID)
		}
	})
}
