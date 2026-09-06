package agent

// The step-replay registry: turning ONE workflow step's `session/load` replay
// into a transcript, on demand.
//
// Modelled on load_projection.go and deliberately NOT a share of it. Two
// differences decide the shape:
//
//   - It is keyed by ACP SESSION id rather than by chat, because a step has no
//     chat. `session/load` addresses a session, and a step's session id is what
//     `inspect`'s state tree hands out.
//   - It touches NO chat store. A step's transcript is never persisted — the
//     content belongs to a turn vibekit never prompted, so nothing finalizes it —
//     which is the whole shape of this feature: the projection is TAKEN by the
//     reader and dropped, never swapped into a record.
//
// # When a replay is complete
//
// The completion condition is the SHARED one, `replayDrain` in replay_drain.go,
// which the chat twin embeds too: the consumer has folded every frame that
// preceded the `session/load` result. That file owns the argument; read it there
// rather than trusting a restatement here.
//
// One consequence of the shared UTILITY bridge that file does not have to state:
// the read-loop position is a property of the BRIDGE, not of a session, so ONE
// observation serves every open replay. That is why settleConsumed takes no
// session id — see its own comment.

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// stepReplays holds the step replays in flight, keyed by ACP session id.
//
// A VALUE whose zero is usable, like Runs.asks, so a bare &Runs{} works in a
// test without wiring.
type stepReplays struct {
	replays map[string]*stepReplay
	mu      sync.Mutex
}

// stepReplay is one step's accumulating transcript.
//
// Guarded by stepReplays.mu; the fields are not independently safe.
type stepReplay struct {
	proj *translate.Projection
	// settled is closed exactly once, when this replay has been drained or
	// abandoned. It is the barrier the reader waits on.
	settled chan struct{}
	// frames counts replay frames ingested, for the settle log — a load that
	// projects zero messages from many frames is a decoding bug.
	frames int
	// drain is the completion condition, shared with the chat twin. Last so every
	// pointer field stays ahead of it (govet fieldalignment).
	drain replayDrain
}

// openStepReplay starts a replay for a session about to be `session/load`ed, and
// reports whether it is the FIRST reader for that session.
//
// A second concurrent read of one step is refused rather than served, which is
// the opposite of load_projection.go's supersede-and-discard: there a re-load is
// a model-switch fallback whose replay genuinely supersedes, while here two
// readers of one step are two requests for the same bytes and superseding would
// leave the first waiting on a barrier nothing closes. The caller answers
// `unavailable`, and the retry meets a settled registry.
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
// replay consumed it. False is the ordinary answer for every frame of every
// session nobody is reading, which is what lets the utility session's forward
// goroutine consult this before it warns about a foreign frame.
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
// at, and ATTEMPTS one settle. Called from the reader's own goroutine.
//
// The attempt is what makes the barrier reachable for the case this route hits
// most: every frame the replay waits for was pushed before that response, so a
// replay the utility session's forward goroutine has already drained is complete
// HERE. Without it the read held the whole 60s budget and answered `unavailable`
// for a transcript that existed and was fully projected.
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
// load that is not happening. A channel rather than a bool: the interesting state
// is "not yet".
func (sr *stepReplays) barrier(sessionID string) <-chan struct{} {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if rep := sr.replays[sessionID]; rep != nil {
		return rep.settled
	}
	return closedBarrier
}

// settleConsumed folds one drain observation into every open replay and closes the
// barrier of each one the consumer has now caught up with.
//
// NO session id, because the caller has none: the position is a property of the
// BRIDGE's read loop and the caller is the drain loop. It still holds per session,
// since a replay's frames were all pushed before its OWN load result. `force` is the
// bridge-exit seal, and a parameter rather than a position value because a channel
// DEPTH of 0 doubled as "nothing more can arrive" while a POSITION of 0 does not.
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

// take removes the replay for sessionID and returns what it projected. The
// reader's own cleanup: it runs whether the barrier closed or the budget expired,
// so an abandoned replay leaks neither an entry nor a waiter.
func (sr *stepReplays) take(sessionID string) []vibekit.Message {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return nil
	}
	delete(sr.replays, sessionID)
	// Closed here too: a take on the timeout path leaves a barrier nothing else
	// will ever close, and settleConsumed can no longer reach this entry.
	sr.closeLocked(rep)
	return rep.proj.Messages()
}

// closeLocked closes a replay's barrier at most once, reporting whether THIS call
// closed it. Caller holds sr.mu.
//
// Three paths reach a settled replay — the drain loop, the reader's own post-load
// settle attempt, and the reader's take — and closing an already-closed channel
// panics, so the idempotence is a correctness requirement rather than tidiness.
// The report is what keeps the settle log to one line per replay, since the drain
// loop attempts a settle on every frame that follows.
func (sr *stepReplays) closeLocked(rep *stepReplay) bool {
	select {
	case <-rep.settled:
		return false
	default:
		close(rep.settled)
		return true
	}
}
