package chat

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// discardedMutate matches a call to the store's write path whose error is
// thrown away with an explicit blank. Mutate returns one value, so this is the
// only spelling that can drop it — `errcheck` and `ineffassign` are both
// enabled repo-wide, which makes an implicitly unchecked or assigned-never-read
// error impossible and leaves exactly this population.
var discardedMutate = regexp.MustCompile(`_\s*=\s*[^=\n]*\bMutate\(`)

// scanSkipDirs are directories with nothing to say about production callers.
var scanSkipDirs = map[string]bool{
	".git":         true,
	".kiro":        true,
	"node_modules": true,
	"static":       true,
	"static-src":   true,
	"testdata":     true,
}

// productionFileFloor guards against a broken walk passing vacuously. The tree
// holds hundreds of production Go files; a scan that finds a handful has
// stopped looking rather than found nothing.
const productionFileFloor = 100

// TestNoProductionSiteDiscardsMutateError is the migration guard for
// ErrTombstoned. Adding a sentinel does not change Mutate's signature, so no
// caller is FORCED to look at it: every `if err != nil` site starts propagating
// the refusal correctly, while a `_ = store.Mutate(...)` keeps the old reading
// and still compiles. The compiler cannot close that hole without a second
// return value at every call site, so this test closes it instead — it fails
// the moment a production file drops the error, which is the moment the
// sentinel becomes ignorable again.
//
// internal/testsupport is exempt on purpose: chatstore_contract.go is a test
// suite that happens not to be named _test.go (it is exported so several
// packages can run it against their own store), and its assertions are the
// reason it discards.
func TestNoProductionSiteDiscardsMutateError(t *testing.T) {
	root := repoRoot(t)
	exempt := filepath.Join(root, "internal", "testsupport")

	var scanned int
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if scanSkipDirs[d.Name()] || path == exempt {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for i, line := range strings.Split(string(data), "\n") {
			if discardedMutate.MatchString(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if scanned < productionFileFloor {
		t.Fatalf("scanned only %d production Go files under %s, want at least %d; the walk is broken, not the tree",
			scanned, root, productionFileFloor)
	}
	if len(offenders) > 0 {
		t.Errorf("production code discards Mutate's error, so chat.ErrTombstoned is silently ignored at:\n%s\n\nHandle the error, or match ErrTombstoned explicitly if the drop is intended.",
			strings.Join(offenders, "\n"))
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
