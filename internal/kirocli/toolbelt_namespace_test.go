package kirocli

// Two engines write into the same persistent tools tree, so the only thing that
// keeps them from deleting each other's installs is the namespace split: the
// toolbelt engine owns bin/, opt/, npm/ and python/ under the tools dir -- its
// unit is opt/<tool>/<version>/ plus a force-replaced bin/<tool> symlink -- and
// this package owns kiro-cli-versions/<version>/ plus one convenience symlink.
//
// These tests plant a toolbelt footprint for a tool literally named `kiro-cli`.
// That is the worst case rather than a hypothetical: the engine's name validator
// accepts `kiro-cli`, its manifest is hand-editable and re-read per operation,
// and both apps mount its HTTP projection, so one `Add` reaches this state.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolbeltBody is the content of every file the fake toolbelt footprint owns, so
// a survivor check can tell "still there" from "replaced by our own file at the
// same path" -- which is what a publish over a colliding root actually does.
const toolbeltBody = "toolbelt-owned\n"

// toolbeltKiroCLI plants what the toolbelt engine puts on the volume for a
// manifest entry named `kiro-cli`: a version tree at opt/<name>/<version>/ and a
// bin/<name> SYMLINK into it for each binName (the engine's linkBin
// force-replaces those). No `.complete` sentinel, because the engine writes
// none -- which is exactly what made this tree a victim: to a manager rooted at
// opt/kiro-cli it reads as an INCOMPLETE install, and prunePartials deletes an
// incomplete install on every boot before selection.
//
// It returns every path that must survive untouched, plus the tree directory.
func toolbeltKiroCLI(t *testing.T, tools, version string, binNames ...string) (survivors []string, tree string) {
	t.Helper()
	tree = filepath.Join(tools, "opt", mainBinary, version)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", tree, err)
	}
	binDir := filepath.Join(tools, binSubdir)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", binDir, err)
	}
	// The engine's own PATH entry for an unrelated tool, so a sweep that walks
	// the shared bin dir instead of naming its targets is caught too.
	survivors = append(survivors, mustSymlink(t, filepath.Join(tools, "opt", "gopls", "1.0.0", "gopls"), filepath.Join(binDir, "gopls")))
	for _, name := range []string{mainBinary, mainBinary + "-chat", mainBinary + "-term"} {
		target := filepath.Join(tree, name)
		if err := os.WriteFile(target, []byte(toolbeltBody), 0o755); err != nil {
			t.Fatalf("WriteFile(%s): %v", target, err)
		}
		survivors = append(survivors, target)
	}
	for _, name := range binNames {
		survivors = append(survivors, mustSymlink(t, filepath.Join(tree, name), filepath.Join(binDir, name)))
	}
	return survivors, tree
}

// mustSymlink links newname -> target and returns newname.
func mustSymlink(t *testing.T, target, newname string) string {
	t.Helper()
	if err := os.Symlink(target, newname); err != nil {
		t.Fatalf("Symlink(%s -> %s): %v", newname, target, err)
	}
	return newname
}

// assertToolbeltIntact checks that every planted path is not merely PRESENT but
// unchanged: a symlink still points where the engine pointed it, and a regular
// file still holds the engine's bytes. Presence alone is not enough -- publish
// removes a colliding version directory and renames its own tree into the same
// place, so the paths reappear holding our files.
func assertToolbeltIntact(t *testing.T, tools string, survivors []string) {
	t.Helper()
	optTree := filepath.Join(tools, "opt") + string(filepath.Separator)
	for _, p := range survivors {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Errorf("%s is gone: this package reached into the toolbelt engine's namespace", p)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				t.Errorf("Readlink(%s): %v", p, err)
				continue
			}
			if !strings.HasPrefix(target, optTree) {
				t.Errorf("%s now points at %q, outside the engine's own opt tree: its symlink was republished under it", p, target)
			}
			continue
		}
		raw, err := os.ReadFile(p) // #nosec G304 -- a path this test planted.
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			continue
		}
		if string(raw) != toolbeltBody {
			t.Errorf("%s holds %q, not the engine's own bytes: it was removed and rewritten", p, string(raw))
		}
	}
}

