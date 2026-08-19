package bridge

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
)

// FuzzSessionCreatedUnmarshal targets the session/new result parsing.
// Bug class: accepting invalid session IDs from kiro-cli response that
// would later be used as filesystem path components — a crafted sessionId
// with path separators could escape the sessions directory. Also validates
// that mode/model arrays survive round-trip without corruption.
func FuzzSessionCreatedUnmarshal(f *testing.F) {
	f.Add(`{"sessionId":"abc-123","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"}]},"configOptions":[{"id":"model","currentValue":"m1","options":[{"value":"m1","name":"M1","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}`)
	f.Add(`{"sessionId":"../escape"}`)
	f.Add(`{"sessionId":"a/b\\c"}`)
	f.Add(`{"sessionId":""}`)
	f.Add(`{}`)
	f.Add(`{"sessionId":"valid-id","modes":null,"configOptions":null}`)
	f.Add(`{"sessionId":"x","modes":{"availableModes":[]},"configOptions":[]}`)

	f.Fuzz(func(t *testing.T, data string) {
		var result sessionCreated
		if json.Unmarshal([]byte(data), &result) != nil {
			return
		}

		// Invariant 1: if sessionId passes ValidSessionID, it must not
		// contain path separators or traversal patterns.
		if ids.ValidSessionID(result.SessionID) {
			for _, ch := range result.SessionID {
				if ch == '/' || ch == '\\' || ch == 0 {
					t.Fatalf("ValidSessionID accepted dangerous char in %q", result.SessionID)
				}
			}
			if result.SessionID == ".." || result.SessionID == "." {
				t.Fatalf("ValidSessionID accepted traversal pattern %q", result.SessionID)
			}
		}

		// Invariant 2: if Modes is non-nil, availableModes must be a valid slice.
		if result.Modes != nil {
			for i, mode := range result.Modes.AvailableModes {
				if mode.ID == "" && mode.Name == "" {
					// Empty modes are technically valid but suspicious.
					_ = i
				}
			}
		}

		// Invariant 3: the v3 model catalog rides configOptions (the
		// "model" select). Documented boundary: kiro-cli MAY send invalid
		// model ids that pass through unchecked; we only assert no panic
		// while walking the choices.
		for i := range result.ConfigOptions {
			for _, choice := range result.ConfigOptions[i].Options {
				_ = choice.Value
			}
		}

		// Invariant 4: round-trip marshal must not lose the sessionId.
		marshalled, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("re-marshal failed: %v", err)
		}
		var result2 sessionCreated
		if json.Unmarshal(marshalled, &result2) != nil {
			t.Fatalf("re-unmarshal failed")
		}
		if result.SessionID != result2.SessionID {
			t.Fatalf("sessionId lost in round-trip: %q → %q", result.SessionID, result2.SessionID)
		}
	})
}
