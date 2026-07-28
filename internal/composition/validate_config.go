package composition

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// validateConfig performs fail-fast checks on the configuration so
// misconfigurations surface as a clear startup error rather than a
// cryptic 500 deep in a handler minutes later.
//
// UX: if any check fails, Build() returns an error and main() exits
// with a non-zero code. The error message is human-readable and
// includes the env var name + actual value so the operator can fix
// it from `docker logs` without guessing which path was wrong.
//
// Log: the error propagates through Build() → main() → slog.Error +
// os.Exit(1). No slog.Warn here — a failed validation is fatal, not
// a degraded-mode situation.
//
// kiro-cli is deliberately NOT checked. It is the install manager's binary and
// the install is still running when this executes on a first boot, so probing
// for it would warn on every healthy cold start. The manager owns that
// reporting: it logs each attempt, and /api/health carries the verdict with its
// phase in the reason until a version is active.
func validateConfig(cfg *Config) error {
	var errs []error

	// configDir must exist and be writable (chat files, settings,
	// mcp.json, checkpoints all live here).
	if err := checkDirWritable(cfg.ConfigDir, "KIRO_CONFIG_DIR"); err != nil {
		errs = append(errs, err)
	}

	// workDir must exist (user's code repository; bind-mounted).
	if info, err := os.Stat(cfg.WorkDir); err != nil {
		errs = append(errs, fmt.Errorf("KIRO_WORK_DIR=%q: %w", cfg.WorkDir, err))
	} else if !info.IsDir() {
		errs = append(errs, fmt.Errorf("KIRO_WORK_DIR=%q: not a directory", cfg.WorkDir))
	}

	return errors.Join(errs...)
}

// checkDirWritable verifies dir exists, is a directory, and is
// writable by creating+removing a temp file.
func checkDirWritable(dir, envVar string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", envVar, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s=%q: not a directory", envVar, dir)
	}
	// Probe writability — Stat mode bits can lie (NFS, FUSE, Docker
	// volume permissions). A real write is the only reliable test.
	probe := filepath.Join(dir, ".vibekit-write-probe")
	f, err := os.CreateTemp(dir, ".vibekit-probe-*")
	if err != nil {
		return fmt.Errorf("%s=%q: not writable: %w", envVar, dir, err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	_ = os.Remove(probe) // clean up any stale probe from a prior crash
	return nil
}
