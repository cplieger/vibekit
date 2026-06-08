// Test helpers for permissions_shell fuzz tests. Lives in a _test.go
// file so the compiler only builds it during `go test`, keeping
// deadcode happy (production evaluation uses metaGuard.Command and
// metaGuard.Arg directly; hasShellMetacharacter is only a fuzz-test
// convenience for checking presence without splitting a command).
//
// If production code ever needs this helper, move it back to
// permissions_shell.go. For now, the only call site is
// command_rules_fuzz_test.go.

package permissions

import (
	"strings"

	"github.com/cplieger/vibekit/internal/permissions/eval"
)

// hasShellMetacharacter reports whether s contains any shell
// metacharacter from eval.ShellMetacharacters. Fuzz tests use this to
// decide whether a generated command should be auto-approved under
// policySafe (no metacharacters → safe) without parsing the command
// into (cmd, args).
func hasShellMetacharacter(s string) bool {
	return strings.ContainsAny(s, eval.ShellMetacharacters)
}
