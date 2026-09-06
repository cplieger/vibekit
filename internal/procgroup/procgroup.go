// Package procgroup signals a spawned command's whole process group instead of
// just its head.
//
// Every consumer must pair Kill with SysProcAttr{Setpgid: true} on the command it
// spawns. Without that the child inherits vibekit's OWN process group, and Kill's
// leader guard is all that stands between a teardown and vibekit signalling
// itself.
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

// Kill signals p's whole process group, falling back to the head alone when p is
// not its own group leader. A command that setsid()s still escapes the group.
//
// The head alone leaks the tree: an agent-chosen command need never read stdin,
// and kiro-cli 2.18.0 leaves its node child reparented to init even with stdin
// closed. The pgid == pid guard is load-bearing — without Setpgid the child
// inherits vibekit's own group, so Kill(-pgid, sig) would signal vibekit itself.
func Kill(p *os.Process, sig syscall.Signal) error {
	pgid, err := syscall.Getpgid(p.Pid)
	if err == nil && Owns(p.Pid, pgid) {
		if gErr := syscall.Kill(-pgid, sig); gErr == nil {
			return nil
		}
	}
	// Not its own leader (the group is OURS), already reaped, or the group signal
	// failed: the head is the only safe target left.
	return p.Signal(sig)
}

// GroupOf reports the process group p leads, and false when it leads none. Read
// it BEFORE the signal that empties the group: the head can be reaped the instant
// it dies and getpgid(2) then answers ESRCH, leaving nothing to wait on in
// exactly the case where teardown worked.
func GroupOf(p *os.Process) (pgid int, ok bool) {
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil || !Owns(p.Pid, pgid) {
		return 0, false
	}
	return pgid, true
}

// waitGonePoll is the interval between WaitGone's /proc sweeps: an ordinary
// teardown returns in one or two, a full-budget wait in a handful.
const waitGonePoll = 20 * time.Millisecond

// WaitGone blocks until pgid's process group holds no LIVE member, bounded by
// budget. A false return is worth a log line and nothing more; the signal has
// already been sent.
//
// A ZOMBIE counts as gone, which is why this reads /proc rather than polling
// kill(-pgid, 0): an unreaped member is still signallable, and vibekit is PID 1,
// so that probe would burn the budget on every teardown that WORKED.
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
// zombie. A pgid at or below 1 answers false without walking anything: 0 is the
// caller's own group to kill(2) and 1 is init's, so a caller that lost its pgid
// would otherwise wait on the whole container.
func groupHasLiveMember(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// Nothing here can answer, and "still alive" would burn every caller's
		// whole budget for no information.
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
// /proc/<pid>/stat. Fields are counted from the LAST ')' because field 2 is the
// executable name in parens and may itself contain spaces and parens; after that
// bracket the fields are state, ppid, pgrp.
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

// Owns reports whether pid leads pgid, i.e. whether Setpgid took and the group
// holds only this command's tree. Pure and exported so the guard that keeps
// Kill(-pgid, …) off vibekit's own group can be tested without signalling a
// non-leader, which would hit the test binary.
func Owns(pid, pgid int) bool { return pgid == pid }

// AlreadyGone reports whether err says the signal target had already been reaped,
// which for a kill is success under another name. Two spellings, one condition: a
// raw syscall reports ESRCH, (*os.Process).Signal translates it to
// os.ErrProcessDone. EPERM and EINVAL are excluded — real failures a caller must
// still report.
func AlreadyGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
