package mcp

import (
	"bytes"
	"log"
	"log/slog"
	"testing"
)

// captureSlog redirects slog.Default to an in-memory buffer at Debug
// level for the duration of the test, restoring the previous logger on
// cleanup. Used by tests whose only observable effect is a log line (an
// eviction count, a perms-tighten failure, a drain-on-error breadcrumb).
//
// The log package's writer and flags are restored too: slog.SetDefault also points
// log at the new handler, and it skips pointing it back when the restored handler
// is the stock one (which reaches log.Output), so every later line in the package
// would land in this buffer.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}
