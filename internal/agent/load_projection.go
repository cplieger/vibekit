package agent

// Replay-projection lifecycle. Replay frames are PUSHED to the bridge's buffered
// notifCh before session/load returns, so settling at Start's return would race a
// partial transcript; replay_drain.go owns the completion condition instead, and the
// step route reads the same type. bridge.replayBudget bounds the RPC above, so the
// condition needs no timeout of its own. A load that never returned leaves the drain
// unloaded and the projection is DISCARDED.

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
	// chats is where a settled projection lands; needs Mutate, not just Get.
	chats translate.ChatRecords
	// lifetime supplies the context the swap runs under.
	lifetime *lifetime
	// projections are the rebuilds in flight, keyed by chat.
	projections map[vibekit.ChatID]*loadProjection
	// settling holds a projection whose swap is IN FLIGHT: it has left projections, so
	// no new frame lands in it, but its barrier stays reachable here until Mutate
	// returns, which is what makes the barrier span the store write.
	settling map[vibekit.ChatID]*loadProjection
	// onProjection receives a settled transcript, called WITHOUT projMu held: the swap
	// writes the chat store, and holding the lock across that would let a store
	// mutation and a replay frame deadlock against each other.
	onProjection func(chatID vibekit.ChatID, msgs []vibekit.Message, watermark string)
	projMu       sync.Mutex
}

// loadProjection is one in-flight session/load's accumulating transcript.
// Guarded by Runtime.projMu; the fields are not independently safe.
type loadProjection struct {
	proj *translate.Projection
	// settled is closed exactly once, when this projection has been ADOPTED into the
	// record or abandoned — the barrier ReplaySettled hands out.
	settled chan struct{}
	// frames counts replay frames ingested: many frames projecting zero messages is a
	// decoding bug.
	frames int
	// drain is the completion condition, shared with the step route. Last so every
	// pointer field stays ahead of it (govet fieldalignment).
	drain replayDrain
}

// The three triggers a settle can run from, for the settle log: the reader's own
// post-load attempt, the position a consumed frame reached, and the bridge-exit seal.
const (
	settleOnLoad  = "load"
	settleOnFrame = "frame"
	settleOnExit  = "exit"
)

// closedBarrier is ReplaySettled's answer for a chat with no projection open, so a
// caller never waits for a resume that is not happening. One value for the whole
// process: a closed channel is stateless and always ready.
var closedBarrier = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// OpenReplayProjection starts a projection for a chat about to session/load. A
// projection already open for that chat is discarded: the only way to reach this twice
// is a re-load (model-switch fallback), whose replay supersedes.
func (rp *replay) OpenReplayProjection(chatID vibekit.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if rp.projections == nil {
		rp.projections = make(map[vibekit.ChatID]*loadProjection)
	}
	if prev, dup := rp.projections[chatID]; dup {
		slog.Debug("replay projection: superseding an open one", "chat_id", chatID)
		// The superseded settle can never run, so release its barrier or a waiter
		// waits for the life of the process; the replacement closes a different one.
		close(prev.settled)
	}
	rp.projections[chatID] = &loadProjection{
		proj:    translate.NewProjection(newMessageID),
		settled: make(chan struct{}),
	}
}

// ReplaySettled returns a channel closed once the chat's in-flight replay has been
// adopted into the record — or discarded, or superseded. No projection open answers
// already-closed, so a caller never blocks on a resume that is not happening.
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

// MarkReplayLoadedAt records the read-loop position the session/load response arrived
// at, and ATTEMPTS one settle. Called from the spawn goroutine.
//
// The attempt is the point: a replay Forward has already drained is complete HERE and
// no later frame is coming to notice it.
func (rp *replay) MarkReplayLoadedAt(chatID vibekit.ChatID, at drainPoint) {
	lp := rp.claimSettled(chatID, at.gen, false, func(d *replayDrain) { d.markLoadedAt(at) })
	rp.adopt(chatID, lp, settleOnLoad)
}

