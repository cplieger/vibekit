package bridge

import (
	"log/slog"
	"testing"
)

func FuzzClassifyStderrLevel(f *testing.F) {
	f.Add(`{"level":"INFO","msg":"started"}`)
	f.Add(`{"level":"ERROR","msg":"crash"}`)
	f.Add(`{"level":"warn","msg":"slow"}`)
	f.Add(`{"level":"DEBUG","msg":"trace"}`)
	f.Add("error: something failed")
	f.Add("WARNING: disk full")
	f.Add("info: server ready")
	f.Add("")
	f.Add("{malformed json")
	f.Add(`{"level":""}`)
	f.Add("FATAL: out of memory")
	f.Add("debug[subsystem]: msg")

	f.Fuzz(func(t *testing.T, line string) {
		lvl := classifyStderrLevel(line)
		// Level must be one of the standard slog levels or Info (default).
		switch lvl {
		case slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError:
		default:
			t.Errorf("unexpected slog level %v for input %q", lvl, line)
		}
	})
}
