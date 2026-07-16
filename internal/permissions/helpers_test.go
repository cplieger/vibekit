package permissions

import (
	"log/slog"
	"strings"

	"github.com/cplieger/slogx/capture"
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

// hasLog reports whether any captured record at the given level contains
// msgSub in its message. It exists so tests can assert on log-only
// effects (operator-facing Warn/Info/Debug lines that have no return
// value) without a production seam.
func hasLog(rec *capture.Recorder, level slog.Level, msgSub string) bool {
	for _, r := range rec.Records() {
		if r.Level == level && strings.Contains(r.Message, msgSub) {
			return true
		}
	}
	return false
}
