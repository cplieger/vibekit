package kirocli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// legacyFixture is the full inherited layout vibekit's shell installer could
// leave on a volume: the three promoted dispatchers in $TOOLS/bin and an orphan
// staging tree its EXIT trap never got to. Every entry is in the SHAPE the shell
// installer left it in (regular files, and a directory for the staging tree),
// which is what the sweep now requires before it removes anything.
//
// There is deliberately no journal, no `.prev` backup and no install/readiness
// marker in here: vibekit's promotion was a single commit point, never an
// in-place transaction, so those artifacts were web-terminal-kiro's alone. A
// future change that adds one has to extend both this fixture and purgeLegacy.
func legacyFixture(t *testing.T, tools string) []string {
	t.Helper()
	dirs := []string{
		filepath.Join(tools, "bin"),
		filepath.Join(tools, ".kiro-cli-stage.abc123"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}
	files := []string{
		"bin/kiro-cli",
		"bin/kiro-cli-chat",
		"bin/kiro-cli-term",
		".kiro-cli-stage.abc123/leftover",
	}
	for _, rel := range files {
		p := filepath.Join(tools, rel)
		if err := os.WriteFile(p, []byte("legacy\n"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	return files
}

// TestPurgeLegacyDeletesTheWholeLayout pins ruling 1: the legacy layout is
// resolved by DELETION, not by a decoder or a rollback path. Every artifact goes
// -- including a retired dispatcher name a fixed list cannot know about -- so
// no inherited journal, backup or marker can influence a later readiness or
// integrity decision.
func TestPurgeLegacyDeletesTheWholeLayout(t *testing.T) {
	env := newFakeEnv(t)
	files := legacyFixture(t, env.tools)
	m := env.manager()

	m.purgeLegacy()

	for _, rel := range files {
		if exists(filepath.Join(env.tools, rel)) {
			t.Errorf("%s survived the legacy purge", rel)
		}
	}
	if exists(filepath.Join(env.tools, ".kiro-cli-stage.abc123")) {
		t.Error("the orphan staging tree survived the legacy purge")
	}
	// $TOOLS/bin is co-owned by the toolbelt engine: the purge takes the
	// kiro-cli names, never the directory.
	if !exists(filepath.Join(env.tools, "bin")) {
		t.Error("the purge removed $TOOLS/bin, which the toolbelt engine co-owns")
	}
}

// TestPurgeLegacyLeavesAnUnknownDispatcherName pins the trade the scoped sweep
// makes deliberately. The `bin/kiro-cli*` prefix sweep it replaces could reclaim
// a retired dispatcher name no fixed list knows about — and could equally
// unlink a toolbelt-owned bin/kiro-cli-<anything> symlink while the engine's
// state row still claimed it. Only the three names the shell installer actually
// promoted are swept now; anything else is inert, dot-free residue costing disk,
// which is the correct price for never deleting another owner's live entry.
func TestPurgeLegacyLeavesAnUnknownDispatcherName(t *testing.T) {
	env := newFakeEnv(t)
	legacyFixture(t, env.tools)
	unknown := filepath.Join(env.tools, binSubdir, mainBinary+"-legacyname")
	if err := os.WriteFile(unknown, []byte("legacy\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%s): %v", unknown, err)
	}
	m := env.manager()

	m.purgeLegacy()

	if !exists(unknown) {
		t.Error("the sweep removed a bin/kiro-cli* name the shell installer never promoted; the prefix sweep is back, and with it the deletion of foreign symlinks in the co-owned bin dir")
	}
}

// TestPurgeLegacyIsIdempotentAndSurvivesInterruption pins the two properties a
// boot-time delete sequence needs. Every step is an unconditional RemoveAll of
// a fixed path, so a second run is a no-op and a run that starts from a
// half-purged volume finishes the job.
func TestPurgeLegacyIsIdempotentAndSurvivesInterruption(t *testing.T) {
	t.Run("idempotent", func(t *testing.T) {
		env := newFakeEnv(t)
		files := legacyFixture(t, env.tools)
		m := env.manager()

		m.purgeLegacy()
		m.purgeLegacy() // the second boot finds nothing and must not fail
		m.purgeLegacy()

		for _, rel := range files {
			if exists(filepath.Join(env.tools, rel)) {
				t.Errorf("%s survived repeated purges", rel)
			}
		}
	})

	t.Run("no legacy layout at all", func(t *testing.T) {
		env := newFakeEnv(t)
		m := env.manager()
		m.purgeLegacy() // a migrated volume: every step is a no-op
	})

	t.Run("resumes from a partially purged volume", func(t *testing.T) {
		env := newFakeEnv(t)
		files := legacyFixture(t, env.tools)
		// Simulate a kill mid-sequence: a prefix of the deletes already ran.
		for _, rel := range []string{"bin/kiro-cli", "bin/kiro-cli-chat"} {
			if err := os.RemoveAll(filepath.Join(env.tools, rel)); err != nil {
				t.Fatalf("RemoveAll(%s): %v", rel, err)
			}
		}
		m := env.manager()

		m.purgeLegacy()

		for _, rel := range files {
			if exists(filepath.Join(env.tools, rel)) {
				t.Errorf("%s survived the resumed purge", rel)
			}
		}
	})
}

// TestPurgeLegacyRunsOncePerProcess pins that the purge is latched: it deletes
// $TOOLS/bin/kiro-cli, which is exactly where the convenience symlink is
// published, so a retry or a rescan must not delete it again.
func TestPurgeLegacyRunsOncePerProcess(t *testing.T) {
	env := newFakeEnv(t)
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	link := filepath.Join(env.tools, binSubdir, mainBinary)
	if !exists(link) {
		t.Fatal("the convenience symlink was not published")
	}
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if !exists(link) {
		t.Error("a second Ensure purged the convenience symlink it had just published")
	}
}

// TestConvenienceSymlinkPointsAtTheActiveBinary pins finding 8's operator path:
// $TOOLS/bin/kiro-cli is republished as a symlink at the active version's
// binary, so `docker exec … kiro-cli --version` keeps resolving after the
// version-addressed move.
func TestConvenienceSymlinkPointsAtTheActiveBinary(t *testing.T) {
	env := newFakeEnv(t)
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	link := filepath.Join(env.tools, binSubdir, mainBinary)
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", link, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (mode %v)", link, fi.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != m.CLIPath() {
		t.Errorf("symlink target = %q, want the active binary %q", target, m.CLIPath())
	}
	// No temp link may be left behind by the atomic publish.
	if exists(filepath.Join(env.tools, binSubdir, "."+mainBinary+".newlink")) {
		t.Error("the staged temp link survived publication")
	}
}

// TestConvenienceSymlinkIsNeverConsultedForReadiness pins the other half of
// finding 8: the symlink is CONVENIENCE ONLY. A failed publish, a dangling
// link, and a link replaced by a plain file must all leave readiness and
// CLIPath untouched, because the manager never reads it.
func TestConvenienceSymlinkIsNeverConsultedForReadiness(t *testing.T) {
	t.Run("a failed publish does not withhold readiness", func(t *testing.T) {
		env := newFakeEnv(t)
		link := filepath.Join(env.tools, binSubdir, mainBinary)
		env.onRename = func(_, newpath string) error {
			if newpath == link {
				return errors.New("injected symlink publish failure")
			}
			return nil
		}
		m := env.manager()

		if err := m.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure error = %v, want nil: a convenience symlink failure must not fail the install", err)
		}
		if ready, reason := m.Ready(); !ready {
			t.Errorf("Ready() = false (%s), want true despite the symlink failure", reason)
		}
		if exists(link) {
			t.Error("the symlink exists although its publish rename failed")
		}
	})

	t.Run("a sabotaged link does not affect a rescan", func(t *testing.T) {
		env := newFakeEnv(t)
		m := env.manager()
		if err := m.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		want := m.CLIPath()
		link := filepath.Join(env.tools, binSubdir, mainBinary)
		if err := os.Remove(link); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := os.Symlink(filepath.Join(env.tools, "nowhere"), link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		ok, err := m.Rescan(context.Background())
		if !ok || err != nil {
			t.Fatalf("Rescan = (%v, %v), want (true, nil)", ok, err)
		}
		if ready, reason := m.Ready(); !ready {
			t.Errorf("Ready() = false (%s), want true: the symlink is not an integrity input", reason)
		}
		if got := m.CLIPath(); got != want {
			t.Errorf("CLIPath() = %q, want the version-directory path %q", got, want)
		}
	})
}
