package composition

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// staleTemp writes an atomicfile-shaped temp aged past the sweep cutoff.
func staleTemp(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(full, old, old); err != nil {
		t.Fatal(err)
	}
	return full
}

// TestSweepStaleTemps_reaches_every_config_subdir pins the reason configDir is swept
// recursively: the previous shape enumerated configDir, chats and chats/<archive> by
// hand, so any new location that writes atomically kept its orphans forever. The
// unlisted-subdir case is the regression this guards.
func TestSweepStaleTemps_reaches_every_config_subdir(t *testing.T) {
	t.Parallel()
	configDir, workDir := t.TempDir(), t.TempDir()

	orphans := []string{
		staleTemp(t, configDir, ".atomicfile-1111111111.tmp"),
		staleTemp(t, filepath.Join(configDir, "chats"), ".atomicfile-2222222222.tmp"),
		staleTemp(t, filepath.Join(configDir, "chats", "archive"), ".atomicfile-3333333333.tmp"),
		// A location no hand-maintained list mentions.
		staleTemp(t, filepath.Join(configDir, "checkpoints", "blobs"), ".atomicfile-4444444444.tmp"),
		staleTemp(t, workDir, ".atomicfile-5555555555.tmp"),
	}

	// Real state files must survive regardless of age or depth.
	keep := filepath.Join(configDir, "chats", "c-abc.json")
	if err := os.WriteFile(keep, []byte(`{"id":"c-abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(keep, old, old); err != nil {
		t.Fatal(err)
	}

	sweepStaleTemps(configDir, workDir)

	for _, orphan := range orphans {
		if _, err := os.Stat(orphan); err == nil {
			t.Errorf("orphan survived the sweep: %s", orphan)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a caller-owned chat file was removed: %v", err)
	}
}

// TestSweepStaleTemps_workDir_is_swept_flat pins the other half of the option's
// argument: workDir is the user's working tree, so descending it on every boot
// would walk an arbitrarily large checkout to find temps that only ever land at
// its top level. An orphan in a workDir SUBDIRECTORY is therefore left alone,
// and the two sweeps must not be "aligned".
func TestSweepStaleTemps_workDir_is_swept_flat(t *testing.T) {
	t.Parallel()
	configDir, workDir := t.TempDir(), t.TempDir()

	top := staleTemp(t, workDir, ".atomicfile-7777777777.tmp")
	nested := staleTemp(t, filepath.Join(workDir, "vendor", "deep"), ".atomicfile-8888888888.tmp")

	sweepStaleTemps(configDir, workDir)

	if _, err := os.Stat(top); err == nil {
		t.Errorf("an orphan at the top of workDir survived the sweep: %s", top)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("the sweep descended into the user's working tree: %v", err)
	}
}

// TestSweepStaleTemps_spares_a_fresh_temp pins that a temp from a write still in
// flight is not reaped out from under it.
func TestSweepStaleTemps_spares_a_fresh_temp(t *testing.T) {
	t.Parallel()
	configDir, workDir := t.TempDir(), t.TempDir()
	fresh := filepath.Join(configDir, "chats", ".atomicfile-6666666666.tmp")
	if err := os.MkdirAll(filepath.Dir(fresh), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("in flight"), 0o600); err != nil {
		t.Fatal(err)
	}

	sweepStaleTemps(configDir, workDir)

	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a temp younger than the cutoff was removed: %v", err)
	}
}

// TestSweepStaleTemps_missing_dirs_are_not_fatal pins that a first run before either
// directory exists is a no-op rather than a startup failure: sweepStaleTemps runs
// during composition, so a panic or hard error here would refuse to boot.
func TestSweepStaleTemps_missing_dirs_are_not_fatal(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	sweepStaleTemps(filepath.Join(base, "nope"), filepath.Join(base, "also-nope"))
}

// TestSweepStaleTemps_reclaims_a_leaked_writability_probe closes the loop between
// the startup writability probe (checkDirWritable) and this sweep. A directory
// that accepts a write and refuses the unlink leaves the probe file behind;
// because that file now carries atomicfile's own temp shape, the recursive
// configDir sweep reclaims it on the next boot instead of leaving it forever, as
// it did while the probe invented its own ".vibekit-probe-*" name.
//
// The name comes from the library's exported generator rather than a literal, so
// this asserts the agreement itself and cannot drift from the shape the probe
// actually creates.
func TestSweepStaleTemps_reclaims_a_leaked_writability_probe(t *testing.T) {
	t.Parallel()
	configDir, workDir := t.TempDir(), t.TempDir()
	leaked := staleTemp(t, configDir, atomicfile.TempName())
	strayShape := staleTemp(t, configDir, ".vibekit-probe-4242")

	sweepStaleTemps(configDir, workDir)

	if _, err := os.Stat(leaked); err == nil {
		t.Errorf("a leaked writability probe survived the sweep: %s", leaked)
	}
	if _, err := os.Stat(strayShape); err != nil {
		t.Errorf("the sweep removed a non-atomicfile name it must never touch: %v", err)
	}
}
