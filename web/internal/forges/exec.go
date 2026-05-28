// CLI subprocess runner with structured output capture.
//
// Each forge backend (gh/glab/tea) is invoked through this helper.
// Stdout/stderr are captured separately, output size is capped at
// maxCmdOutputBytes to bound memory pressure on large repo lists,
// and exit codes are surfaced as structured errors.

package forges

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// maxCmdOutputBytes caps stdout/stderr capture per CLI call. 32 MiB
// is generous enough for a 500-entry repo list with metadata; any
// CLI streaming more than that is misbehaving.
const maxCmdOutputBytes = 32 << 20

// CmdError is returned when a CLI subprocess exits non-zero. It
// carries the stderr text (which often contains actionable hints
// like "not logged in" or "rate limited") and the exit code.
type CmdError struct {
	Err      error
	CLI      string
	Stderr   string
	Args     []string
	ExitCode int
}

func (e *CmdError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s %s: exit %d: %s", e.CLI, strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
	}
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %v", e.CLI, strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("%s %s: exit %d", e.CLI, strings.Join(e.Args, " "), e.ExitCode)
}

func (e *CmdError) Unwrap() error { return e.Err }

// runCmd executes a CLI command with timeout and captures its output.
// stdin, if non-nil, is fed to the subprocess's stdin (used for token
// paste flows).
//
// Returns ErrNotInstalled if the CLI binary is not on PATH.
// Returns a *CmdError for non-zero exit. Stderr is always normalized
// for inspection by callers (e.g. login.go inspecting auth errors).
func runCmd(ctx context.Context, timeout time.Duration, stdin []byte, cli string, args ...string) (stdout []byte, err error) {
	return runCmdEnv(ctx, timeout, stdin, nil, cli, args...)
}

// runCmdEnv is runCmd with extra environment variables merged into
// the subprocess env. Used by providers that prefer env-var host
// configuration over CLI flags — e.g. gh's GH_HOST works for every
// subcommand, while `--hostname` is rejected by `gh repo list`,
// `gh issue list`, and others.
func runCmdEnv(ctx context.Context, timeout time.Duration, stdin []byte, extraEnv []string, cli string, args ...string) (stdout []byte, err error) {
	if _, lookErr := exec.LookPath(cli); lookErr != nil {
		return nil, fmt.Errorf("%w: %s not on PATH", ErrNotInstalled, cli)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, cli, args...) //nolint:gosec // G702: user-initiated git command
	cmd.Env = sanitizeEnv(os.Environ())
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Env, extraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &cappedWriter{w: &outBuf, max: maxCmdOutputBytes}
	cmd.Stderr = &cappedWriter{w: &errBuf, max: maxCmdOutputBytes}

	runErr := cmd.Run()
	if runErr == nil {
		return outBuf.Bytes(), nil
	}
	stderrText := errBuf.String()
	exitCode := -1
	if ee, ok := errors.AsType[*exec.ExitError](runErr); ok {
		exitCode = ee.ExitCode()
	}
	// Surface "not logged in" with a typed error so callers can
	// route the user to the connect flow.
	if isNotLoggedIn(stderrText) {
		return outBuf.Bytes(), fmt.Errorf("%w: %s", ErrNotLoggedIn, strings.TrimSpace(stderrText))
	}
	return outBuf.Bytes(), &CmdError{
		CLI:      cli,
		Args:     args,
		ExitCode: exitCode,
		Stderr:   stderrText,
		Err:      runErr,
	}
}

// runJSON executes a CLI command and parses its stdout as JSON into v.
func runJSON(ctx context.Context, timeout time.Duration, v any, cli string, args ...string) error {
	return runJSONEnv(ctx, timeout, nil, v, cli, args...)
}

// runJSONEnv is runJSON with extra environment variables merged in.
func runJSONEnv(ctx context.Context, timeout time.Duration, extraEnv []string, v any, cli string, args ...string) error {
	out, err := runCmdEnv(ctx, timeout, nil, extraEnv, cli, args...)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	if jsonErr := json.Unmarshal(out, v); jsonErr != nil {
		return fmt.Errorf("%s json decode: %w", cli, jsonErr)
	}
	return nil
}

// cappedWriter caps the bytes written to w at max. Subsequent writes
// are silently dropped (the underlying buffer stops growing).
type cappedWriter struct {
	w   io.Writer
	max int64
	n   int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n >= c.max {
		return len(p), nil
	}
	remaining := c.max - c.n
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// sanitizeEnv strips variables that could leak credentials into a
// CLI subprocess (e.g. accidentally inheriting GH_TOKEN from an
// outer shell). The CLIs read their tokens from their own config
// files; env-var fallbacks just create confusion.
func sanitizeEnv(env []string) []string {
	stripped := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			key = kv[:i]
		}
		if shouldStripEnv(key) {
			continue
		}
		stripped = append(stripped, kv)
	}
	return stripped
}

// shouldStripEnv returns true for variables that should not flow into
// CLI subprocesses.
func shouldStripEnv(key string) bool {
	switch key {
	case "GH_TOKEN", "GITHUB_TOKEN",
		"GITLAB_TOKEN", "GLAB_TOKEN",
		"GITEA_TOKEN", "TEA_TOKEN":
		return true
	}
	return false
}

// notLoggedInPatterns is the data table of stderr substrings that
// indicate the forge CLI is not authenticated. Kept at package scope
// for inspectability and per-entry testability.
var notLoggedInPatterns = []string{
	"not logged in",           // gh
	"no token configured",     // glab
	"no logins available",     // tea
	"login required",          // various
	"authentication required", // various
}

// isNotLoggedIn detects "not authenticated" errors across all three CLIs.
func isNotLoggedIn(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, p := range notLoggedInPatterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
