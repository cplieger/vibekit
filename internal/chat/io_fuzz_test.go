package chat

import (
	"encoding/json"
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
