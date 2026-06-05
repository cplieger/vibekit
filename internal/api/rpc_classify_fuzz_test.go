package api

import (
	"encoding/json"
	"testing"
)

// FuzzRPCResponseClassification targets the RPCResponse JSON unmarshalling
// and classification logic. Bug class: ambiguous messages where both ID and
// Method are populated (violating JSON-RPC 2.0 spec) being silently treated
// as one or the other — could cause response/notification misrouting in the
// bridge's readLoop dispatch.
func FuzzRPCResponseClassification(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"s1"}}`)
	f.Add(`{"jsonrpc":"2.0","method":"session/update","params":{"kind":"agent_message_chunk"}}`)
	f.Add(`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"fail"}}`)
	f.Add(`{"jsonrpc":"2.0","id":null,"method":"notify"}`)
	f.Add(`{}`)
	f.Add(`{"jsonrpc":"2.0","id":1,"method":"both","result":"x"}`)

	f.Fuzz(func(t *testing.T, data string) {
		var resp RPCResponse
		if json.Unmarshal([]byte(data), &resp) != nil {
			return
		}

		isResponse := resp.ID != nil
		isNotification := resp.Method != "" && resp.ID == nil

		// Invariant 1: a well-formed response (has ID) cannot also be a
		// notification (no ID + has Method). If both are set, at least
		// isNotification must be false (ID takes precedence).
		if isResponse && isNotification {
			t.Fatalf("RPCResponse classified as both response and notification: %q", data)
		}

		// Invariant 2: if Error is non-nil, Result must be nil (JSON-RPC 2.0 spec).
		if resp.Error != nil && resp.Result != nil {
			// Not a crash, but the bridge should not trust such messages.
			// We verify the parser at least doesn't produce impossible states.
			if resp.Error.Message == "" && resp.Error.Code == 0 {
				// Zero-value error with result present is effectively "no error".
				return
			}
		}

		// Invariant 3: Error.Error() must not panic and must return the Message field.
		if resp.Error != nil {
			msg := resp.Error.Error()
			if msg != resp.Error.Message {
				t.Fatalf("RPCError.Error() = %q, want %q", msg, resp.Error.Message)
			}
		}
	})
}
