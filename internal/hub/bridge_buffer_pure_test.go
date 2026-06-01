package hub

// Pure-function tests for bridge_buffer.go. Pins extractStopReason
// branches that the existing suite only exercises via happy-path
// turn-end flows.

import (
	"encoding/json"
	"testing"

	"vibekit/internal/api"
)

func TestExtractStopReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		resp *api.RPCResponse
		want api.StopReason
	}{
		{name: "nil response", resp: nil, want: ""},
		{name: "nil result", resp: &api.RPCResponse{Result: nil}, want: ""},
		{name: "malformed JSON", resp: &api.RPCResponse{Result: json.RawMessage(`not json`)}, want: ""},
		{name: "missing field", resp: &api.RPCResponse{Result: json.RawMessage(`{"other":"value"}`)}, want: ""},
		{name: "end_turn", resp: &api.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, want: "end_turn"},
		{name: "cancelled", resp: &api.RPCResponse{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}, want: "cancelled"},
		{name: "empty string", resp: &api.RPCResponse{Result: json.RawMessage(`{"stopReason":""}`)}, want: ""},
		{name: "max_turn_requests", resp: &api.RPCResponse{Result: json.RawMessage(`{"stopReason":"max_turn_requests"}`)}, want: "max_turn_requests"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractStopReason(tc.resp); got != tc.want {
				t.Errorf("extractStopReason() = %q, want %q", got, tc.want)
			}
		})
	}
}
