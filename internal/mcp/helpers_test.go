package mcp

import (
	"bytes"
	"log/slog"
	"testing"
)

// captureSlog redirects slog.Default to an in-memory buffer at Debug
// level for the duration of the test, restoring the previous logger on
// cleanup. Used by tests whose only observable effect is a log line (an
// eviction count, a perms-tighten failure, a drain-on-error breadcrumb).
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
