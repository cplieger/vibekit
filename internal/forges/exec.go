// Forge-CLI subprocess execution: structured output capture, size-capped
// buffers, and typed errors.
//
// This was internal/forges/cliexec, reachable only from here, and the wrapper
// file that stood in this place existed for no reason other than to re-export
// it at package scope ("so existing callers within the forges package continue
// to compile unchanged"). Rolling it up deleted that layer and unexported
// everything the forge providers do not name across a package boundary; only
// the two sentinel errors stay exported, because they are the error vocabulary
// a caller matches on.

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

// maxCmdOutputBytes caps stdout/stderr capture per CLI call. 32 MiB.
const maxCmdOutputBytes = 32 << 20

// ErrNotInstalled signals the backing CLI is not on PATH. It is the sentinel
// runCmd actually returns, and it is the one every errors.Is in this package
// matches against — the package used to declare a SEPARATE errors.New with the
// same message beside the providers, so every one of those checks silently
// never matched.
var ErrNotInstalled = errors.New("forges: CLI not installed")

// ErrNotLoggedIn signals the CLI is installed but no auth is configured.
var ErrNotLoggedIn = errors.New("forges: not logged in")

// cmdError is returned when a CLI subprocess exits non-zero.
type cmdError struct {
	Err      error
	CLI      string
	Stderr   string
	Args     []string
	ExitCode int
}

func (e *cmdError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s %s: exit %d: %s", e.CLI, strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
	}
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %v", e.CLI, strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("%s %s: exit %d", e.CLI, strings.Join(e.Args, " "), e.ExitCode)
}

func (e *cmdError) Unwrap() error { return e.Err }

// runCmd executes a CLI command with timeout and captures its output.
func runCmd(ctx context.Context, timeout time.Duration, stdin []byte, cli string, args ...string) (stdout []byte, err error) {
	return runCmdEnv(ctx, timeout, stdin, nil, cli, args...)
}

// runCmdEnv is runCmd with extra environment variables merged in.
func runCmdEnv(ctx context.Context, timeout time.Duration, stdin []byte, extraEnv []string, cli string, args ...string) (stdout []byte, err error) {
	if _, lookErr := exec.LookPath(cli); lookErr != nil {
		return nil, fmt.Errorf("%w: %s not on PATH", ErrNotInstalled, cli)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	// The directive below suppresses the unused-directive check as well, for the
	// reason internal/git/exec.go records in full: G702 is a G7xx taint rule
	// whose analysis does not terminate reliably on a package with many variadic
	// exec sites, so gosec reports it on some runs and not others, and the silent
	// runs fail the build. This is the second of the two sites in this repo that
	// rest on that rule alone.
	//nolint:gosec,nolintlint // G702: cli is one of the three literal CLI names Kind.CLI() returns, resolved through LookPath above; args are built by this package's providers and reach execve as separate tokens with no shell, so a request-supplied repo or branch cannot become a command
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = sanitizeEnv(os.Environ())
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Env, extraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &cappedWriter{W: &outBuf, Max: maxCmdOutputBytes}
	cmd.Stderr = &cappedWriter{W: &errBuf, Max: maxCmdOutputBytes}

	runErr := cmd.Run()
	if runErr == nil {
		return outBuf.Bytes(), nil
	}
	stderrText := errBuf.String()
	exitCode := -1
	if ee, ok := errors.AsType[*exec.ExitError](runErr); ok {
		exitCode = ee.ExitCode()
	}
	if isNotLoggedIn(stderrText) {
		return outBuf.Bytes(), fmt.Errorf("%w: %s", ErrNotLoggedIn, strings.TrimSpace(stderrText))
	}
	return outBuf.Bytes(), &cmdError{
		CLI:      cli,
		Args:     args,
		ExitCode: exitCode,
		Stderr:   stderrText,
		Err:      runErr,
	}
}

// runJSON executes a CLI command and parses its stdout as JSON into v.
//
//nolint:unparam // timeout and cli are kept for symmetry with the three sibling runners: every current caller is a single-object glab read under CmdTimeout, and collapsing either parameter would leave one of four wrappers with a different shape
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

// cappedWriter caps the bytes written to W at Max.
type cappedWriter struct {
	W   io.Writer
	Max int64
	N   int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
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

// sanitizeEnv strips variables that could leak credentials into a CLI subprocess.
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

// shouldStripEnv returns true for variables that should not flow into CLI subprocesses.
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
// indicate the forge CLI is not authenticated.
var notLoggedInPatterns = []string{
	"not logged in",
	"no token configured",
	"no logins available",
	"login required",
	"authentication required",
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
