package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzCheckDirWritable asserts checkDirWritable never panics and that
// every rejection names the env var -- the operator-facing contract
// (validateConfig's doc) that lets a bad KIRO_CONFIG_DIR be fixed from
// `docker logs` without guessing which variable was wrong. Inputs that
// would resolve outside the per-call scratch dir are skipped (harness
// safety): the probe creates and removes a temp file, so it must only ever
// touch this fresh TempDir, never a path a ".." segment collapsed onto.
func FuzzCheckDirWritable(f *testing.F) {
	f.Add("subdir")
	f.Add("")
	f.Add("a/b/c")

	const envVar = "TEST_DIR"
	f.Fuzz(func(t *testing.T, subpath string) {
		tmp := t.TempDir()
		target := filepath.Join(tmp, subpath)
		if target != tmp && !strings.HasPrefix(target, tmp+string(os.PathSeparator)) {
			t.Skip() // escapes the scratch dir
		}
		_ = os.MkdirAll(target, 0o755)

		err := checkDirWritable(target, envVar)
		if err == nil {
			return
		}
		msg := err.Error()
		if msg == "" {
			t.Fatalf("checkDirWritable(%q) returned an error with an empty message", target)
		}
		if !strings.Contains(msg, envVar) {
			t.Errorf("checkDirWritable(%q) error %q does not name env var %q", target, msg, envVar)
		}
	})
}
