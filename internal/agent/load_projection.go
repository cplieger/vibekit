package agent

// Replay-projection lifecycle: turning a `session/load` replay into a chat
// transcript.
//
// KAS answers session/load by replaying the stored transcript as ordinary
// session/update notifications tagged `_meta.kiro.replay`. translate.Projection
// accumulates those into []vibekit.Message; this file owns WHEN one is open,
// which frames reach it, and when it is complete.
//
// # Why completion needs care
//
// session/load is issued inside bridge.Start, which BLOCKS on the result,
// while the replay frames arrive on the bc.Forward goroutine attached just
// before it. The frames precede the result on the wire, so by the time
// Start returns they have all been PUSHED to the bridge's notifCh — but
// that channel is buffered (256), so Forward has not necessarily DRAINED
// them. Settling the projection at Start's return would be a race against
// a partial transcript.
//
// replay_drain.go owns the condition it settles on instead, and the step
// route reads the same type, so a fix there reaches both. THREE triggers
// ask it here and each is needed: Forward per frame consumed (the ordinary
// drain), MarkReplayLoadedAt (a replay already drained when the RPC
// returned), and Forward's sealed call at exit (trailing frames that never
// came). So the settle runs on the spawn goroutine as well as Forward — see
// swapProjectedTranscript, which is written for that. A load that never
// returned leaves the drain unloaded and the projection is DISCARDED.
//
// The condition needs no timeout: bridge.replayBudget bounds the
// session/load RPC above it, which is what makes "a load that never
// returned" a state this code reaches at all. Do not move that budget down
// here — the settle is a correctness argument and a timer on it would only
// turn a sound barrier into a guess.

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// replay owns the session/load transcript projection: the in-flight rebuild
// of a chat's history from KAS's own replay, and the swap that makes it the
// record.
type replay struct {
	// chats is where a settled projection lands. Needs Mutate, not just
	// Get, so it takes the translator's 3-method contract.
	chats translate.ChatRecords
	// lifetime supplies the context the swap runs under, since a settle
	// outlives the frame that triggered it.
	lifetime *lifetime
	// projections are the rebuilds in flight, keyed by chat.
	projections map[vibekit.ChatID]*loadProjection
	// settling holds a projection whose swap is IN FLIGHT: it has left
	// projections, so no new frame lands in it, but its Mutate has not
	// returned. Keeping its barrier reachable here is what makes the barrier
	// span the swap — without it, ReplaySettled answers the already-closed
	// sentinel for the whole duration of the store write, which is exactly the
	// window a caller waits to avoid.
	settling map[vibekit.ChatID]*loadProjection
	// onProjection receives a settled transcript. Called WITHOUT projMu
	// held: the swap writes the chat store, and holding the projection
	// lock across that would let a store mutation and a replay frame
	// deadlock against each other.
	onProjection func(chatID vibekit.ChatID, msgs []vibekit.Message, watermark string)
	projMu       sync.Mutex
}

// loadProjection is one in-flight session/load's accumulating transcript.
// Guarded by Runtime.projMu; the fields are not independently safe.
type loadProjection struct {
	proj *translate.Projection
	// settled is closed exactly once, when this projection has been ADOPTED
	// into the record or abandoned. It is the barrier a caller about to rewrite
	// the transcript waits on — see ReplaySettled.
	settled chan struct{}
	// frames counts replay frames ingested, for the settle log — a load
	// that projects zero messages from many frames is a decoding bug.
	frames int
	// drain is the completion condition, shared with the step route. Last so
	// every pointer field stays ahead of it (govet fieldalignment).
	drain replayDrain
}

// The three triggers a settle can run from, for the settle log. `load` is the
// reader's own post-load attempt — a replay whose frames all drained before
// session/load returned, which nothing else would notice — `frame` is the position
// reached by a consumed frame, and `exit` is the bridge-exit seal.
const (
	settleOnLoad  = "load"
	settleOnFrame = "frame"
	settleOnExit  = "exit"
)

// closedBarrier is ReplaySettled's answer for a chat with no projection open:
// an already-closed channel, so a caller never waits for a resume that is not
// happening. One value for the whole process — a closed channel is
// stateless, and receiving from it is always ready.
var closedBarrier = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// OpenReplayProjection starts a projection for a chat about to session/load.
// A projection already open for that chat is discarded: the only way to reach
// this twice is a re-load (model-switch fallback), whose replay supersedes.
func (rp *replay) OpenReplayProjection(chatID vibekit.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if rp.projections == nil {
		rp.projections = make(map[vibekit.ChatID]*loadProjection)
	}
	if prev, dup := rp.projections[chatID]; dup {
		slog.Debug("replay projection: superseding an open one", "chat_id", chatID)
		// Release the superseded projection's barrier: its settle can never run
		// now, so anything waiting on it would wait for the life of the process.
		// The replacement's own settle closes a DIFFERENT channel.
		close(prev.settled)
	}
	rp.projections[chatID] = &loadProjection{
		proj:    translate.NewProjection(newMessageID),
		settled: make(chan struct{}),
	}
}

