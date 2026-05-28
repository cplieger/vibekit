// Package cliexec provides CLI subprocess execution with structured
// output capture, size-capped buffers, and typed errors.
package cliexec

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

// MaxCmdOutputBytes caps stdout/stderr capture per CLI call. 32 MiB.
const MaxCmdOutputBytes = 32 << 20

// ErrNotInstalled signals the backing CLI is not on PATH.
var ErrNotInstalled = errors.New("forges: CLI not installed")

// ErrNotLoggedIn signals the CLI is installed but no auth is configured.
var ErrNotLoggedIn = errors.New("forges: not logged in")

// CmdError is returned when a CLI subprocess exits non-zero.
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

// RunCmd executes a CLI command with timeout and captures its output.
func RunCmd(ctx context.Context, timeout time.Duration, stdin []byte, cli string, args ...string) (stdout []byte, err error) {
	return RunCmdEnv(ctx, timeout, stdin, nil, cli, args...)
}

// RunCmdEnv is RunCmd with extra environment variables merged in.
func RunCmdEnv(ctx context.Context, timeout time.Duration, stdin []byte, extraEnv []string, cli string, args ...string) (stdout []byte, err error) {
	if _, lookErr := exec.LookPath(cli); lookErr != nil {
		return nil, fmt.Errorf("%w: %s not on PATH", ErrNotInstalled, cli)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, cli, args...) //nolint:gosec // G702: user-initiated git command
	cmd.Env = SanitizeEnv(os.Environ())
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Env, extraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &CappedWriter{W: &outBuf, Max: MaxCmdOutputBytes}
	cmd.Stderr = &CappedWriter{W: &errBuf, Max: MaxCmdOutputBytes}

	runErr := cmd.Run()
	if runErr == nil {
		return outBuf.Bytes(), nil
	}
	stderrText := errBuf.String()
	exitCode := -1
	if ee, ok := errors.AsType[*exec.ExitError](runErr); ok {
		exitCode = ee.ExitCode()
	}
	if IsNotLoggedIn(stderrText) {
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

// RunJSON executes a CLI command and parses its stdout as JSON into v.
func RunJSON(ctx context.Context, timeout time.Duration, v any, cli string, args ...string) error {
	return RunJSONEnv(ctx, timeout, nil, v, cli, args...)
}

// RunJSONEnv is RunJSON with extra environment variables merged in.
func RunJSONEnv(ctx context.Context, timeout time.Duration, extraEnv []string, v any, cli string, args ...string) error {
	out, err := RunCmdEnv(ctx, timeout, nil, extraEnv, cli, args...)
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

// CappedWriter caps the bytes written to W at Max.
type CappedWriter struct {
	W   io.Writer
	Max int64
	N   int64
}

func (c *CappedWriter) Write(p []byte) (int, error) {
	if c.N >= c.Max {
		return len(p), nil
	}
	remaining := c.Max - c.N
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := c.W.Write(p)
	c.N += int64(n)
	return n, err
}

// SanitizeEnv strips variables that could leak credentials into a CLI subprocess.
func SanitizeEnv(env []string) []string {
	stripped := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			key = kv[:i]
		}
		if ShouldStripEnv(key) {
			continue
		}
		stripped = append(stripped, kv)
	}
	return stripped
}

// ShouldStripEnv returns true for variables that should not flow into CLI subprocesses.
func ShouldStripEnv(key string) bool {
	switch key {
	case "GH_TOKEN", "GITHUB_TOKEN",
		"GITLAB_TOKEN", "GLAB_TOKEN",
		"GITEA_TOKEN", "TEA_TOKEN":
		return true
	}
	return false
}

// NotLoggedInPatterns is the data table of stderr substrings that
// indicate the forge CLI is not authenticated.
var NotLoggedInPatterns = []string{
	"not logged in",
	"no token configured",
	"no logins available",
	"login required",
	"authentication required",
}

// IsNotLoggedIn detects "not authenticated" errors across all three CLIs.
func IsNotLoggedIn(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, p := range NotLoggedInPatterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
