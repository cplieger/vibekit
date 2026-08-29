package agent

// The read-loop sequence, and the position the FOLDER has reached: a response goes
// straight to the waiting Call while notifications queue for Forward, so a turn
// settled on its response alone is settled while turn_end sits unread (EWD687a).

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// attachForward resets the chat's observed position for a newly attached forward
// goroutine and returns the generation it runs under. A new bridge restarts its
// sequence at zero, so the previous position bounds nothing and the generation is
// what lets a straggling forward from the OLD bridge be ignored.
func (r *turnRegistry) attachForward(chatID vibekit.ChatID) uint64 {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.fwdGen++
	lc.observedSeq = 0
	lc.forwardGone = false
	lc.wakeLocked()
	return lc.fwdGen
}

// observe advances the position the folder has reached to seq, waking anything
// parked on it. Called for every frame Forward CONSUMES, not for every fold: many
// paths through the session-update cascade consume a frame without touching a turn,
// so a fold-bounded position can park a settle forever.
func (r *turnRegistry) observe(chatID vibekit.ChatID, gen, seq uint64) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if gen != lc.fwdGen || seq <= lc.observedSeq {
		return
	}
	lc.observedSeq = seq
	lc.wakeLocked()
}

// sealPosition records that the chat's forward goroutine has exited, so no further
// frame can advance the position. The waiters DEFER rather than close: the
// bridge-death closer names the process that went away, and every teardown vibekit
// performs itself has its own closer, so nothing is stranded.
func (r *turnRegistry) sealPosition(chatID vibekit.ChatID, gen uint64) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if gen != lc.fwdGen {
		return
	}
	lc.forwardGone = true
	lc.wakeLocked()
}

// awaitPosition parks until the folder has consumed everything that preceded this
// turn's response, reporting whether it REACHED that position. It waits WITHOUT
// the lifecycle mutex and WITHOUT claiming: claiming moves the chat into
// turnFinalizing, where a fold waits, so the settle would block the folder it
// waits for. It does NOT stop early once the awaited turn has finalized — the wait
// also orders whether a LATER turn opened, the empty-turn gate's structural clause,
// and returning early let a re-prompt duplicate execution and spend.
func (r *turnRegistry) awaitPosition(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	gen := lc.fwdGen
	if t := lc.turnLocked(epoch); t != nil {
		t.NeedSeq = seq
		t.needGen = gen
	}
	for {
		switch {
		case lc.observedSeq >= seq:
			lc.mu.Unlock()
			return true
		case lc.forwardGone, lc.fwdGen != gen:
			lc.mu.Unlock()
			return false
		}
		changed := lc.changed
		lc.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
		lc.mu.Lock()
	}
}
