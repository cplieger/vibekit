//go:build unix

package hub

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// setShellProcAttr configures the shell process so signalShellGroup can
// target bash and any of its descendants by negative PGID.
//
// We deliberately do NOT set Setpgid here. pty.StartWithSize forces
// Setsid:true (and Setctty:true) on the cmd, and the Go runtime runs
// setsid before setpgid in forkAndExecInChild. After setsid the child
// is already a session AND process-group leader (PGID == PID), so a
// subsequent setpgid(0, 0) returns EPERM ("change the process group
// ID of a session leader") — which surfaces as
//
//	fork/exec /usr/bin/bash: operation not permitted
//
// and turns into a "shell unavailable" close on the WebSocket.
//
// Setsid alone is sufficient: bash starts as the leader of a brand-new
// process group whose PGID equals its PID, so signalShellGroup's
// kill(-pid, sig) reaches bash and any still-in-group children.
func setShellProcAttr(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
}

// signalShellGroup signals the foreground process group of the shell's
// controlling tty — the same group the kernel signals when the user
// types Ctrl+C in a real terminal.
//
// When bash has job control on (the default for an interactive --login
// shell on a pty) each pipeline runs in its own pgroup, and bash calls
// tcsetpgrp to mark the running job as the tty's foreground pgroup.
// Sending the signal to bash's own pgroup would miss the running job
// entirely. tcgetpgrp on the master fd returns the foreground pgid the
// kernel sees for the slave's tty, so kill(-fgPgid, sig) reaches the
// running command.
//
// Fallback: if tcgetpgrp fails or returns a non-positive pgid, signal
// bash's own pgroup. That preserves the original behavior when the
// foreground pgrp can't be determined (e.g. the master fd is closed,
// or running under a kernel where the ioctl isn't supported), and
// keeps SIGTERM/SIGQUIT useful even in degenerate cases.
func signalShellGroup(cmd *exec.Cmd, ptmx *os.File, sig syscall.Signal) error {
	if cmd.Process == nil {
		return errors.New("no shell process")
	}
	if ptmx != nil {
		if pgid, err := tcgetpgrp(ptmx.Fd()); err == nil && pgid > 0 {
			return syscall.Kill(-pgid, sig)
		}
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

// tcgetpgrp returns the process-group ID of the foreground process
// group of the controlling tty referred to by fd. Equivalent to the
// POSIX tcgetpgrp(3) call. Implemented as a raw ioctl(TIOCGPGRP) so we
// can use a master pty fd directly without round-tripping through
// /dev/tty (which would refer to the test runner's tty, not bash's).
func tcgetpgrp(fd uintptr) (int, error) {
	var pgid int32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGPGRP),
		uintptr(unsafe.Pointer(&pgid)),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(pgid), nil
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
