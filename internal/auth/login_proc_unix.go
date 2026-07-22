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
//
// Fleet alignment note (l-f3 audit, 2026-07): this is the group HALF of the
// fleet's own-process-group child wrapper — deliberately NOT the full copy.
// The full shape (scheduler.NewCommandRunner + Setpgid + group-SIGTERM
// Cancel with a grace window) lives line-aligned in docker-renovate-scheduler
// runner.go defaultCommandRunner and pg-autodump internal/pg newCommand;
// this login flow wants a hard SIGKILL on timeout, not a graceful drain, and
// pulling the scheduler library in for two syscalls isn't worth the dep. If
// vibekit ever needs the graceful variant, that is the THIRD consumer that
// triggers extracting WithProcessGroup() into scheduler (scheduler.md,
// "Setpgid pairing rule").
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
