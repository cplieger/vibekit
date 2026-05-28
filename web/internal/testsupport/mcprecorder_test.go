package testsupport

import (
	"context"
	"testing"

	"vibekit/internal/translate"
)

// Compile-time assertion that NopMCPRecorder satisfies translate.MCPRecorder.
var _ translate.MCPRecorder = (*NopMCPRecorder)(nil)

func TestNopMCPRecorder_NoPanic(t *testing.T) {
	r := &NopMCPRecorder{}
	// Exercise all methods — verify no panics.
	r.RecordConnected(context.Background(), "server1")
	r.RecordOAuth(context.Background(), "server1", "https://example.com/oauth")
	r.RecordInitFailure(context.Background(), "server1", "timeout")
	r.SignalReady()
	r.SetKnownTools(context.Background(), "server1", []string{"tool1", "tool2"})
	r.SetKnownTools(context.Background(), "server1", nil)
}