// TestInstallRootIsOutsideTheToolbeltNamespace pins the structural half of the
// fix, which no behavioral test can pin on its own: the installation root is a
// single component directly under the tools dir, and it is none of the four
// directories the toolbelt engine creates and enumerates. Any tool name the
// engine accepts therefore resolves to a path that cannot contain, be contained
// by, or alias this package's tree.
func TestInstallRootIsOutsideTheToolbeltNamespace(t *testing.T) {
	if strings.ContainsRune(versionsSubdir, os.PathSeparator) {
		t.Fatalf("versionsSubdir = %q: a nested root can sit inside a tree the engine enumerates; keep it one component under the tools dir", versionsSubdir)
	}
	// The engine's binDir/optDir/npmDir/pythonDir, i.e. every directory it
	// creates under the tools dir. It never scans the tools dir itself.
	for _, owned := range []string{"bin", "opt", "npm", "python"} {
		if versionsSubdir == owned {
			t.Fatalf("versionsSubdir = %q collides with the toolbelt engine's own %q tree", versionsSubdir, owned)
		}
	}
}

// TestToolbeltKiroCLIFootprintSurvivesABoot is the whole-boot half of the
// collision: with a toolbelt-owned `kiro-cli` tool already on the volume, a full
// Ensure must neither READ nor DELETE any of it, and must install and activate
// its own version regardless.
//
// Under the old opt/kiro-cli root every one of those properties failed:
// prunePartials removed the sentinel-less tree, selection probed the foreign
// binary, and the pin was satisfied (or destroyed) by another owner's files.
func TestToolbeltKiroCLIFootprintSurvivesABoot(t *testing.T) {
	env := newFakeEnv(t)
	// Deliberately the PINNED version: if the roots still overlapped, this tree
	// would be the pin's own directory.
	//
	// bin/kiro-cli is deliberately NOT planted here. publishConvenienceLink
	// force-replaces that ONE path by design (it is the documented
	// `docker exec … kiro-cli` pointer, atomic rename over whatever is there),
	// so a boot legitimately owns it; the sidecar links below are never touched
	// by any code path. The sweep's treatment of a foreign bin/kiro-cli -- which
	// is where the every-boot deletion actually lived -- is pinned in
	// TestPurgeLegacySparesToolbeltSymlinks.
	survivors, tree := toolbeltKiroCLI(t, env.tools, pinnedVersion, mainBinary+"-chat", mainBinary+"-term")
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	assertToolbeltIntact(t, env.tools, survivors)
	// "Neither reads" is the other half. Every subprocess the manager runs is
	// recorded, so a probe or a settings call against the foreign tree shows up.
	for _, call := range env.called() {
		if strings.Contains(call, tree) {
			t.Errorf("the manager ran %q against the toolbelt-owned tree; it must never treat another owner's files as a version candidate", call)
		}
	}
	// Its own install is unaffected: it downloaded, published and activated the
	// pin under its own root.
	if got := env.fetchCount(); got != 1 {
		t.Errorf("fetches = %d, want 1: the foreign tree must not satisfy the pin", got)
	}
	wantDir := filepath.Join(env.tools, versionsSubdir, pinnedVersion)
	if got := m.PathEntry(); got != wantDir {
		t.Errorf("PathEntry() = %q, want %q", got, wantDir)
	}
	if got := m.CLIPath(); got != filepath.Join(wantDir, mainBinary) {
		t.Errorf("CLIPath() = %q, want the binary in its own version directory", got)
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true", reason)
	}
}

