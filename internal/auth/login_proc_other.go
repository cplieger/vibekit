//go:build !unix

package auth

import (
	"errors"
	"os/exec"
)

// setLoginProcAttr is a no-op on non-unix platforms; vibekit runs in
// Linux containers. Tests running on Windows fall back to a single-PID
// kill via cmd.Process.Kill() in killLoginProcess.
func setLoginProcAttr(_ *exec.Cmd) {}

// errUnsupportedKill is returned by loginKill on non-unix platforms so
// killLoginProcess falls through to its single-PID Kill fallback.
// Hoisted so callers can match with errors.Is (and so we stop
// allocating a fresh *errorString on every call — trivial, but the
// sentinel pattern is the idiomatic shape).
var errUnsupportedKill = errors.New("process-group kill unsupported on this platform")

// loginKill always returns errUnsupportedKill on non-unix so
// killLoginProcess falls through to the single-PID Kill fallback.
func loginKill(_ *exec.Cmd) error { return errUnsupportedKill }
