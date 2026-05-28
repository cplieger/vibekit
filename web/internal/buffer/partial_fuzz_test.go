package buffer

import (
	"encoding/json"
	"testing"
)

func FuzzRecoverPartialSnapshot(f *testing.F) {
	// Seed: valid snapshot.
	f.Add([]byte(`{"message_id":"m1","content":"hello","ts":1000}`))
	// Reasoning-only snapshot.
	f.Add([]byte(`{"message_id":"m2","reasoning":"thinking","ts":1001}`))
	// Empty content and reasoning (should fail).
	f.Add([]byte(`{"message_id":"m3","content":"","reasoning":"","ts":1002}`))
	// Truncated JSON.
	f.Add([]byte(`{"message_id":"m4","content":"he`))
	// Empty input.
	f.Add([]byte{})
	// Huge content field.
	f.Add([]byte(`{"message_id":"m5","content":"` + string(make([]byte, 4096)) + `","ts":1003}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		snap, ok := RecoverPartialSnapshot(data)

		// Invariant 1: no panic (implicit).

		// Invariant 2: if JSON is invalid, recovery must fail.
		if !json.Valid(data) && ok {
			t.Error("recovery succeeded on invalid JSON")
		}

		// Invariant 3: if ok, Content or Reasoning must be non-empty.
		if ok && snap.Content == "" && snap.Reasoning == "" {
			t.Error("recovery succeeded but both Content and Reasoning are empty")
		}

		// Invariant 4: round-trip — marshal(snap) → RecoverPartialSnapshot → same snap.
		if ok {
			marshaled, err := json.Marshal(snap)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			snap2, ok2 := RecoverPartialSnapshot(marshaled)
			if !ok2 {
				t.Fatal("round-trip recovery failed")
			}
			if snap2.MessageID != snap.MessageID || snap2.Content != snap.Content || snap2.Reasoning != snap.Reasoning {
				t.Error("round-trip produced different snapshot")
			}
		}
	})
}
