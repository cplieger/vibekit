//go:build unix

package auth

import (
	"os/exec"
	"syscall"
)

// setLoginProcAttr starts the kiro-cli login subprocess in its own
// process group so we can kill the whole tree on timeout. kiro-cli is
// a bun/Node wrapper that may spawn children; a PID-only kill orphans
// them under tini and leaves the stdout pipe open.
func setLoginProcAttr(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// loginKill sends SIGKILL to the login subprocess's whole group (-pgid).
func loginKill(c *exec.Cmd) error {
	if c.Process == nil {
		return syscall.ESRCH
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
