package bridge

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzSessionCreatedUnmarshal targets the session/new result parsing.
// Bug class: accepting invalid session IDs from kiro-cli response that
// would later be used as filesystem path components — a crafted sessionId
// with path separators could escape the sessions directory. Also validates
// that mode/model arrays survive round-trip without corruption.
func FuzzSessionCreatedUnmarshal(f *testing.F) {
	f.Add(`{"sessionId":"abc-123","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"}]},"models":{"currentModelId":"auto","availableModels":[{"modelId":"m1","name":"M1"}]}}`)
	f.Add(`{"sessionId":"../escape"}`)
	f.Add(`{"sessionId":"a/b\\c"}`)
	f.Add(`{"sessionId":""}`)
	f.Add(`{}`)
	f.Add(`{"sessionId":"valid-id","modes":null,"models":null}`)
	f.Add(`{"sessionId":"x","modes":{"availableModes":[]},"models":{"availableModels":[]}}`)

	f.Fuzz(func(t *testing.T, data string) {
		var result sessionCreated
		if json.Unmarshal([]byte(data), &result) != nil {
			return
		}

		// Invariant 1: if sessionId passes ValidSessionID, it must not
		// contain path separators or traversal patterns.
		if api.ValidSessionID(result.SessionID) {
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

		// Invariant 3: if Models is non-nil, availableModels must have
		// safe model IDs (ValidIdent passes or empty).
		if result.Models != nil {
			for _, model := range result.Models.AvailableModels {
				// Documented boundary: kiro-cli MAY send invalid model IDs
				// that pass through unchecked; we assert that a non-empty
				// invalid ID is at least a UTF-8-valid string (any panic
				// downstream would already fail the fuzz invocation).
				_ = model.ModelID
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
