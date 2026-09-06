package agent

// The step-replay registry: one workflow step's `session/load` replay, keyed by
// ACP session id because a step has no chat. It touches no chat store — the
// projection is TAKEN by the reader and dropped, never swapped into a record.
// The completion condition is the shared `replayDrain` in replay_drain.go.

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// stepReplays holds the step replays in flight, keyed by ACP session id. Its zero
// value is usable.
type stepReplays struct {
	replays map[string]*stepReplay
	mu      sync.Mutex
}

// stepReplay is one step's accumulating transcript, guarded by stepReplays.mu.
type stepReplay struct {
	proj *translate.Projection
	// settled is the barrier the reader waits on; closed exactly once.
	settled chan struct{}
	// frames counts replay frames ingested; zero messages from many is a decoding bug.
	frames int
	// drain is the completion condition; last so pointer fields stay ahead (fieldalignment).
	drain replayDrain
}

// open starts a replay for a session about to be `session/load`ed, and reports
// whether it is the FIRST reader. A second concurrent reader is refused rather
// than superseded: superseding would leave the first waiting on a barrier nothing
// closes.
func (sr *stepReplays) open(sessionID string) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.replays == nil {
		sr.replays = make(map[string]*stepReplay)
	}
	if _, dup := sr.replays[sessionID]; dup {
		return false
	}
	sr.replays[sessionID] = &stepReplay{
		proj:    translate.NewProjection(newMessageID),
		settled: make(chan struct{}),
	}
	return true
}

// ingest folds one frame into the open replay for sessionID, reporting whether a
// replay consumed it. False is ordinary — no reader — so the forward goroutine
// consults this before warning about a foreign frame.
func (sr *stepReplays) ingest(sessionID string, kind vibekit.ACPUpdateKind, raw json.RawMessage) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return false
	}
	rep.proj.Ingest(kind, raw)
	rep.frames++
	return true
}

// markLoadedAt records the read-loop position the `session/load` response arrived
// at, and ATTEMPTS one settle. Called from the reader's own goroutine. The attempt
// is what makes the barrier reachable when the forward goroutine has already
// drained every frame the replay waits for.
func (sr *stepReplays) markLoadedAt(sessionID string, at drainPoint) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return
	}
	rep.drain.markLoadedAt(at)
	sr.settleLocked(sessionID, rep, at.gen, false, settleOnLoad)
}

// barrier returns a channel closed once the replay for sessionID has drained, or
// an already-closed one when no replay is open — so a reader never waits for a
// load that is not happening.
func (sr *stepReplays) barrier(sessionID string) <-chan struct{} {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if rep := sr.replays[sessionID]; rep != nil {
		return rep.settled
	}
	return closedBarrier
}

// settleConsumed folds one drain observation into every open replay and closes the
// barrier of each one the consumer has caught up with. No session id: the position
// is a property of the BRIDGE's read loop, and it holds per session because a
// replay's frames were all pushed before its own load result. `force` seals at exit.
func (sr *stepReplays) settleConsumed(at drainPoint, force bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	trigger := settleOnFrame
	if force {
		trigger = settleOnExit
	}
	for id, rep := range sr.replays {
		rep.drain.noteConsumed(at)
		sr.settleLocked(id, rep, at.gen, force, trigger)
	}
}

// settleLocked closes one replay's barrier when its drain is complete, logging the
// settle exactly once. Caller holds sr.mu.
func (sr *stepReplays) settleLocked(sessionID string, rep *stepReplay, gen uint64, force bool, trigger string) {
	if !rep.drain.complete(gen, force) {
		return
	}
	if sr.closeLocked(rep) {
		slog.Debug("step replay settled",
			"session_id", sessionID, "frames", rep.frames, "trigger", trigger)
	}
}

// take removes the replay for sessionID and returns what it projected. Runs
// whether the barrier closed or the budget expired, so an abandoned replay leaks
// neither an entry nor a waiter.
func (sr *stepReplays) take(sessionID string) []vibekit.Message {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return nil
	}
	delete(sr.replays, sessionID)
	// A take on the timeout path leaves a barrier nothing else can close.
	sr.closeLocked(rep)
	return rep.proj.Messages()
}

// closeLocked closes a replay's barrier at most once, reporting whether THIS call
// closed it. Caller holds sr.mu. Three paths reach a settled replay and closing a
// closed channel panics; the report keeps the settle log to one line per replay.
func (sr *stepReplays) closeLocked(rep *stepReplay) bool {
	select {
	case <-rep.settled:
		return false
	default:
		close(rep.settled)
		return true
	}
}
