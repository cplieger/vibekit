package chat

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// FuzzScanMessagesArrayCorrectness targets the single token pass that derives
// BOTH header facts a chat file's raw messages array carries: MessageCount and
// LastTurnOutcome. Bug class: a wrong count causes sidebar message-count drift
// vs actual messages, and a wrong outcome paints the wrong activity dot on
// every chat tab after a reconnect.
//
// The count half is verified against stdlib json.Unmarshal. The outcome half is
// an ORACLE property: the scan's answer must equal what a full stdlib unmarshal
// of the same bytes reports as the last non-empty turn_outcome, which is what
// makes the streaming implementation substitutable for the obvious one.
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
	// The outcome-bearing shapes: the carrier on an assistant row, the carrier
	// on an event row (a turn that emitted nothing), rows after the carrier
	// that must not hide it, the newest-wins ordering, and a non-object element
	// sitting between two carriers so a desynchronised walk is visible.
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

		// Invariant 1: count must be non-negative.
		if count < 0 {
			t.Fatalf("scanMessagesArray returned negative count: %d", count)
		}

		// Invariant 2: if data is a valid JSON array, count must equal the
		// actual number of top-level elements, and the outcome must equal what
		// a full stdlib unmarshal reports.
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

		// Invariant 3: for non-array valid JSON, count must be 0 and there is
		// no outcome to report.
		var probe any
		if err := json.Unmarshal(data, &probe); err == nil {
			if _, ok := probe.([]any); !ok {
				// Valid JSON but not an array (object, string, number, null, bool).
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

// oracleLastOutcome is the obvious implementation the streaming scan must agree
// with: unmarshal every element in full and take the last non-empty
// turn_outcome. An element that is not an object contributes nothing, matching
// encoding/json's own refusal to unmarshal a scalar or an array into a struct.
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
