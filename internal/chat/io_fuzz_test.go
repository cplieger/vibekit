package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// One token pass derives both header facts a raw messages array carries. A wrong count
// drifts the sidebar's message count; a wrong outcome paints the wrong activity dot on every
// chat tab after a reconnect. Both halves are ORACLE properties against a full stdlib
// unmarshal of the same bytes, which is what makes the streaming scan substitutable for the
// obvious implementation.
func FuzzScanMessagesArrayCorrectness(f *testing.F) {
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"not an array"`))
	f.Add([]byte(`[{"id":"m1"},{"id":"m2"}]`))
	f.Add([]byte(`[[1,2],[3,4]]`))
	f.Add([]byte(`[1`))          // truncated
	f.Add([]byte(`{}`))          // object not array
	f.Add([]byte(``))            // empty
	f.Add([]byte(`[null,null]`)) // null elements
	// The discriminating shapes: a carrier on an assistant row, one on an event row, rows after
	// the carrier, newest-wins, and a non-object element between two carriers.
	f.Add([]byte(`[{"role":"assistant","turn_outcome":"completed"}]`))
	f.Add([]byte(`[{"role":"event","event_kind":"turn_outcome","turn_outcome":"failed"}]`))
	f.Add([]byte(`[{"turn_outcome":"completed"},{"role":"assistant","plan":[]}]`))
	f.Add([]byte(`[{"turn_outcome":"completed"},{"turn_outcome":"failed"}]`))
	f.Add([]byte(`[{"turn_outcome":"completed"},[1,2],{"role":"user"}]`))
	f.Add([]byte(`[{"turn_outcome":""},{"turn_outcome":"refused"}]`))
	f.Add([]byte(`[{"turn_outcome":"completed","turn_outcome":"cancelled"}]`))
	f.Add([]byte(`[{"nested":{"turn_outcome":"failed"}}]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		count, last := scanMessagesArray(json.RawMessage(data))

		if count < 0 {
			t.Fatalf("scanMessagesArray returned negative count: %d", count)
		}

		var arr []json.RawMessage
		if json.Unmarshal(data, &arr) == nil {
			if count != len(arr) {
				t.Fatalf("scanMessagesArray(%q) count = %d, want %d (valid array)",
					data, count, len(arr))
			}
			if want := oracleLastOutcome(arr); last != want {
				t.Fatalf("scanMessagesArray(%q) outcome = %q, want %q",
					data, last, want)
			}
			return
		}

		// Valid JSON that is not an array reports nothing at all.
		var probe any
		if err := json.Unmarshal(data, &probe); err == nil {
			if _, ok := probe.([]any); !ok {
				if count != 0 {
					t.Fatalf("scanMessagesArray(%q) count = %d for non-array JSON, want 0",
						data, count)
				}
				if last != "" {
					t.Fatalf("scanMessagesArray(%q) outcome = %q for non-array JSON, want %q",
						data, last, "")
				}
			}
		}
	})
}

// FuzzDecodeChatHeaderMessageCount pins the streaming header decoder against
// encoding/json over the same bytes: the streaming message count must equal
// stdlib's element count, and what stdlib refuses must be refused here too.
func FuzzDecodeChatHeaderMessageCount(f *testing.F) {
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"not an array"`))
	f.Add([]byte(`[{"id":"m1"},{"id":"m2"}]`))
	f.Add([]byte(`[[1,2],[3,4]]`))
	f.Add([]byte(`[1`))          // truncated
	f.Add([]byte(`{}`))          // object not array
	f.Add([]byte(``))            // empty
	f.Add([]byte(`[null,null]`)) // null elements

	f.Fuzz(func(t *testing.T, messages []byte) {
		body := `{"id":"c1","name":"n","messages":` + string(messages) + `}`

		h, err := decodeChatHeader(strings.NewReader(body))

		var probe struct {
			Messages []json.RawMessage `json:"messages"`
		}
		stdlibOK := json.Unmarshal([]byte(body), &probe) == nil

		if err != nil {
			// Refusing what stdlib reads would drop the chat out of the sidebar
			// while the transcript still opens it.
			if stdlibOK {
				t.Fatalf("decodeChatHeader(%q) = error %v, want it read like encoding/json", messages, err)
			}
			return
		}
		if !stdlibOK {
			t.Fatalf("decodeChatHeader(%q) accepted a body encoding/json rejects", messages)
		}
		if h.MessageCount != len(probe.Messages) {
			t.Fatalf("decodeChatHeader(%q).MessageCount = %d, want %d",
				messages, h.MessageCount, len(probe.Messages))
		}
		if h.ID != "c1" || h.Name != "n" {
			t.Fatalf("decodeChatHeader(%q) header = {ID:%q Name:%q}, want {ID:\"c1\" Name:\"n\"}",
				messages, h.ID, h.Name)
		}
	})
}

// The obvious implementation the streaming scan must agree with. A non-object element
// contributes nothing, matching encoding/json's refusal to unmarshal a scalar into a struct.
func oracleLastOutcome(arr []json.RawMessage) vibekit.TurnOutcome {
	var last vibekit.TurnOutcome
	for _, elem := range arr {
		var probe outcomeProbe
		if err := json.Unmarshal(elem, &probe); err != nil {
			continue
		}
		if probe.TurnOutcome != "" {
			last = probe.TurnOutcome
		}
	}
	return last
}
