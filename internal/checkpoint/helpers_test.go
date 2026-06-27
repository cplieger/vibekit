package checkpoint

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// logRecorder is a slog.Handler that records every record's message so
// a test can assert that a particular Warn/Error/Debug line was or was
// not emitted. Enabled at every level so Debug lines (e.g. syncDir) are
// captured too.
type logRecorder struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (h logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h logRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	*h.msgs = append(*h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}

func (h logRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h logRecorder) WithGroup(string) slog.Handler      { return h }

// captureLogs installs a capturing handler as the slog default for the
// duration of the test and returns a predicate reporting whether any
// captured message contains substr. The previous default is restored on
// cleanup. Tests using it must not call t.Parallel — slog's default
// handler is process-global, and no test in this package runs parallel.
func captureLogs(t *testing.T) func(substr string) bool {
	t.Helper()
	var mu sync.Mutex
	var msgs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(logRecorder{mu: &mu, msgs: &msgs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func(substr string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range msgs {
			if strings.Contains(m, substr) {
				return true
			}
		}
		return false
	}
}
