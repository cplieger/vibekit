package vibekit

import (
	"encoding/json"
	"testing"
)

// FuzzClientCommandEnvelopeIntegrity targets the ClientCommand envelope
// deserialization. Bug class: type confusion where a fuzzed JSON payload
// unmarshals into a command with mismatched type/payload — could route a
// "delete_chat" payload through the "prompt" handler if the Type field
// is parsed incorrectly from crafted input.
func FuzzClientCommandEnvelopeIntegrity(f *testing.F) {
	f.Add(`{"type":"prompt","chat_id":"c1","payload":{"text":"hi","message_id":"m1"}}`)
	f.Add(`{"type":"delete_chat","chat_id":"c1"}`)
	f.Add(`{"type":"cancel","chat_id":"c1","request_id":"r1"}`)
	f.Add(`{"type":"","payload":null}`)
	f.Add(`{"type":"prompt","chat_id":"c1","payload":{"text":""}}`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, data string) {
		var cmd ClientCommand
		if json.Unmarshal([]byte(data), &cmd) != nil {
			return
		}

		// Invariant 1: Type must survive round-trip (not corrupted by payload parsing).
		reMarshalled, err := json.Marshal(cmd)
		if err != nil {
			t.Fatalf("re-marshal failed: %v", err)
		}
		var cmd2 ClientCommand
		if json.Unmarshal(reMarshalled, &cmd2) != nil {
			t.Fatalf("re-unmarshal failed for: %s", reMarshalled)
		}
		if cmd.Type != cmd2.Type {
			t.Fatalf("Type changed after round-trip: %q → %q", cmd.Type, cmd2.Type)
		}
		if cmd.ChatID != cmd2.ChatID {
			t.Fatalf("ChatID changed after round-trip: %q → %q", cmd.ChatID, cmd2.ChatID)
		}

		// Invariant 2: if Payload is not nil, it must be valid JSON.
		if cmd.Payload != nil {
			var probe json.RawMessage
			if json.Unmarshal(cmd.Payload, &probe) != nil {
				t.Fatalf("Payload is not valid JSON after unmarshal: %s", cmd.Payload)
			}
		}
	})
}
