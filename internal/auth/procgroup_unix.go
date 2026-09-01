//go:build unix

package auth

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts a kiro-cli subprocess in its own process group so the
// whole tree can be killed on timeout. kiro-cli is a bun/Node wrapper that
// may spawn children; a PID-only kill orphans them and leaves the stdout
// pipe open, pinning cmd.Wait to the last descendant's lifetime.
//
// This is the group HALF of the fleet's own-process-group child wrapper
// (`scheduler.md` "Setpgid pairing rule") — deliberately not the full
// shape, since this login flow wants a hard SIGKILL on timeout, not a
// graceful drain.
func setProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup sends SIGKILL to the subprocess's whole group (-pgid).
func killGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return syscall.ESRCH
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
