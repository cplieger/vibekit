package composition

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// a degraded-mode situation. (The one degraded-mode check, kiro-cli
// presence, deliberately lives OUTSIDE this function: see
// warnIfCLIMissing.)
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

// warnIfCLIMissing is the degraded-start posture (P5, mirroring
// web-terminal-kiro): a missing kiro-cli must NOT abort boot. The
// entrypoint downloads the binary before exec-ing vibekit, but a
// first-boot network failure legitimately leaves it absent — killing
// the server then would erase the UI, diagnostics, and health signal
// together (the old `sleep infinity` dead end). The server starts,
// /api/health reports "kiro-cli unavailable" per probe, the client
// shows a degraded banner, and a container restart retries the
// install. Bridge spawns fail loudly in the meantime.
func warnIfCLIMissing(cfg *Config) {
	if err := checkExecutable(cfg.CLIPath, "KIRO_CLI_PATH"); err != nil {
		slog.Warn("starting degraded: kiro-cli unavailable — chats cannot start until it is installed; restart the container to retry the install",
			"error", err)
	}
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

// checkExecutable verifies path resolves to an existing executable.
// Two modes:
//   - Bare name (no slash): looked up via exec.LookPath, which walks
//     $PATH and tests the executable bit. Matches what os/exec does
//     when Bridge spawns the kiro-cli subprocess.
//   - Path with slash (absolute or relative): stat'd directly so a
//     misspelled path or stripped exec bit is caught.
func checkExecutable(path, envVar string) error {
	if !strings.ContainsRune(path, '/') {
		if _, err := exec.LookPath(path); err != nil {
			return fmt.Errorf("%s=%q: not found on $PATH: %w", envVar, path, err)
		}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", envVar, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s=%q: is a directory, expected executable file", envVar, path)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s=%q: file exists but is not executable (mode %s)", envVar, path, info.Mode())
	}
	return nil
}
