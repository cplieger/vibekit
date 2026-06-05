package chat

import (
	"encoding/json"
	"testing"
)

// FuzzCountJSONArrayElementsCorrectness targets the JSON array element
// counter that determines MessageCount on headers. Bug class: incorrect
// count when given malformed JSON, nested arrays, or deeply escaped
// content — a wrong count causes sidebar message-count drift vs actual
// messages. Verifies count matches stdlib's json.Unmarshal for valid arrays.
func FuzzCountJSONArrayElementsCorrectness(f *testing.F) {
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

	f.Fuzz(func(t *testing.T, data []byte) {
		count := countJSONArrayElements(json.RawMessage(data))

		// Invariant 1: count must be non-negative.
		if count < 0 {
			t.Fatalf("countJSONArrayElements returned negative: %d", count)
		}

		// Invariant 2: if data is a valid JSON array, count must equal
		// the actual number of top-level elements.
		var arr []json.RawMessage
		if json.Unmarshal(data, &arr) == nil {
			if count != len(arr) {
				t.Fatalf("countJSONArrayElements(%q) = %d, want %d (valid array)",
					data, count, len(arr))
			}
		}

		// Invariant 3: for non-array valid JSON, count must be 0.
		var probe any
		if err := json.Unmarshal(data, &probe); err == nil {
			if _, ok := probe.([]any); !ok {
				// Valid JSON but not an array (object, string, number, null, bool).
				if count != 0 {
					t.Fatalf("countJSONArrayElements(%q) = %d for non-array JSON, want 0",
						data, count)
				}
			}
		}
	})
}
