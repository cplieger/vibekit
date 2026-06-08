package testsupport

import "context"

// NopMCPRecorder is a no-op implementation of translate.MCPRecorder for
// tests and benchmarks. The compile-time assertion lives in
// mcprecorder_test.go to avoid a circular import (translate imports
// testsupport in its test files).
type NopMCPRecorder struct{}

func (*NopMCPRecorder) RecordConnected(context.Context, string)           {}
func (*NopMCPRecorder) RecordOAuth(context.Context, string, string)       {}
func (*NopMCPRecorder) RecordInitFailure(context.Context, string, string) {}
func (*NopMCPRecorder) SignalReady()                                      {}
func (*NopMCPRecorder) SetKnownTools(context.Context, string, []string)   {}
