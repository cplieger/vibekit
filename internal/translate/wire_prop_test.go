package translate

import (
	"encoding/json"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// TestACPWire_RoundTrip_Rapid verifies that marshal→unmarshal preserves all
// fields for valid ACP wire types.
func TestACPWire_RoundTrip_Rapid(t *testing.T) {
	t.Run("ACPChunkWire", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			orig := ACPChunkWire{}
			orig.Content.Type = rapid.StringMatching(`[a-z]+`).Draw(rt, "type")
			orig.Content.Text = rapid.String().Draw(rt, "text")

			data, err := json.Marshal(orig)
			if err != nil {
				rt.Fatal(err)
			}
			var decoded ACPChunkWire
			if err := json.Unmarshal(data, &decoded); err != nil {
				rt.Fatal(err)
			}
			if !reflect.DeepEqual(orig, decoded) {
				rt.Fatalf("round-trip mismatch: %+v != %+v", orig, decoded)
			}
		})
	})

	t.Run("ACPToolCallWire", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			orig := ACPToolCallWire{
				ToolCallID: rapid.StringMatching(`[a-z0-9]{4,12}`).Draw(rt, "id"),
				Title:      rapid.String().Draw(rt, "title"),
				Kind:       "shell",
				Status:     "in_progress",
				RawInput:   json.RawMessage(`{}`),
			}

			data, err := json.Marshal(orig)
			if err != nil {
				rt.Fatal(err)
			}
			var decoded ACPToolCallWire
			if err := json.Unmarshal(data, &decoded); err != nil {
				rt.Fatal(err)
			}
			if orig.ToolCallID != decoded.ToolCallID || orig.Title != decoded.Title ||
				orig.Kind != decoded.Kind || orig.Status != decoded.Status {
				rt.Fatalf("round-trip mismatch: %+v != %+v", orig, decoded)
			}
		})
	})

	t.Run("ACPToolCallUpdateWire", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			orig := ACPToolCallUpdateWire{
				ToolCallID: rapid.StringMatching(`[a-z0-9]{4,12}`).Draw(rt, "id"),
				Status:     "completed",
				// Set explicitly, like ACPToolCallWire's RawInput above, because a
				// nil json.RawMessage marshals to `null` and decodes back as the
				// four bytes `null` rather than nil — DeepEqual would fail on a
				// field neither side got wrong.
				RawOutput: json.RawMessage(`{}`),
			}

			data, err := json.Marshal(orig)
			if err != nil {
				rt.Fatal(err)
			}
			var decoded ACPToolCallUpdateWire
			if err := json.Unmarshal(data, &decoded); err != nil {
				rt.Fatal(err)
			}
			if !reflect.DeepEqual(orig, decoded) {
				rt.Fatalf("round-trip mismatch: %+v != %+v", orig, decoded)
			}
		})
	})

	t.Run("ACPPlanWire", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			orig := ACPPlanWire{}
			data, err := json.Marshal(orig)
			if err != nil {
				rt.Fatal(err)
			}
			var decoded ACPPlanWire
			if err := json.Unmarshal(data, &decoded); err != nil {
				rt.Fatal(err)
			}
			// nil vs empty slice: normalize for comparison.
			if orig.Entries == nil && decoded.Entries == nil {
				return
			}
			if !reflect.DeepEqual(orig, decoded) {
				rt.Fatalf("round-trip mismatch: %+v != %+v", orig, decoded)
			}
		})
	})

	t.Run("ACPModeUpdateWire", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			orig := ACPModeUpdateWire{
				ModeID: rapid.StringMatching(`[a-z_]+`).Draw(rt, "modeId"),
			}

			data, err := json.Marshal(orig)
			if err != nil {
				rt.Fatal(err)
			}
			var decoded ACPModeUpdateWire
			if err := json.Unmarshal(data, &decoded); err != nil {
				rt.Fatal(err)
			}
			if !reflect.DeepEqual(orig, decoded) {
				rt.Fatalf("round-trip mismatch: %+v != %+v", orig, decoded)
			}
		})
	})
}
