package procgroup

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startGroupWithMember starts a head in its own process group plus a second member
// of that group owned by the TEST, and returns the group id and the member.
//
// The member is a direct child of the test binary rather than a descendant forked
// inside the head, for the reason TestKill_ReapsTheWholeTree records: a descendant
// is orphaned the moment the head exits, so whether it is reaped at all would
// depend on the ambient reaper, and this container's PID 1 is vibekit.
func startGroupWithMember(t *testing.T) (pgid int, member *exec.Cmd) {
	t.Helper()
	head := exec.Command("sh", "-c", "echo $$; exec sleep 60")
	head.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := head.StdoutPipe()
	if err != nil {
		t.Fatalf("Setup: stdout pipe: %v", err)
	}
	if sErr := head.Start(); sErr != nil {
		t.Fatalf("Setup: start head: %v", sErr)
	}
	t.Cleanup(func() {
		_ = Kill(head.Process, syscall.SIGKILL)
		_ = head.Wait()
	})
	buf := make([]byte, 32)
	n, rErr := out.Read(buf)
	if rErr != nil || n == 0 {
		t.Fatalf("Setup: read head pid: n=%d err=%v", n, rErr)
	}
	leader, cErr := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if cErr != nil {
		t.Fatalf("Setup: parse head pid %q: %v", buf[:n], cErr)
	}

	member = exec.Command("sleep", "60")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader}
	if sErr := member.Start(); sErr != nil {
		t.Fatalf("Setup: start group member: %v", sErr)
	}
	t.Cleanup(func() {
		_ = member.Process.Kill()
		_ = member.Wait()
	})
	// Without this the fixture is vacuous: a member that failed to join leaves the
	// group empty and every assertion passes for the wrong reason.
	got, gErr := syscall.Getpgid(member.Process.Pid)
	if gErr != nil {
		t.Fatalf("Setup: Getpgid(member): %v", gErr)
	}
	if got != leader {
		t.Fatalf("Setup: member pgid = %d, want the head's group %d; the fixture proves nothing", got, leader)
	}
	return leader, member
}

// WaitGone must not answer "gone" while the group still holds a running process.
// That is the whole claim: a teardown reads its return as the FACT that the
// command's tree has stopped, so a false positive is a caller told a build tool
// finished while it is still writing files.
func TestWaitGone_RefusesWhileAMemberIsRunning(t *testing.T) {
	pgid, _ := startGroupWithMember(t)

	start := time.Now()
	if WaitGone(pgid, 150*time.Millisecond) {
		t.Error("WaitGone(live group) = true; a teardown would report the command gone while its tree runs")
	}
	// It has to actually wait, or it is a `kill(pgid, 0)` spelled expensively.
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("WaitGone returned after %v, want at least the 150ms budget", elapsed)
	}
}

// The group emptying is what WaitGone reports, and it must report it promptly
// rather than burning the budget.
func TestWaitGone_ReturnsWhenTheGroupEmpties(t *testing.T) {
	pgid, member := startGroupWithMember(t)
	// Kill the whole group, then reap the one member this test owns. The head is
	// reaped by the cleanup startGroupWithMember registered.
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("Setup: kill group: %v", err)
	}
	_ = member.Wait()

	start := time.Now()
	if !WaitGone(pgid, 5*time.Second) {
		t.Fatal("WaitGone(killed group) = false; nothing live is left in it")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("WaitGone took %v on an empty group, want well under the budget", elapsed)
	}
}