// ReplaySettled returns a channel closed once the chat's in-flight replay has
// been adopted into the record — or discarded, or superseded. No projection open
// answers already-closed, so a caller never blocks on a resume that is not
// happening. A channel rather than a bool: the interesting state is "not yet".
//
// `settling` is the load-bearing half: a projection mid-swap has left
// `projections`, so reading that map alone answers already-closed for the whole
// store write — the exact window a caller is waiting out.
func (rp *replay) ReplaySettled(chatID vibekit.ChatID) <-chan struct{} {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if lp := rp.projections[chatID]; lp != nil {
		return lp.settled
	}
	if lp := rp.settling[chatID]; lp != nil {
		return lp.settled
	}
	return closedBarrier
}

// MarkReplayLoadedAt records the read-loop position the session/load response
// arrived at, and ATTEMPTS one settle. Called from the spawn goroutine.
//
// The attempt is the point: a replay Forward has already drained is complete HERE
// and no later frame is coming to notice. Without it such a replay held its barrier
// until the bridge died, which no caller can wait for — so AwaitReplayAdopted spent
// its whole budget and refused a rewind of a transcript sitting fully built.
func (rp *replay) MarkReplayLoadedAt(chatID vibekit.ChatID, at drainPoint) {
	lp := rp.claimSettled(chatID, at.gen, false, func(d *replayDrain) { d.markLoadedAt(at) })
	rp.adopt(chatID, lp, settleOnLoad)
}

// DiscardReplayProjection drops a chat's projection unsettled, when the
// load failed, so a half-built transcript cannot be adopted later.
func (rp *replay) DiscardReplayProjection(chatID vibekit.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if lp, open := rp.projections[chatID]; open {
		delete(rp.projections, chatID)
		// A discarded projection adopts nothing, so the barrier is satisfied:
		// there is no swap left to race.
		close(lp.settled)
		slog.Debug("replay projection: discarded", "chat_id", chatID)
	}
}

// ingestReplayFrame folds one replay-tagged frame into the chat's open
// projection. Reports whether a projection consumed it, so the caller can
// fall back to dropping when a replay arrives with no load in flight.
func (rp *replay) ingestReplayFrame(chatID vibekit.ChatID, kind vibekit.ACPUpdateKind, raw json.RawMessage) bool {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return false
	}
	lp.proj.Ingest(kind, raw)
	lp.frames++
	return true
}

// SettleReplayProjection folds one drain observation into the chat's projection
// and completes it once the consumer has folded everything that preceded the load
// result. `at` is the frame's own position and the attachment that consumed it;
// `force` is the bridge-exit seal, which bypasses the position because no frame
// can advance it again. See replay_drain.go for the condition.
//
// No-op when no projection is open, so callers may call it per frame.
func (rp *replay) SettleReplayProjection(chatID vibekit.ChatID, at drainPoint, force bool) {
	lp := rp.claimSettled(chatID, at.gen, force, func(d *replayDrain) { d.noteConsumed(at) })
	trigger := settleOnFrame
	if force {
		trigger = settleOnExit
	}
	rp.adopt(chatID, lp, trigger)
}

// claimSettled applies one observation to the chat's drain and, when that leaves
// the replay complete, MOVES the projection into `settling` — returning it to
// exactly one caller and nil to every other.
//
// The claim is what makes the settle run once: three triggers reach it (Forward
// per frame, the reader's own post-load attempt, the bridge-exit seal) and they
// run on two goroutines. The MOVE rather than a delete is what makes the barrier
// span the swap; see ReplaySettled.
func (rp *replay) claimSettled(chatID vibekit.ChatID, gen uint64, force bool, note func(*replayDrain)) *loadProjection {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return nil
	}
	note(&lp.drain)
	if !lp.drain.complete(gen, force) {
		return nil
	}
	delete(rp.projections, chatID)
	if rp.settling == nil {
		rp.settling = make(map[vibekit.ChatID]*loadProjection)
	}
	rp.settling[chatID] = lp
	return lp
}

// adopt swaps a claimed projection into the record and releases its barrier ON
// RETURN. Nil is the ordinary answer — a settle attempt that claimed nothing.
func (rp *replay) adopt(chatID vibekit.ChatID, lp *loadProjection, trigger string) {
	if lp == nil {
		return
	}
	// Released on RETURN, not at claimSettled's map move: onProjection is the
	// swap, and a waiter woken before its Mutate has landed still races it. The
	// move only makes the projection unreachable to new FRAMES — ReplaySettled
	// still finds it in settling, which is what makes the barrier span the swap.
	defer func() {
		rp.projMu.Lock()
		// Only when the entry is still OURS: a supersede can open, settle and
		// register a second projection for this chat while this swap is in
		// flight, and deleting that one's entry would hide its barrier.
		if rp.settling[chatID] == lp {
			delete(rp.settling, chatID)
		}
		close(lp.settled)
		rp.projMu.Unlock()
	}()

	msgs := lp.proj.Messages()
	slog.Info("replay projection settled",
		"chat_id", chatID,
		"frames", lp.frames,
		"messages", len(msgs),
		"watermark", lp.proj.Watermark,
		"trigger", trigger)

	if rp.onProjection != nil {
		rp.onProjection(chatID, msgs, lp.proj.Watermark)
	}
}

