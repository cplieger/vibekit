//go:build unix

package hub

import (
	"errors"
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestSetShellProcAttr_PTYStart is a regression test for a subtle EPERM
// bug. creack/pty.StartWithSize forces Setsid:true (and Setctty:true) on
// the cmd; the Go runtime runs setsid before setpgid in
// forkAndExecInChild. After setsid the child is a session leader, so a
// subsequent setpgid(0, 0) returns EPERM. If setShellProcAttr ever sets
// Setpgid:true again, every shell-session start will fail with:
//
//	fork/exec /usr/bin/bash: operation not permitted
//
// and the /api/shell/ws WebSocket closes with code 1011 "shell
// unavailable" — which is exactly the "terminal console never loads"
// symptom in the browser.
//
// We test the production code path directly (setShellProcAttr +
// pty.StartWithSize) without hub plumbing so the failure mode is
// unambiguous, and we distinguish "EPERM from the regression" from "no
// /dev/ptmx in this sandbox" so CI environments without PTY support
// still skip cleanly.
func TestSetShellProcAttr_PTYStart(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not in PATH: %v", err)
	}

	c := exec.Command(bash, "-c", "exit 0")
	setShellProcAttr(c)

	ptmx, err := pty.StartWithSize(c, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Fatalf("regression: setShellProcAttr makes pty.StartWithSize "+
				"fail with EPERM. setpgid(0,0) on a session leader is always "+
				"EPERM, and pty.StartWithSize forces Setsid:true. Drop "+
				"Setpgid from setShellProcAttr. err=%v", err)
		}
		t.Skipf("PTY unavailable in this environment: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Drain the slave side so cmd.Wait doesn't block on a full pipe; the
	// goroutine exits naturally when the master is closed by the defer.
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()

	if wErr := c.Wait(); wErr != nil {
		t.Fatalf("bash --c 'exit 0' under PTY: %v", wErr)
	}
}

// TestSignalShellGroup_TargetsForegroundJob exercises the
// foreground-pgrp targeting in signalShellGroup. With job control on
// (the default for an interactive bash on a pty), bash places each
// pipeline in its own pgroup and uses tcsetpgrp to make the running
// job the tty's foreground pgroup. Sending the signal to bash's own
// pgroup misses the running job entirely.
//
// Sequence:
//  1. Start an interactive bash on a pty.
//  2. Wait for bash to claim the foreground (tcgetpgrp == bash.Pid).
//  3. Run `sleep` in the foreground.
//  4. Wait for the foreground pgrp to flip to sleep's pgroup.
//  5. Call signalShellGroup(SIGINT).
//  6. Verify the foreground pgrp returns to bash.Pid (sleep died and
//     bash reclaimed the tty).
//
// With a buggy signalShellGroup that targets only bash's pgrp, step 6
// times out: sleep keeps running because nothing in its pgroup got
// the signal.
func TestSignalShellGroup_TargetsForegroundJob(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not in PATH: %v", err)
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not in PATH: %v", err)
	}

	// --noprofile/--norc keep the test hermetic from the user's bash
	// config (some setups print MOTD or run aliases that disturb the
	// pgrp transitions we're observing). -i forces interactive mode so
	// job control kicks in even though stdin/stdout aren't /dev/tty.
	c := exec.Command(bash, "--noprofile", "--norc", "-i")
	setShellProcAttr(c)

	ptmx, err := pty.StartWithSize(c, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Fatalf("regression: setShellProcAttr makes pty.StartWithSize "+
				"fail with EPERM. err=%v", err)
		}
		t.Skipf("PTY unavailable in this environment: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		// Best-effort cleanup: kill bash if it's still alive after the
		// test, otherwise leak the goroutine into the next test.
		if c.Process != nil {
			_ = c.Process.Kill()
			_, _ = c.Process.Wait()
		}
	}()

	// Drain output so bash doesn't block on a full pipe. The goroutine
	// exits when ptmx.Close in the defer above unblocks the read.
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()

	bashPID := c.Process.Pid

	// Step 2: bash sets up job control asynchronously after exec; wait
	// for it to claim the tty as its foreground pgrp before sending
	// commands.
	if !waitFor(2*time.Second, func() bool {
		pgid, err := tcgetpgrp(ptmx.Fd())
		return err == nil && pgid == bashPID
	}) {
		pgid, _ := tcgetpgrp(ptmx.Fd())
		t.Fatalf("bash never became the tty's foreground pgrp. "+
			"pgid=%d bashPID=%d", pgid, bashPID)
	}

	// Step 3: launch a sleep in the foreground. The newline triggers
	// command parsing; bash forks sleep into a fresh pgroup and
	// tcsetpgrp's it as the foreground.
	if _, wErr := io.WriteString(ptmx, "sleep 30\n"); wErr != nil {
		t.Fatalf("write to ptmx: %v", wErr)
	}

	// Step 4: wait for the foreground pgrp to differ from bash's PID.
	var sleepPGID int
	if !waitFor(2*time.Second, func() bool {
		pgid, err := tcgetpgrp(ptmx.Fd())
		if err == nil && pgid > 0 && pgid != bashPID {
			sleepPGID = pgid
			return true
		}
		return false
	}) {
		pgid, _ := tcgetpgrp(ptmx.Fd())
		t.Fatalf("sleep never became foreground pgrp. "+
			"pgid=%d bashPID=%d (job control may be off)", pgid, bashPID)
	}

	// Step 5: send SIGINT through the production code path.
	if err := signalShellGroup(c, ptmx, syscall.SIGINT); err != nil {
		t.Fatalf("signalShellGroup: %v", err)
	}

	// Step 6: bash reclaims the tty after sleep dies. With buggy
	// behavior (signal goes to bash's pgrp, not sleep's), this never
	// happens.
	if !waitFor(2*time.Second, func() bool {
		pgid, err := tcgetpgrp(ptmx.Fd())
		return err == nil && pgid == bashPID
	}) {
		pgid, _ := tcgetpgrp(ptmx.Fd())
		t.Fatalf("after SIGINT, bash didn't reclaim the foreground. "+
			"pgid=%d bashPID=%d sleepPGID=%d — SIGINT didn't reach "+
			"the running job", pgid, bashPID, sleepPGID)
	}

	// Bash should still be alive; SIGINT in interactive mode just
	// abandons the current input line.
	var status syscall.WaitStatus
	wpid, werr := syscall.Wait4(c.Process.Pid, &status, syscall.WNOHANG, nil)
	if werr == nil && wpid == c.Process.Pid && status.Exited() {
		t.Fatalf("bash exited unexpectedly after SIGINT: status=%v", status)
	}
}

// waitFor polls cond every 20ms until it returns true or timeout
// elapses, returning whether cond was observed true.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
