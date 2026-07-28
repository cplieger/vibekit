package composition

import (
	"path/filepath"
	"testing"
)

// TestCheckDirWritable verifies a writable directory passes the probe
// and a missing directory is rejected.
func TestCheckDirWritable(t *testing.T) {
	t.Run("writable dir returns nil", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(dir, "TEST_DIR"); err != nil {
			t.Errorf("checkDirWritable(writable dir) = %v, want nil", err)
		}
	})

	t.Run("missing dir returns error", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		if err := checkDirWritable(dir, "TEST_DIR"); err == nil {
			t.Error("checkDirWritable(missing dir) = nil, want error")
		}
	})
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
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig with no kiro-cli installed yet = %v, want nil (degraded, not fatal)", err)
	}
}