// TestPurgeLegacySparesToolbeltSymlinks is the sweep half of the collision. The
// prefix sweep this replaced listed $TOOLS/bin and deleted every kiro-cli*
// entry, unconditionally, on every boot -- so an engine-owned symlink was
// unlinked while the engine's state row still claimed it, silently, forever.
//
// The sweep now names its three targets and requires the shape the shell
// installer left (a regular file), so a symlink at the same path is refused.
func TestPurgeLegacySparesToolbeltSymlinks(t *testing.T) {
	env := newFakeEnv(t)
	survivors, _ := toolbeltKiroCLI(t, env.tools, "9.9.9", mainBinary, mainBinary+"-chat", mainBinary+"-term")
	// The genuine legacy residue is present at the same time, so the test also
	// proves the sweep still does its job rather than passing by doing nothing.
	// vibekit's shell installer left no journal or marker, so its own residue is
	// an orphan staging tree.
	stage := filepath.Join(env.tools, legacyStagePrefix+"abc123")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", stage, err)
	}
	m := env.manager()

	m.purgeLegacy()

	assertToolbeltIntact(t, env.tools, survivors)
	if exists(stage) {
		t.Error("the legacy sweep no longer removes the shell installer's own orphan staging trees")
	}
}

// TestPurgeLegacyLeavesTheInstallRootAndItsMarker pins that the two entries this
// package now keeps directly under the tools dir cannot be swept by the
// top-level orphan-stage pass that shares that directory with them.
func TestPurgeLegacyLeavesTheInstallRootAndItsMarker(t *testing.T) {
	env := newFakeEnv(t)
	dir := env.placeVersion(pinnedVersion)
	m := env.manager()

	m.purgeLegacy() // writes the marker itself
	m.purgeLegacy()

	if !exists(dir) {
		t.Errorf("the orphan-stage sweep removed the installation root %s", dir)
	}
	if !exists(filepath.Join(env.tools, legacyPurgeMarker)) {
		t.Error("the orphan-stage sweep removed its own completion marker")
	}
}

// TestMigrationSweepRunsOncePerVolume pins the once-only property. The layout
// the sweep deletes cannot come back -- no code writes it any more -- so after
// migration a pass over the co-owned bin dir can only find another owner's
// files. A SECOND boot (a fresh Manager on the same volume, which is what a
// container restart is) must therefore remove nothing at all.
func TestMigrationSweepRunsOncePerVolume(t *testing.T) {
	env := newFakeEnv(t)
	legacyFixture(t, env.tools)

	first := env.manager()
	first.purgeLegacyOnce()
	marker := filepath.Join(env.tools, legacyPurgeMarker)
	if !exists(marker) {
		t.Fatalf("the first boot did not record %s, so every later boot re-runs the sweep", legacyPurgeMarker)
	}

	// Plant the sweep's own targets again, in the shape it removes. Only a
	// second pass could take them.
	replanted := []string{
		binSubdir + "/" + mainBinary,
		binSubdir + "/" + mainBinary + "-chat",
	}
	for _, rel := range replanted {
		p := filepath.Join(env.tools, rel)
		if err := os.WriteFile(p, []byte("planted after migration\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	stage := legacyStagePrefix + "replanted"
	if err := os.MkdirAll(filepath.Join(env.tools, stage), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", stage, err)
	}
	replanted = append(replanted, stage)

	second := env.manager()
	second.purgeLegacyOnce()

	for _, rel := range replanted {
		if !exists(filepath.Join(env.tools, rel)) {
			t.Errorf("the second boot removed %s: the migration sweep still runs on every boot", rel)
		}
	}
}

// TestARefusedForeignEntryStillCompletesTheMigration pins the interaction
// between the two halves of this change: a foreign entry sitting at one of the
// swept paths is REFUSED, which is not a failure, so the migration is still
// recorded complete. Otherwise a volume with a toolbelt-owned bin/kiro-cli would
// re-run the sweep on every boot forever -- the exact "runs on every boot"
// property the marker exists to end.
func TestARefusedForeignEntryStillCompletesTheMigration(t *testing.T) {
	env := newFakeEnv(t)
	survivors, _ := toolbeltKiroCLI(t, env.tools, "9.9.9", mainBinary, mainBinary+"-chat")
	m := env.manager()

	m.purgeLegacy()

	if !exists(filepath.Join(env.tools, legacyPurgeMarker)) {
		t.Fatal("a refused foreign entry blocked the completion marker, so the sweep re-runs on every boot")
	}
	// And the second boot leaves the refused entries exactly where they are.
	env.manager().purgeLegacyOnce()
	assertToolbeltIntact(t, env.tools, survivors)
}
