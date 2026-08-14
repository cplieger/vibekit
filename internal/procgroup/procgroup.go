// Package procgroup signals a spawned command's whole process group instead of
// just its head.
//
// It is its own package because both consumers need it and neither should own
// it: internal/hub spawns the agent's terminals and internal/bridge spawns
// kiro-cli, and hub already imports bridge, so exporting it from either would
// warp that package's surface for the other's benefit.
//
// Every consumer must pair Kill with SysProcAttr{Setpgid: true} on the command
// it spawns. Without that the child inherits vibekit's OWN process group and
// the guard below is the only thing standing between a teardown and vibekit
// signalling itself.
package procgroup

import (
	"os"
	"syscall"
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

// Owns reports whether pid is the leader of pgid, i.e. whether Setpgid took and
// the group contains only this command's tree.
//
// Exported as a pure function on purpose: this comparison is the guard that
// keeps Kill(-pgid, …) off vibekit's own process group, and pinning it by
// actually signalling a non-leader would deliver that signal to the test
// binary. Table-test the decision here; the tree-kill integration is covered
// separately against a real Setpgid child.
func Owns(pid, pgid int) bool { return pgid == pid }