// DiscardReplayProjection drops a chat's projection unsettled, when the load failed,
// so a half-built transcript cannot be adopted later.
func (rp *replay) DiscardReplayProjection(chatID vibekit.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if lp, open := rp.projections[chatID]; open {
		delete(rp.projections, chatID)
		// A discarded projection adopts nothing, so there is no swap left to race.
		close(lp.settled)
		slog.Debug("replay projection: discarded", "chat_id", chatID)
	}
}

// ingestReplayFrame folds one replay-tagged frame into the chat's open projection.
// Reports whether a projection consumed it, so the caller can drop a replay that
// arrived with no load in flight.
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

// SettleReplayProjection folds one drain observation into the chat's projection and
// completes it once the consumer has folded everything that preceded the load result.
// `at` is the frame's own position and the attachment that consumed it; `force` is the
// bridge-exit seal, which bypasses the position because no frame can advance it again.
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

// claimSettled applies one observation to the chat's drain and, when that leaves the
// replay complete, MOVES the projection into `settling` — returning it to exactly one
// caller and nil to every other. The claim is what makes the settle run once: three
// triggers reach it, on two goroutines. The move rather than a delete is what keeps the
// barrier reachable across the swap.
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

// adopt swaps a claimed projection into the record and releases its barrier ON RETURN.
// Nil is the ordinary answer — a settle attempt that claimed nothing.
func (rp *replay) adopt(chatID vibekit.ChatID, lp *loadProjection, trigger string) {
	if lp == nil {
		return
	}
	// Released on RETURN, not at claimSettled's map move: onProjection is the swap, and
	// a waiter woken before its Mutate has landed still races it.
	defer func() {
		rp.projMu.Lock()
		// Only when the entry is still OURS: a supersede can register a second
		// projection for this chat mid-swap, and deleting that would hide its barrier.
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

// swapProjectedTranscript makes a settled replay the chat's transcript, merged with
// what a replay cannot speak for (see mergeProjection).
//
// Runs on the Forward goroutine OR on the spawn goroutine, so it must not hold projMu —
// the store mutation below can block and a replay frame arriving meanwhile needs that
// lock. No broadcast: the swapped transcript is what the next fetch returns, the same
// window the gap/refetch path already covers.
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
	// Both counts because a SHRINK is the signal: the replay covered turns the record
	// no longer holds.
	slog.Info("replay projection: transcript swapped",
		"chat_id", chatID, "was", before, "now", after, "projected", len(msgs))
}

// mergeProjection decides the transcript to persist after a replay: the projection's
// messages, plus the ones vibekit holds that a replay cannot speak for. Role alone is
// not the rule — only the user half of the window has matching ids, so a merge keyed on
// assistant ids would duplicate the whole transcript; EVENT rows are not on the replay
// wire, so a replace drops every badge; and a plan row is RoleAssistant, also absent
// from the wire, and regenerated by nothing, so it is kept by SHAPE. Anything newer than
// the projection's last message survives regardless of role: KAS's log is NOT fsynced,
// so a turn vibekit durably holds can legitimately be absent from a replay.
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
	// Indexed, not ranged by value: vibekit.Message is 216 bytes (gocritic rangeValCopy).
	for i := range existing {
		if _, dup := projectedIDs[existing[i].ID]; dup {
			continue
		}
		if existing[i].Role == vibekit.RoleEvent || existing[i].Ts > newest ||
			isPlanRow(&existing[i]) {
			out = append(out, existing[i])
		}
	}

	// Stable, so at the same instant a projected message stays ahead of a preserved
	// one — the projected copy is the more complete of the two.
	slices.SortStableFunc(out, func(a, b vibekit.Message) int {
		return cmp.Compare(a.Ts, b.Ts)
	})
	return out
}

// isPlanRow reports whether m is a turn's plan row: an assistant message whose ONLY
// payload is Plan. Every other assistant field must be empty, or a real reply carrying a
// plan would survive a replay that already re-projected it and render twice.
func isPlanRow(m *vibekit.Message) bool {
	return m.Role == vibekit.RoleAssistant &&
		len(m.Plan) > 0 &&
		m.Content == "" &&
		m.Reasoning == "" &&
		len(m.ToolCalls) == 0 &&
		len(m.Blocks) == 0
}
