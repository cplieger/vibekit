package composition

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/atomicfile/v3"
)

// TestCheckDirWritable covers the probe's verdicts and the property the
// hand-rolled create-close-remove probe it replaced could not offer.
//
// The old probe discarded its close and remove errors, so its only testable
// contract was "writable dir passes, missing dir fails". Two cases below are new
// defences on the adopted primitive: a successful probe leaves the directory as
// it found it (the old probe's leftover, on the remove-refused path, was named
// nothing this app sweeps), and a file passed where a directory belongs is
// rejected before any probe runs. The teardown-failure branch (a directory that
// accepts a write and refuses the unlink, now a WARN rather than a silent pass)
// needs a filesystem that denies unlink to a UID that may write, which a unit
// test cannot arrange portably; the leak it can produce is covered instead by
// TestSweepStaleTemps_reclaims_a_leaked_writability_probe.
func TestCheckDirWritable(t *testing.T) {
	t.Run("writable dir returns nil", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(t.Context(), dir, "TEST_DIR"); err != nil {
			t.Errorf("checkDirWritable(writable dir) = %v, want nil", err)
		}
	})

	t.Run("successful probe leaves nothing behind", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(t.Context(), dir, "TEST_DIR"); err != nil {
			t.Fatalf("checkDirWritable(writable dir) = %v, want nil", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("probe left %v behind, want an empty directory", names)
		}
	})

	t.Run("missing dir returns error", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		if err := checkDirWritable(t.Context(), dir, "TEST_DIR"); err == nil {
			t.Error("checkDirWritable(missing dir) = nil, want error")
		}
	})

	t.Run("file where a dir belongs returns error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkDirWritable(t.Context(), path, "TEST_DIR"); err == nil {
			t.Error("checkDirWritable(regular file) = nil, want error")
		}
	})
}

// TestCheckDirWritable_ProbeNameIsSweepable pins the app-to-library agreement the
// adoption bought: whatever name the writability probe creates, this repo's own
// stale-temp sweep recognises it. The probe that used to run here invented
// ".vibekit-probe-*", which sweepStaleTemps walks straight past, so a directory
// that refused the unlink left a file nothing would ever reclaim. Asserting
// through the exported generator and predicate keeps the agreement checked rather
// than documented.
func TestCheckDirWritable_ProbeNameIsSweepable(t *testing.T) {
	name := atomicfile.TempName()
	if !atomicfile.IsPackageTemp(name) {
		t.Fatalf("atomicfile.TempName() = %q, which IsPackageTemp rejects", name)
	}
	if atomicfile.IsPackageTemp(".vibekit-probe-123") {
		t.Error("the retired app-invented probe name reads as sweepable; the sweep never reclaimed it")
	}
}

// TestValidateConfig_MissingCLIIsNotFatal pins the degraded-start posture
// (invariant 6): a kiro-cli the server cannot reach must not abort boot, and
// validation must not probe for one at all. On a first boot the install is still
// running when this executes, so any check here would fail on every healthy cold
// start. The failure surfaces through /api/health instead, which carries the
// install manager's own reason -- never through a fatal validation error that
// would erase the UI and the diagnostics page together.
func TestValidateConfig_MissingCLIIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{ConfigDir: dir, WorkDir: dir}
	if err := validateConfig(t.Context(), cfg); err != nil {
		t.Fatalf("validateConfig with no kiro-cli installed yet = %v, want nil (degraded, not fatal)", err)
	}
}
