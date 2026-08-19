package hub

// Pure-function tests for bridge_buffer.go. Pins extractStopReason
// branches that the existing suite only exercises via happy-path
// turn-end flows.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestExtractStopReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		resp *vibekit.RPCResponse
		want vibekit.StopReason
	}{
		{name: "nil response", resp: nil, want: ""},
		{name: "nil result", resp: &vibekit.RPCResponse{Result: nil}, want: ""},
		{name: "malformed JSON", resp: &vibekit.RPCResponse{Result: json.RawMessage(`not json`)}, want: ""},
		{name: "missing field", resp: &vibekit.RPCResponse{Result: json.RawMessage(`{"other":"value"}`)}, want: ""},
		{name: "end_turn", resp: &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, want: "end_turn"},
		{name: "cancelled", resp: &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}, want: "cancelled"},
		{name: "empty string", resp: &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":""}`)}, want: ""},
		{name: "max_turn_requests", resp: &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"max_turn_requests"}`)}, want: "max_turn_requests"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractStopReason(tc.resp); got != tc.want {
				t.Errorf("extractStopReason() = %q, want %q", got, tc.want)
			}
		})
	}
}
