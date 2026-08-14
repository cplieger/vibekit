package procgroup

import (
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
