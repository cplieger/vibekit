package composition

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/cplieger/atomicfile/v3"
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
// os.Exit(1). The one WARN below it is checkDirWritable's teardown case,
// which is a usable directory reporting a cleanup refusal — a failed
// validation is still fatal, not a degraded-mode situation.
//
// kiro-cli is deliberately NOT checked. It is the install manager's binary and
// the install is still running when this executes on a first boot, so probing
// for it would warn on every healthy cold start. The manager owns that
// reporting: it logs each attempt, and /api/health carries the verdict with its
// phase in the reason until a version is active.
func validateConfig(ctx context.Context, cfg *Config) error {
	var errs []error

	// configDir must exist and be writable (chat files, settings,
	// mcp.json, checkpoints all live here).
	if err := checkDirWritable(ctx, cfg.ConfigDir, "KIRO_CONFIG_DIR"); err != nil {
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

// checkDirWritable verifies dir exists, is a directory, and genuinely accepts a
// write.
//
// The write half is atomicfile.ProbeWritable: it walks the same
// create/write/sync/close/remove ladder a real atomic write walks and reports
// which stage failed. The hand-rolled create-close-remove probe it replaces
// discarded both teardown outcomes, so a directory that accepted a create and
// then refused the unlink passed this preflight silently; and its leftover
// carried an app-invented name, which sweepStaleTemps could never reclaim even
// though that sweep walks configDir recursively — it only reclaims atomicfile's
// own temp shape. The probe file now carries that shape by construction, so a
// leak is reclaimable rather than permanent.
//
// Policy on the two outcomes that used to be dropped: a directory that never
// accepted a real write is fatal, exactly as before, because nothing can persist
// a chat file there. A close or remove failure AFTER the bytes were written and
// flushed (ProbeResult.Writable) is a WARN and boot continues — the directory is
// usable, the leftover is swept, and refusing to start over a teardown quirk on
// the persistent volume would leave the operator no way INTO the container to
// repair it (invariant 6: a broken state must be able to heal itself). Either
// way the outcome is now reported instead of discarded.
func checkDirWritable(ctx context.Context, dir, envVar string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", envVar, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s=%q: not a directory", envVar, dir)
	}
	// Probe writability — Stat mode bits can lie (NFS, FUSE, Docker
	// volume permissions). A real write is the only reliable test.
	res, err := atomicfile.ProbeWritable(ctx, dir)
	if err != nil {
		// Not a verdict on the directory: the probe was never attempted, so
		// writability is unproven and this stays fatal.
		return fmt.Errorf("%s=%q: writability probe not attempted: %w", envVar, dir, err)
	}
	if !res.Writable() {
		return fmt.Errorf("%s=%q: not writable: %w", envVar, dir, res.Err)
	}
	if !res.OK() {
		slog.Warn("directory accepts writes but refused probe cleanup",
			"env", envVar, "dir", res.Dir, "stage", res.Stage.String(),
			"probe", res.Name, "leaked", res.Leaked, "error", res.Err)
	}
	return nil
}
