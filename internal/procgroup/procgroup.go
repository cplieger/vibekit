// Package procgroup signals a spawned command's whole process group instead of
// just its head.
//
// It is its own package because both consumers need it and neither should own
// it: internal/agent spawns the agent's terminals and internal/bridge spawns
// kiro-cli, and agent already imports bridge, so exporting it from either would
// warp that package's surface for the other's benefit.
//
// Every consumer must pair Kill with SysProcAttr{Setpgid: true} on the command
// it spawns. Without that the child inherits vibekit's OWN process group and
// the guard below is the only thing standing between a teardown and vibekit
// signalling itself.
package procgroup

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kill signals p's whole process group, falling back to the head alone when p
// is not its own group leader.
//
// The head alone is not enough for either consumer, and for different reasons.
// An agent terminal is the agent's own command, so it is routinely a tree (a
// build tool, a test runner, a dev server) with no stdin-EOF channel to reclaim
// it, because an agent-chosen command need never read stdin.
//
// kiro-cli DOES read stdin, and closing it was once enough: the measurement
// behind that (0 of 2 trials leaked on kiro-cli 2.16.0) is why the bridge
// signalled only its head. That no longer holds. Measured on kiro-cli 2.18.0,
// one ordinary bridge teardown left `kiro-cli-chat` alive at 33 MB with its
// `node` child at 218 MB, reparented to init — so every model switch, tab close
// and idle cull leaked about 250 MB. Closing stdin first is still correct and
// still happens; it is no longer sufficient on its own.
//
// The pgid == pid guard is load-bearing, not defensive. Without Setpgid the
// child inherits vibekit's own process group, so Kill(-pgid, sig) would signal
// vibekit itself. The guard restricts the group form to when the child really is
// its own leader, which is exactly when Setpgid succeeded. A command that
// setsid()s on purpose still escapes the group; that is accepted, since a
// process which deliberately daemonizes is asking to outlive its parent.
func Kill(p *os.Process, sig syscall.Signal) error {
	pgid, err := syscall.Getpgid(p.Pid)
	if err == nil && Owns(p.Pid, pgid) {
		if gErr := syscall.Kill(-pgid, sig); gErr == nil {
			return nil
		}
	}
	// Not its own leader (Setpgid did not take, so the group is OURS and the
	// group form would signal vibekit), Getpgid failed (already reaped), or the
	// group signal failed — fall back to the head, which is the behaviour every
	// caller had before this package existed.
	return p.Signal(sig)
}

// GroupOf reports the process group p leads, and false when it leads none —
// Setpgid did not take, so the "group" is this process's own, or p is reaped.
//
// Exported because the pgid has to be read BEFORE the signal that empties the
// group: the head can be reaped the instant it dies and getpgid(2) then answers
// ESRCH, leaving nothing to wait on in exactly the case where teardown worked.
func GroupOf(p *os.Process) (pgid int, ok bool) {
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil || !Owns(p.Pid, pgid) {
		return 0, false
	}
	return pgid, true
}

// waitGonePoll is the retry interval WaitGone uses between /proc sweeps. Short
// enough that an ordinary teardown returns in one or two sweeps, long enough that
// a full-budget wait is a handful of sweeps rather than thousands.
const waitGonePoll = 20 * time.Millisecond

// WaitGone blocks until pgid's process group holds no LIVE member, bounded by
// budget. A false return is worth a log line and nothing more — the signal has
// already been sent.
//
// A ZOMBIE counts as gone, which is why this reads /proc instead of polling
// kill(-pgid, 0): an unreaped member is still signallable, and vibekit is PID 1, so
// that probe would burn the budget on every teardown that WORKED. Kill's own doc
// covers why the head alone is not the group.
func WaitGone(pgid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if !groupHasLiveMember(pgid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(waitGonePoll)
	}
}

// groupHasLiveMember reports whether any process in pgid is in a state other than
// zombie.
//
// A pgid at or below 1 answers false without walking anything: 0 means "the
// caller's own group" to kill(2) and 1 is init's, so a caller that lost its pgid
// would otherwise wait on the whole container.
func groupHasLiveMember(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc unreadable: nothing here can answer, and reporting "still alive"
		// would make every caller wait its whole budget for no information.
		return false
	}
	for _, e := range entries {
		pid, cerr := strconv.Atoi(e.Name())
		if cerr != nil {
			continue
		}
		gid, state, ok := statPgrpState(pid)
		if ok && gid == pgid && state != 'Z' {
			return true
		}
	}
	return false
}

// statPgrpState reads pid's process-group id and state letter out of
// /proc/<pid>/stat.
//
// Fields are counted from the LAST ')' because field 2 is the executable name in
// parentheses and may itself contain spaces and parens, which a whitespace split
// gets wrong. After that bracket the fields are state, ppid, pgrp.
func statPgrpState(pid int) (pgrp int, state byte, ok bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// The process exited between the directory read and this one, which is
		// the commonest outcome during a teardown sweep.
		return 0, 0, false
	}
	cut := bytes.LastIndexByte(raw, ')')
	if cut < 0 {
		return 0, 0, false
	}
	f := strings.Fields(string(raw[cut+1:]))
	if len(f) < 3 || f[0] == "" {
		return 0, 0, false
	}
	g, err := strconv.Atoi(f[2])
	if err != nil {
		return 0, 0, false
	}
	return g, f[0][0], true
}

// Owns reports whether pid is the leader of pgid, i.e. whether Setpgid took and
// the group contains only this command's tree.
//
// Exported as a pure function on purpose: this comparison is the guard that
// keeps Kill(-pgid, …) off vibekit's own process group, and pinning it by
// actually signalling a non-leader would deliver that signal to the test
// binary. Table-test the decision here; the tree-kill integration is covered
// separately against a real Setpgid child.
func Owns(pid, pgid int) bool { return pgid == pid }

// AlreadyGone reports whether err says the signal target had already been
// reaped. For a kill that is the outcome the caller wanted, arriving under
// another name, so it is not a failure to report.
//
// Two errors are one condition because the two ways this app signals a process
// spell the same fact differently. A raw syscall reports ESRCH: auth's killGroup
// returns it for a command that never started, and passes through the ESRCH of
// syscall.Kill(-pgid, sig) when the whole group has gone. (*os.Process).Signal
// never returns a bare ESRCH — os translates it (convertESRCH in
// os/exec_unix.go) and answers os.ErrProcessDone both for that and for a
// process this program has already Wait'ed. So the ESRCH half covers the callers
// that signal by syscall and the os.ErrProcessDone half covers Kill above, whose
// every failure path returns p.Signal's error.
//
// Deliberately NOT in the set, and this is the whole point of naming the
// condition once: EPERM (the target exists and we are not allowed to signal it)
// and EINVAL (a bad signal number) are real failures a caller must still report.
// On Linux, the only platform this ships on, there is no third spelling — the
// three syscalls involved (kill(2), pidfd_send_signal(2), getpgid(2)) all report
// a vanished target as ESRCH, which is either matched here directly or already
// folded into os.ErrProcessDone.
func AlreadyGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
