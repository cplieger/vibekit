package mcp

import (
	"encoding/json"
	"testing"
)

// TestUnmarshalJSON_PreservesTransport verifies the parse-boundary
// invariant: every accepted transport (stdio/http/sse) round-trips to
// its own Server.Transport value. "sse" is preserved as TransportSSE
// (no longer folded into "http") — KAS accepts a distinct {type:"sse"}
// mcpServers entry on the v3 wire, and UnmarshalJSON assigns
// ParseTransport's return value so the parsed record passes Validate()
// on first read.
func TestUnmarshalJSON_PreservesTransport(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want Transport
	}{
		{
			name: "sse preserved",
			in:   `{"id":"abc","name":"linear","transport":"sse","url":"https://mcp.example.com/sse","enabled":true}`,
			want: TransportSSE,
		},
		{
			name: "http preserved",
			in:   `{"id":"abc","name":"linear","transport":"http","url":"https://mcp.example.com","enabled":true}`,
			want: TransportHTTP,
		},
		{
			name: "stdio preserved",
			in:   `{"id":"abc","name":"local","transport":"stdio","command":"x","enabled":true}`,
			want: TransportStdio,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s Server
			if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if s.Transport != tc.want {
				t.Errorf("Transport = %q; want %q", s.Transport, tc.want)
			}
			// And the parsed record must pass Validate so it can be
			// served from a Read or written back to disk.
			if err := Validate(&s); err != nil {
				t.Errorf("Validate after unmarshal: %v", err)
			}
		})
	}
}

// TestUnmarshalJSON_RejectsUnknownTransport pins the parse-boundary
// behaviour: anything outside {stdio, http, sse} is rejected at
// unmarshal time, not at Validate.
func TestUnmarshalJSON_RejectsUnknownTransport(t *testing.T) {
	t.Parallel()
	var s Server
	err := json.Unmarshal([]byte(`{"id":"x","name":"y","transport":"websocket","enabled":true}`), &s)
	if err == nil {
		t.Fatal("expected error for unknown transport, got nil")
	}
}
