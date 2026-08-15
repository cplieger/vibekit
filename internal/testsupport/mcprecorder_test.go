package testsupport

import (
	"testing"

	"github.com/cplieger/vibekit/internal/translate"
)

// Compile-time assertion that NopMCPRecorder satisfies translate.MCPRecorder.
var _ translate.MCPRecorder = (*NopMCPRecorder)(nil)

func TestNopMCPRecorder_NoPanic(t *testing.T) {
	r := &NopMCPRecorder{}
	// Exercise all methods — verify no panics.
	r.RecordConnected(t.Context(), "server1", nil, nil, nil)
	r.RecordOAuth(t.Context(), "server1", "https://example.com/oauth")
	r.RecordInitFailure(t.Context(), "server1", "timeout")
	r.SignalReady()
}
