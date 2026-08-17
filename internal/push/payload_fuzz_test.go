package push

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/api"
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
		// Apply the same truncation the Service.Send path uses.
		gotTitle, gotBody, _ := fitToCap(title, body, api.PushSubject{})

		payload, err := json.Marshal(pushPayload{Title: gotTitle, Body: gotBody})
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}

		// The marshaled payload must fit the cap so push() delivers it
		// rather than dropping an oversize record — the property the old
		// raw-length truncation violated, because the JSON envelope and any
		// character escaping count toward the limit.
		if len(payload) > pushBodyCap {
			t.Errorf("marshaled payload = %d bytes, exceeds cap %d", len(payload), pushBodyCap)
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
