package push

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// FuzzPayloadTruncation exercises the push payload truncation logic with
// arbitrary title/body strings to verify no panics, valid JSON output,
// and valid UTF-8.
func FuzzPayloadTruncation(f *testing.F) {
	f.Add("Title", "Short body")
	f.Add("", "")
	f.Add("T", string(make([]byte, pushBodyCap+100)))
	f.Add(string(make([]byte, pushBodyCap+100)), "body")
	f.Add("Title", "Hello 世界 🌍")

	f.Fuzz(func(t *testing.T, title, body string) {
		// Apply the same truncation logic as Service.Send.
		if total := len(title) + len(body); total > pushBodyCap {
			room := max(pushBodyCap-len(title)-3, 0)
			body = truncate(body, room)
		}

		payload, err := json.Marshal(pushPayload{Title: title, Body: body})
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}

		// Output must be valid JSON.
		if !json.Valid(payload) {
			t.Error("output is not valid JSON")
		}

		// Output must be valid UTF-8.
		if !utf8.Valid(payload) {
			t.Error("output is not valid UTF-8")
		}
	})
}