// A ZOMBIE member counts as gone, and this is the case that decides whether
// WaitGone reads /proc or polls kill(-pgid, 0).
//
// An exited-but-unreaped process is still signallable, so the null-signal probe
// answers "present" for as long as nothing reaps it — and vibekit is PID 1 with no
// reaper, which is exactly that case. The cheap probe would therefore burn the full
// budget on every teardown that WORKED.
//
// The fixture makes the zombie deterministic: the group member is killed and
// deliberately NOT waited, so this test owns an unreaped child for the duration.
func TestWaitGone_AnUnreapedMemberCountsAsGone(t *testing.T) {
	pgid, member := startGroupWithMember(t)
	pid := member.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("Setup: kill group: %v", err)
	}
	// Wait for the member to actually reach Z, without reaping it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, state, ok := statPgrpState(pid)
		if ok && state == 'Z' {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("Setup: member %d never reached the zombie state (ok=%v)", pid, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The premise: an unreaped member is still a signallable group member, so the
	// probe WaitGone deliberately does not use still answers "present".
	if err := syscall.Kill(-pgid, syscall.Signal(0)); err != nil {
		t.Fatalf("Setup: kill(-%d, 0) = %v, want nil; without a signallable zombie this test asserts nothing", pgid, err)
	}

	if !WaitGone(pgid, 300*time.Millisecond) {
		t.Error("WaitGone(group of zombies) = false; every teardown would wait its whole grace and log a false warning")
	}
}

// A pgid this package never created must not be waited on. 0 means "the caller's
// own group" to kill(2) and 1 is init's, so a caller that lost its pgid would
// otherwise wait its whole budget on the container's own processes.
func TestWaitGone_RefusesToWaitOnAGroupItDoesNotOwn(t *testing.T) {
	for _, pgid := range []int{0, 1, -1} {
		start := time.Now()
		if !WaitGone(pgid, 5*time.Second) {
			t.Errorf("WaitGone(%d, 5s) = false, want true without waiting", pgid)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("WaitGone(%d) took %v; it walked /proc for a group it must refuse", pgid, elapsed)
		}
	}
}

// GroupOf answers the pgid only for a command that leads its own group, which is
// the guard that keeps a caller from waiting on vibekit's own group.
func TestGroupOf(t *testing.T) {
	own := exec.Command("sleep", "60")
	own.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := own.Start(); err != nil {
		t.Fatalf("Setup: start leader: %v", err)
	}
	t.Cleanup(func() {
		_ = own.Process.Kill()
		_ = own.Wait()
	})
	pgid, ok := GroupOf(own.Process)
	if !ok || pgid != own.Process.Pid {
		t.Errorf("GroupOf(leader) = (%d, %v), want (%d, true)", pgid, ok, own.Process.Pid)
	}

	// No Setpgid: the child inherits the TEST binary's group, so it leads none and
	// GroupOf must refuse it — waiting on that group would wait on the test.
	inherited := exec.Command("sleep", "60")
	if err := inherited.Start(); err != nil {
		t.Fatalf("Setup: start inheritor: %v", err)
	}
	t.Cleanup(func() {
		_ = inherited.Process.Kill()
		_ = inherited.Wait()
	})
	if pgid, ok := GroupOf(inherited.Process); ok {
		t.Errorf("GroupOf(non-leader) = (%d, true), want ok=false; a caller would wait on its own group", pgid)
	}

	// A reaped process answers ESRCH from getpgid, which is why the pgid has to be
	// read before the signal rather than after it.
	reaped := exec.Command("true")
	reaped.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := reaped.Start(); err != nil {
		t.Fatalf("Setup: start reaped: %v", err)
	}
	p := reaped.Process
	if err := reaped.Wait(); err != nil {
		t.Fatalf("Setup: wait reaped: %v", err)
	}
	if pgid, ok := GroupOf(p); ok {
		t.Errorf("GroupOf(reaped) = (%d, true), want ok=false", pgid)
	}
}

// statPgrpState counts its fields from the last ')' because field 2 is the
// executable name in parentheses and may itself contain spaces and parens. A
// whitespace split puts state and pgrp at the wrong index for exactly that
// process, and the symptom is a group that never reports gone.
func TestStatPgrpState_ReadsPastAnExecutableNameWithSpacesAndParens(t *testing.T) {
	dir := t.TempDir()
	// A command whose argv[0] basename carries both, so /proc/<pid>/stat's comm
	// field does too (comm is the basename, truncated to 15 bytes).
	name := "s (p) x"
	script := dir + "/" + name
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatalf("Setup: write script: %v", err)
	}
	cmd := exec.Command(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Setup: start %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = Kill(cmd.Process, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	pid := cmd.Process.Pid

	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("Setup: read stat: %v", err)
	}
	// The premise. Without a ')' inside comm this test is the plain case.
	if comm := string(raw); !strings.Contains(comm, "(s (p) x)") {
		t.Fatalf("Setup: stat comm is not the parenthesised name this test needs: %q", comm)
	}

	pgrp, state, ok := statPgrpState(pid)
	if !ok {
		t.Fatal("statPgrpState = ok:false for a live process")
	}
	if pgrp != pid {
		t.Errorf("statPgrpState(%d) pgrp = %d, want %d (the leader's own pid)", pid, pgrp, pid)
	}
	if state == 'Z' || state == 0 {
		t.Errorf("statPgrpState(%d) state = %q, want a live state; a misread field makes a live group look empty", pid, state)
	}
}