// swapProjectedTranscript makes a settled replay the chat's transcript,
// merged with what a replay cannot speak for (see mergeProjection).
//
// Runs on the Forward goroutine OR on the spawn goroutine (MarkReplayLoadedAt is
// the second trigger), so it must not hold projMu — the store mutation below can
// block and a replay frame arriving meanwhile needs that lock.
//
// No broadcast: the swapped transcript is what the next fetch returns, and
// a connected client on a stale copy of an untouched chat sees the
// correction on its next refetch, the same window the gap/refetch path
// already covers.
func (rp *replay) swapProjectedTranscript(chatID vibekit.ChatID, msgs []vibekit.Message, watermark string) {
	var before, after int
	err := rp.chats.Mutate(durable.Context(rp.lifetime.shutdownCtx), chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		before = len(c.Messages)
		merged := mergeProjection(c.Messages, msgs)
		after = len(merged)
		sameWatermark := watermark == "" || watermark == c.CompactionWatermark
		if after == before && sameWatermark {
			return false
		}
		c.Messages = merged
		if watermark != "" {
			c.CompactionWatermark = watermark
		}
		return true
	})
	if err != nil {
		slog.Error("replay projection: swap failed", "chat_id", chatID, "error", err)
		return
	}
	// Logged with both counts because a SHRINK is the signal worth seeing:
	// it means the replay covered turns the record no longer holds.
	slog.Info("replay projection: transcript swapped",
		"chat_id", chatID, "was", before, "now", after, "projected", len(msgs))
}

// mergeProjection decides the transcript to persist after a replay: the
// projection's messages, plus the ones vibekit holds that a replay cannot
// speak for.
//
// A blind replace is wrong in two directions, each needing a different
// rule since message identity differs by role:
//
//   - USER and ASSISTANT messages inside the projection's window are the
//     wire's. Only the user half has matching ids — KAS echoes back the
//     messageId vibekit sent, while an assistant turn carries KAS's own
//     `<uuid>-say`. A merge keyed on assistant ids would keep every
//     existing turn AND add the projected one, duplicating the whole
//     transcript. The projection supersedes them wholesale instead.
//   - EVENT messages are not on the replay wire at all. cancelled,
//     model_switched, interrupted and compaction_failed are vibekit's own
//     record of what happened to a turn, so a replace would silently drop
//     every badge on every resume. They are preserved wherever they sit.
//   - A PLAN ROW is not on the replay wire either, and it is not an event:
//     translate.HandlePlan persists it as a RoleAssistant message whose only
//     payload is Plan. Role alone therefore drops it, and nothing regenerates
//     it — so every resumed chat lost its plan cards permanently. It is
//     preserved by SHAPE, which is what distinguishes it from a real turn.
//
// Anything newer than the projection's last message is preserved
// regardless of role: KAS's log is NOT fsynced, so a turn vibekit durably
// holds can legitimately be absent from a replay, and losing it to a
// resume would make the projection strictly worse than the stack it
// replaces.
func mergeProjection(existing, projected []vibekit.Message) []vibekit.Message {
	if len(projected) == 0 {
		return existing
	}

	newest := int64(0)
	projectedIDs := make(map[string]struct{}, len(projected))
	for i := range projected {
		projectedIDs[projected[i].ID] = struct{}{}
		if projected[i].Ts > newest {
			newest = projected[i].Ts
		}
	}

	out := make([]vibekit.Message, 0, len(projected)+len(existing))
	out = append(out, projected...)
	// Indexed rather than ranged by value: vibekit.Message is 216 bytes,
	// which gocritic's rangeValCopy flags.
	for i := range existing {
		if _, dup := projectedIDs[existing[i].ID]; dup {
			continue
		}
		if existing[i].Role == vibekit.RoleEvent || existing[i].Ts > newest ||
			isPlanRow(&existing[i]) {
			out = append(out, existing[i])
		}
	}

	// Stable sort so same-timestamp messages keep the order they were
	// added, which puts a projected message ahead of a preserved one at
	// the same instant — the projected copy is the more complete of the two.
	slices.SortStableFunc(out, func(a, b vibekit.Message) int {
		return cmp.Compare(a.Ts, b.Ts)
	})
	return out
}

// isPlanRow reports whether m is a turn's plan row: an assistant message whose
// ONLY payload is Plan. Every other assistant field must be empty, or a real
// reply that happened to carry a plan would survive a replay that already
// re-projected it and the turn would render twice.
func isPlanRow(m *vibekit.Message) bool {
	return m.Role == vibekit.RoleAssistant &&
		len(m.Plan) > 0 &&
		m.Content == "" &&
		m.Reasoning == "" &&
		len(m.ToolCalls) == 0 &&
		len(m.Blocks) == 0
}
