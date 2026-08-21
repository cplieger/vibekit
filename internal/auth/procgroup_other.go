//go:build !unix

package auth

import (
	"errors"
	"os/exec"
)

// setProcGroup is a no-op on non-unix platforms; vibekit runs in
// Linux containers. Tests running on Windows fall back to a single-PID
// kill via cmd.Process.Kill() in killProcessGroup.
//
// Consequence for boundChild: with no process group, its Cancel reaches only the
// parent PID, so WaitDelay is the whole bound there rather than the backstop.
func setProcGroup(_ *exec.Cmd) {}

// errUnsupportedKill is returned by killGroup on non-unix platforms so
// killProcessGroup falls through to its single-PID Kill fallback.
// Hoisted so callers can match with errors.Is (and so we stop
// allocating a fresh *errorString on every call — trivial, but the
// sentinel pattern is the idiomatic shape).
var errUnsupportedKill = errors.New("process-group kill unsupported on this platform")

// killGroup always returns errUnsupportedKill on non-unix so
// killProcessGroup falls through to the single-PID Kill fallback.
func killGroup(_ *exec.Cmd) error { return errUnsupportedKill }
