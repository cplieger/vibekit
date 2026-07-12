package translate

import (
	"encoding/json"
	"testing"
)

// FuzzACPWireDecode exercises json.Unmarshal into all ACP wire-format
// structs with arbitrary bytes, asserting no panics and that invalid
// inputs produce explicit errors rather than ambiguous nils.
func FuzzACPWireDecode(f *testing.F) {
	// Seed corpus: known-valid ACP wire messages.
	seeds := []string{
		// session-started envelope
		`{"sessionId":"sess-1","update":{"sessionUpdate":"session_started"}}`,
		// session-update with agent_message_chunk
		`{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}`,
		// request-permission
		`{"sessionId":"sess-1","update":{"sessionUpdate":"request_permission"}}`,
		// read-text-file tool call
		`{"toolCallId":"tc-1","title":"Read file","kind":"file_read","status":"running","rawInput":{},"locations":[{"path":"/tmp/x"}],"content":[{"type":"text","content":{"text":"data"}}]}`,
		// tool_call_update
		`{"toolCallId":"tc-2","status":"completed","locations":[],"content":[]}`,
		// plan
		`{"entries":[{"title":"Step 1","status":"pending"}]}`,
		// current_mode_update (KAS keys the mode on currentModeId)
		`{"currentModeId":"code"}`,
		// chunk with empty text (edge case)
		`{"content":{"type":"text","text":""}}`,
		// malformed but parseable JSON
		`{}`,
		`{"sessionId":"","update":null}`,
		`null`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Attempt decode into each wire struct. We only assert no panic
		// and that errors are explicit (non-nil) for truly invalid JSON.
		var chunk ACPChunkWire
		decodeAndCheck(t, data, &chunk)

		var tc ACPToolCallWire
		decodeAndCheck(t, data, &tc)

		var tu ACPToolCallUpdateWire
		decodeAndCheck(t, data, &tu)

		var plan ACPPlanWire
		decodeAndCheck(t, data, &plan)

		var mode ACPModeUpdateWire
		decodeAndCheck(t, data, &mode)

		var env ACPSessionUpdateEnvelope
		decodeAndCheck(t, data, &env)

		var base ACPSessionUpdateBase
		decodeAndCheck(t, data, &base)
	})
}

// decodeAndCheck unmarshals data into dst and verifies that invalid
// JSON produces an explicit error (not a nil error with zero-value
// result that could be confused with success).
func decodeAndCheck(t *testing.T, data []byte, dst any) {
	t.Helper()
	err := json.Unmarshal(data, dst)
	// If the input is not valid JSON at all, err must be non-nil.
	if !json.Valid(data) && err == nil {
		t.Errorf("invalid JSON produced nil error for %T", dst)
	}
}
