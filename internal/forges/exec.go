// CLI subprocess runner — thin wrappers over internal/forges/cliexec.
//
// This file re-exports the cliexec API at package scope so existing
// callers within the forges package continue to compile unchanged.

package forges

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/forges/cliexec"
)

// runCmd executes a CLI command with timeout and captures its output.
//
//nolint:unparam // stdin mirrors cliexec.RunCmd; forge request bodies go through runCmdEnv (gh) or net/http (gitea), so runCmd's stdin is currently always nil
func runCmd(ctx context.Context, timeout time.Duration, stdin []byte, cli string, args ...string) ([]byte, error) {
	return cliexec.RunCmd(ctx, timeout, stdin, cli, args...)
}

// runCmdEnv is runCmd with extra environment variables merged in.
//
//nolint:unparam // cli kept for symmetry with runCmd; future forges (gitlab, codeberg) will pass non-"gh" here
func runCmdEnv(ctx context.Context, timeout time.Duration, stdin []byte, extraEnv []string, cli string, args ...string) ([]byte, error) {
	return cliexec.RunCmdEnv(ctx, timeout, stdin, extraEnv, cli, args...)
}

// runJSON executes a CLI command and parses its stdout as JSON into v.
func runJSON(ctx context.Context, timeout time.Duration, v any, cli string, args ...string) error {
	return cliexec.RunJSON(ctx, timeout, v, cli, args...)
}

// runJSONEnv is runJSON with extra environment variables merged in.
//
//nolint:unparam // cli kept for symmetry with runJSON; future forges will pass non-"gh" here
func runJSONEnv(ctx context.Context, timeout time.Duration, extraEnv []string, v any, cli string, args ...string) error {
	return cliexec.RunJSONEnv(ctx, timeout, extraEnv, v, cli, args...)
}

// sanitizeEnv strips credential-bearing env vars from a subprocess env.
func sanitizeEnv(env []string) []string {
	return cliexec.SanitizeEnv(env)
}
