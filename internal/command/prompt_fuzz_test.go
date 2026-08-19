package command

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func FuzzValidatePromptPayload(f *testing.F) {
	// Valid prompt.
	f.Add([]byte(`{"text":"hello","message_id":"abc-123","model":"claude"}`))
	// Empty text.
	f.Add([]byte(`{"text":"","message_id":"abc-123","model":"claude"}`))
	// Missing message_id.
	f.Add([]byte(`{"text":"hi","message_id":"","model":"m"}`))
	// Invalid characters in message_id.
	f.Add([]byte(`{"text":"hi","message_id":"../evil","model":"m"}`))
	// Oversized text.
	f.Add(make([]byte, 600000))
	// Malformed JSON.
	f.Add([]byte(`{broken`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cmd := &vibekit.ClientCommand{Payload: json.RawMessage(data)}
		p, code, err := validatePromptPayload(cmd)

		// No panic (implicit).

		if err == nil && code != 0 {
			t.Errorf("err==nil but code=%d", code)
		}
		if err != nil && code == 0 {
			t.Errorf("err=%v but code=0", err)
		}

		// When validation passes, fields must satisfy constraints.
		if err == nil {
			if p.Text == "" {
				t.Error("validation passed but Text is empty")
			}
			if len(p.Text) > maxPromptBytes {
				t.Error("validation passed but Text exceeds cap")
			}
			if p.MessageID == "" {
				t.Error("validation passed but MessageID is empty")
			}
		}
	})
}
