//go:build !unix

package hub

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setShellProcAttr is a no-op on non-unix platforms; the shell subsystem
// is only exercised in production Linux containers.
func setShellProcAttr(_ *exec.Cmd) {}

// signalShellGroup is unsupported on non-unix platforms.
func signalShellGroup(_ *exec.Cmd, _ *os.File, _ syscall.Signal) error {
	return errors.New("shell signals not supported on this platform")
}

// parseSignal returns false everywhere on non-unix platforms so the
// client request is rejected cleanly.
func parseSignal(_ string) (syscall.Signal, bool) { return 0, false }
