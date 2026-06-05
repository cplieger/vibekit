package buffer

import (
	"encoding/json"
	"testing"
)

// FuzzRecoverPartialSnapshotRoundTrip targets the crash-recovery partial
// snapshot parser. Bug class: accepting corrupt/inconsistent snapshots
// that violate the invariant "recovered snapshot must have non-empty
// content or reasoning" — could cause empty messages injected into chat
// after crash recovery. Also checks marshal→recover round-trip fidelity.
func FuzzRecoverPartialSnapshotRoundTrip(f *testing.F) {
	f.Add(`{"message_id":"m1","content":"hello","ts":1}`)
	f.Add(`{"message_id":"m1","reasoning":"think","ts":1}`)
	f.Add(`{"message_id":"","content":"","ts":0}`)
	f.Add(`{}`)
	f.Add(`{"content":"x","blocks":[{"type":"text","text":"x"}]}`)
	f.Add(`not json`)
	f.Add(`{"message_id":"m1","content":"a","tool_calls":[{"id":"t1","title":"T","kind":"shell","status":"completed","ts":1}]}`)

	f.Fuzz(func(t *testing.T, data string) {
		snap, ok := RecoverPartialSnapshot([]byte(data))

		// Invariant 1: if recovery succeeds, content or reasoning must be non-empty.
		if ok {
			if snap.Content == "" && snap.Reasoning == "" {
				t.Fatalf("RecoverPartialSnapshot accepted snapshot with empty content AND reasoning: %q", data)
			}
		}

		// Invariant 2: if recovery succeeds, re-marshalling and re-recovering
		// must produce the same result (idempotent round-trip).
		if ok {
			marshalled, err := json.Marshal(snap)
			if err != nil {
				t.Fatalf("failed to re-marshal recovered snapshot: %v", err)
			}
			snap2, ok2 := RecoverPartialSnapshot(marshalled)
			if !ok2 {
				t.Fatalf("re-recovery failed for marshalled snapshot: %s", marshalled)
			}
			if snap.MessageID != snap2.MessageID || snap.Content != snap2.Content ||
				snap.Reasoning != snap2.Reasoning || snap.Ts != snap2.Ts {
				t.Fatalf("round-trip mismatch:\n  original: %+v\n  recovered: %+v", snap, snap2)
			}
		}

		// Invariant 3: invalid JSON must never be accepted.
		var probe json.RawMessage
		if json.Unmarshal([]byte(data), &probe) != nil && ok {
			t.Fatalf("RecoverPartialSnapshot accepted invalid JSON: %q", data)
		}
	})
}
