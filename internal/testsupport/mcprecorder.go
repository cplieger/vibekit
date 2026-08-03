package testsupport

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// NopMCPRecorder is a no-op implementation of translate.MCPRecorder for
// tests and benchmarks. The compile-time assertion lives in
// mcprecorder_test.go to avoid a circular import (translate imports
// testsupport in its test files).
type NopMCPRecorder struct{}

// RecordConnected is a no-op; implements translate.MCPRecorder.
func (*NopMCPRecorder) RecordConnected(context.Context, string, []string, []api.MCPPromptInfo, []api.MCPResourceInfo) {
}

// RecordOAuth is a no-op; implements translate.MCPRecorder.
func (*NopMCPRecorder) RecordOAuth(context.Context, string, string) {}

// RecordInitFailure is a no-op; implements translate.MCPRecorder.
func (*NopMCPRecorder) RecordInitFailure(context.Context, string, string) {}

// SignalReady is a no-op; implements translate.MCPRecorder.
func (*NopMCPRecorder) SignalReady() {}
