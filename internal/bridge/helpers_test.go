package bridge

import (
	"bytes"
	"log"
	"log/slog"
	"testing"
)

// captureLogs swaps the slog default to a buffer-backed debug handler for the
// duration of the test and restores it on cleanup. The default is process-wide,
// so a test using it must not run in parallel.
//
// The log package's writer and flags are restored too: slog.SetDefault also points
// log at the new handler, and it skips pointing it back when the restored handler
// is the stock one (which reaches log.Output), so every later line in the package
// would land in this buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
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
