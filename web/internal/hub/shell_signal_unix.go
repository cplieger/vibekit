//go:build unix

package hub

import (
	"errors"
	"os/exec"
	"syscall"
)

// setShellProcAttr configures the shell process to start in its own
// process group. Lets us send signals to the foreground job (e.g. the
// child started by bash) rather than just bash itself.
func setShellProcAttr(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalShellGroup sends sig to the shell's entire process group.
// The negative PID targets the group, which is what we want: SIGINT
// should go to whatever bash is currently running, not bash itself
// (which ignores SIGINT by default in non-interactive mode).
func signalShellGroup(c *exec.Cmd, sig syscall.Signal) error {
	if c.Process == nil {
		return errors.New("no shell process")
	}
	return syscall.Kill(-c.Process.Pid, sig)
}

// parseSignal maps a POSIX signal name to its syscall constant.
// Only the signals the client is allowed to send are recognized.
func parseSignal(name string) (syscall.Signal, bool) {
	switch name {
	case "SIGINT":
		return syscall.SIGINT, true
	case "SIGTERM":
		return syscall.SIGTERM, true
	case "SIGQUIT":
		return syscall.SIGQUIT, true
	default:
		return 0, false
	}
}
