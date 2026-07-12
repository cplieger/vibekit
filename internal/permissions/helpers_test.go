package permissions

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/permissions/eval"
)

// hasShellMetacharacter reports whether s contains any shell
// metacharacter from eval.ShellMetacharacters. Fuzz tests use this to
// decide whether a generated command should be auto-approved under
// policySafe (no metacharacters → safe) without parsing the command
// into (cmd, args). Production evaluation uses MetaGuard.CommandDisqualified
// directly; this is a test-only presence check.
func hasShellMetacharacter(s string) bool {
	return strings.ContainsAny(s, eval.ShellMetacharacters)
}

// logCapture is an in-memory slog.Handler that records every emitted
// record at every level. It exists so tests can assert on log-only
// effects (operator-facing Warn/Info/Debug lines that have no return
// value) without a production seam.
type logCapture struct {
	records []slog.Record
	mu      sync.Mutex
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCapture) WithGroup(string) slog.Handler      { return h }

// has reports whether any captured record at the given level contains
// msgSub in its message.
func (h *logCapture) has(level slog.Level, msgSub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == level && strings.Contains(r.Message, msgSub) {
			return true
		}
	}
	return false
}

// captureLogs installs an in-memory slog default logger and restores the
// previous default at test end. Tests using it must not run in parallel
// (they mutate the global default logger).
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	h := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}
