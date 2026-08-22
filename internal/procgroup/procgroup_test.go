package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOwns pins the guard that keeps the group-kill form off vibekit's own
// process group.
//
// Without Setpgid a child inherits vibekit's group, so `Kill(-pgid, sig)` would
// signal vibekit itself. This comparison is the only thing preventing that, and
// it cannot be pinned by actually signalling a non-leader (the signal would land
// on the test binary), which is why the decision is a pure function.
func TestOwns(t *testing.T) {
	cases := []struct {
		name string
		pid  int
		pgid int
		want bool
	}{
		{name: "own leader: Setpgid took, group is the command's tree", pid: 4242, pgid: 4242, want: true},
		{name: "inherited group: Setpgid absent, group is vibekit's", pid: 4242, pgid: 17, want: false},
		{name: "inherited group where vibekit is the leader", pid: 4242, pgid: 1, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Owns(tc.pid, tc.pgid); got != tc.want {
				t.Errorf("Owns(%d, %d) = %v, want %v", tc.pid, tc.pgid, got, tc.want)
			}
		})
	}
}

// TestAlreadyGone pins which errors mean the signal target was already reaped,
// because that decision is what stands between a teardown log and a false alarm:
// callers gate a Warn on it, so a member wrongly added silences a real failure
// and a member wrongly dropped puts a warning on every ordinary release.
//
// The wrapped cases are the reason the predicate spells this with errors.Is and
// not ==. No caller wraps today — auth's killGroup returns the raw syscall error
// and Kill returns p.Signal's — so an unwrapped-only test would stay green while
// the first %w on either path silently broke every consumer's gate.
//
// EPERM is the negative case that stops the predicate being vacuously true: the
// target exists and we are not permitted to signal it, which is exactly the
// failure the Warn exists for.
func TestAlreadyGone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare ESRCH: no such process", err: syscall.ESRCH, want: true},
		{name: "bare ErrProcessDone: this program already reaped it", err: os.ErrProcessDone, want: true},
		{name: "wrapped ESRCH", err: fmt.Errorf("kill group: %w", syscall.ESRCH), want: true},
		{name: "wrapped ErrProcessDone", err: fmt.Errorf("signal head: %w", os.ErrProcessDone), want: true},
		{name: "EPERM: the process is there and we may not signal it", err: syscall.EPERM, want: false},
		{name: "EINVAL: a bad signal number, a programmer error", err: syscall.EINVAL, want: false},
		{name: "an unrelated error", err: errors.New("write |1: broken pipe"), want: false},
		{name: "no error: the kill landed on something alive", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AlreadyGone(tc.err); got != tc.want {
				t.Errorf("AlreadyGone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestKill_ReapedProcessIsAlreadyGone ties the predicate to the error Kill really
// produces on the path that made this necessary: KAS releases an agent terminal
// after wait_for_exit, so awaitExit has already reaped the process by the time
// the release site calls Kill.
//
// Without this, TestAlreadyGone pins a set of errnos with nothing saying they are
// the errnos this code meets. The measured answer on go1.27.0 is one specific
// member: Getpgid answers ESRCH so the group form is skipped, and p.Signal answers
// os.ErrProcessDone rather than a bare ESRCH, because os translates it.
func TestKill_ReapedProcessIsAlreadyGone(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := cmd.Process
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	err := Kill(p, syscall.SIGKILL)
	if err == nil {
		t.Fatal("Kill on a reaped process = nil; the release site would log nothing and this gate would be dead code")
	}
	if !AlreadyGone(err) {
		t.Errorf("AlreadyGone(Kill(reaped)) = false for %[1]v (%[1]T); the release site would warn on every normal release", err)
	}
	if !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("Kill(reaped) = %v, want os.ErrProcessDone; os.convertESRCH is what makes the ESRCH half of the predicate belong to the syscall callers instead", err)
	}
}

// Kill reclaims a tree whose head has children that outlive it, which is the
// shape both consumers produce: an agent terminal running a build tool, and
// `kiro-cli acp` re-execing kiro-cli-chat and node.
//
// The assertion is on the GRANDCHILD, because that is what survived the measured
// head-only kill. It is a direct child of the TEST binary that JOINS the group
// under test, so this test reaps it at an instant it controls rather than
// depending on the ambient reaper; a descendant forked inside the head would be
// orphaned the moment the head exits and the poll would be reaper-dependent.
func TestKill_ReapsTheWholeTree(t *testing.T) {
	head := exec.Command("sh", "-c", "echo $$; exec sleep 60")
	head.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := head.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if sErr := head.Start(); sErr != nil {
		t.Fatalf("start head: %v", sErr)
	}
	t.Cleanup(func() {
		_ = Kill(head.Process, syscall.SIGKILL)
		_ = head.Wait()
	})

	buf := make([]byte, 32)
	n, rErr := out.Read(buf)
	if rErr != nil || n == 0 {
		t.Fatalf("read head pid: n=%d err=%v", n, rErr)
	}
	leader, cErr := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if cErr != nil {
		t.Fatalf("parse head pid %q: %v", buf[:n], cErr)
	}

	// A second member of the head's group, owned by this test so it can be
	// reaped deterministically.
	member := exec.Command("sleep", "60")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader}
	if sErr := member.Start(); sErr != nil {
		t.Fatalf("start group member: %v", sErr)
	}
	memberDone := make(chan error, 1)
	go func() { memberDone <- member.Wait() }()

	// Without this the fixture is vacuous: a member that failed to join leaves
	// the group empty and every assertion below passes for the wrong reason.
	pgid, gErr := syscall.Getpgid(member.Process.Pid)
	if gErr != nil {
		t.Fatalf("Getpgid(member): %v", gErr)
	}
	if pgid != leader {
		t.Fatalf("member pgid = %d, want the head's group %d; the fixture proves nothing", pgid, leader)
	}

	if kErr := Kill(head.Process, syscall.SIGKILL); kErr != nil {
		t.Fatalf("Kill: %v", kErr)
	}

	// The member must die with the group. A head-only signal leaves it running.
	select {
	case <-memberDone:
	case <-time.After(5 * time.Second):
		_ = member.Process.Kill()
		t.Fatal("group member survived Kill; the signal reached the head alone")
	}
}
