package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/sse"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayUpdate builds a replay-tagged session/update `update` object.
func replayUpdate(t *testing.T, kind vibekit.ACPUpdateKind, text, sub string) json.RawMessage {
	t.Helper()
	kiro := map[string]any{"replay": true}
	if sub != "" {
		kiro["kind"] = sub
	}
	u := map[string]any{
		"sessionUpdate": string(kind),
		"_meta":         map[string]any{"kiro": kiro},
	}
	if text != "" {
		u["content"] = map[string]any{"type": "text", "text": text}
		kiro["messageId"] = "id-" + text
		kiro["timestamp"] = "2026-08-02T20:01:00.000Z"
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// settleRecorder captures what a settle handed to the swap seam.
type settleRecorder struct {
	calls     int
	msgs      []vibekit.Message
	watermark string
}

func (r *settleRecorder) sink() func(vibekit.ChatID, []vibekit.Message, string) {
	return func(_ vibekit.ChatID, msgs []vibekit.Message, wm string) {
		r.calls++
		r.msgs = msgs
		r.watermark = wm
	}
}

// feedOneTurn ingests a complete bracketed turn.
func feedOneTurn(t *testing.T, rp *replay, chatID vibekit.ChatID) {
	t.Helper()
	for _, f := range []struct {
		kind vibekit.ACPUpdateKind
		text string
		sub  string
	}{
		{"user_message_chunk", "ONE", ""},
		{vibekit.ACPUpdateSessionInfo, "", "turn_start"},
		{vibekit.ACPUpdateAgentChunk, "reply", ""},
		{vibekit.ACPUpdateSessionInfo, "", "turn_end"},
	} {
		if !rp.ingestReplayFrame(chatID, f.kind, replayUpdate(t, f.kind, f.text, f.sub)) {
			t.Fatalf("frame %v/%s was not consumed by a projection", f.kind, f.sub)
		}
	}
}

// The positions these tests drive. feedOneTurn ingests four frames and every one
// of them precedes the load result on the wire, so a load that answered after them
// answers at position 4 — and the attachment is whichever generation the forward
// goroutine took (attachForward's first is 1).
const (
	testFwdGen  uint64 = 1
	testLoadSeq uint64 = 4
)

// atFrame is the observation Forward reports after folding the frame at seq.
func atFrame(seq uint64) drainPoint { return drainPoint{gen: testFwdGen, seq: seq} }

// atLoad is the position the session/load response arrived at.
func atLoad() drainPoint { return drainPoint{gen: testFwdGen, seq: testLoadSeq} }

// atExit is the bridge-exit seal: an attachment, and no position, because no frame
// can advance one again.
func atExit() drainPoint { return drainPoint{gen: testFwdGen} }

// TestReplayProjection_SettleBarrier is the test that matters here: the settle
// condition is a RACE GUARD, and each half of it has to be load-bearing.
//
// session/load is issued inside bridge.Start, which blocks on the result, while
// the replay frames arrive on the Forward goroutine. The frames precede the
// result on the wire, so when Start returns they are all PUSHED — but notifCh is
// buffered (256), so Forward may not have FOLDED them. Settling on the load's
// return alone would adopt a partial transcript.
func TestReplayProjection_SettleBarrier(t *testing.T) {
	const chatID vibekit.ChatID = "c1"

	t.Run("no settle before the load returns", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)

		rp.SettleReplayProjection(chatID, atLoad(), false)
		if rec.calls != 0 {
			t.Errorf("settled %d times before the load returned, want 0", rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Error("projection was dropped before the load returned")
		}
	})

	t.Run("no settle while the consumer is behind the load position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// The consumer has folded up to the frame BEFORE the result: undrained
		// replay, whatever else is or is not queued behind it.
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq-1), false)
		if rec.calls != 0 {
			t.Errorf("settled %d times one frame short of the load position, want 0", rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Error("projection was dropped while the consumer was still behind")
		}
	})

	t.Run("settles once the consumer reaches the load position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		if rec.calls != 1 {
			t.Fatalf("settled %d times, want exactly 1", rec.calls)
		}
		if len(rec.msgs) != 2 {
			t.Errorf("projected %d messages, want 2 (user + assistant)", len(rec.msgs))
		}
		if rp.hasProjection(chatID) {
			t.Error("projection outlived its settle")
		}
	})

	t.Run("a frame PAST the load position does not delay the settle", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// A post-result catalog frame carries a HIGHER position, so reaching it
		// satisfies the condition rather than resetting it — which is what the old
		// channel-depth observation could not express: that frame kept the channel
		// non-empty and held the settle back.
		rp.SettleReplayProjection(chatID, atFrame(testLoadSeq+3), false)
		if rec.calls != 1 {
			t.Errorf("settled %d times past the load position, want 1", rec.calls)
		}
	})

	t.Run("settle is idempotent", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// Forward calls this after EVERY frame, so a second call with the same
		// condition must not re-swap a transcript.
		for range 4 {
			rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		}
		if rec.calls != 1 {
			t.Errorf("settled %d times, want 1: Forward calls settle per frame", rec.calls)
		}
	})

	t.Run("the seal settles despite an unreached position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// The bridge-exit call: no further frame can arrive to re-trigger the
		// check, so the projection must complete rather than leak.
		rp.SettleReplayProjection(chatID, atExit(), true)
		if rec.calls != 1 {
			t.Errorf("sealed settle ran %d times, want 1", rec.calls)
		}
	})

	t.Run("the seal still requires the load to have returned", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)

		// A bridge that died before session/load returned has no transcript to
		// adopt; sealing must not manufacture one from a partial replay.
		rp.SettleReplayProjection(chatID, atExit(), true)
		if rec.calls != 0 {
			t.Errorf("sealed settle ran %d times on a load that never returned, want 0", rec.calls)
		}
	})

	t.Run("a straggler from a previous attachment settles nothing", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		// This chat's load ran on attachment 2, the model-switch reload's forward.
		const reload = testFwdGen + 1
		rp.MarkReplayLoadedAt(chatID, drainPoint{gen: reload, seq: testLoadSeq})

		// The PREVIOUS bridge's forward is still draining its closed channel, and
		// its positions run far ahead — a whole session's frames against a fresh
		// load's three. Adopting them would settle this replay on frame one.
		rp.SettleReplayProjection(chatID, drainPoint{gen: testFwdGen, seq: 900}, false)
		if rec.calls != 0 {
			t.Errorf("a straggling observation from attachment %d settled the replay "+
				"loaded on attachment %d %d times, want 0", testFwdGen, reload, rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Fatal("the straggler dropped the projection")
		}

		// And it must not have been ADOPTED either, which refusing to settle on it
		// does not prove: the live attachment's own first frame is still one frame
		// in, so a stored 900 would settle the replay here on a partial transcript.
		rp.SettleReplayProjection(chatID, drainPoint{gen: reload, seq: 1}, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times on the live attachment's FIRST frame, want 0 — "+
				"the straggler's position was adopted", rec.calls)
		}

		// Its own attachment reaching the load position still settles it.
		rp.SettleReplayProjection(chatID, drainPoint{gen: reload, seq: testLoadSeq}, false)
		if rec.calls != 1 {
			t.Errorf("settled %d times once its own attachment caught up, want 1", rec.calls)
		}
	})

	t.Run("a NEW attachment invalidates the load position", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadedAt(chatID, atLoad())

		// A second bridge attached, so the frames the load bounded are queued on a
		// channel nobody will drain further and its sequence restarts at zero. The
		// low positions the new attachment reports must not satisfy a bound
		// measured against the old one.
		rp.SettleReplayProjection(chatID, drainPoint{gen: testFwdGen + 1, seq: 1}, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times on a fresh attachment's first frame, want 0", rec.calls)
		}
	})
}

// TestReplayProjection_ADrainedReplaySettlesWhenTheLoadReturns: a replay whose frames all
// drained BEFORE the RPC returned has nothing left to notice it — no frame is coming, and a
// caller cannot wait for the bridge to die. With the settle running only from Forward (per
// frame consumed, and once at bridge exit) such a transcript sat fully built in the map while
// AwaitReplayAdopted spent its whole 45s budget and refused the rewind.
func TestReplayProjection_ADrainedReplaySettlesWhenTheLoadReturns(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	rp, rec := replayWithRecorder()
	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID)

	// Forward folded every replayed frame first, which is the ordinary case for a
	// short transcript: the drain finishes while the RPC is still in flight.
	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
	if rec.calls != 0 {
		t.Fatalf("settled %d times before the load returned, want 0", rec.calls)
	}

	rp.MarkReplayLoadedAt(chatID, atLoad())

	if rec.calls != 1 {
		t.Fatalf("recording the load position settled %d times, want 1 — a replay "+
			"already folded has no frame left to trigger it and no caller can wait "+
			"for the bridge to die", rec.calls)
	}
	if len(rec.msgs) != 2 {
		t.Errorf("projected %d messages, want 2 (user + assistant)", len(rec.msgs))
	}
	if !barrierClosed(rp.ReplaySettled(chatID)) {
		t.Error("the barrier is still open after the load returned on a drained replay")
	}
}

// TestReplayProjection_DiscardOnFailedLoad pins that a failed session/load
// leaves nothing behind. tryLoadSession falls through to session/new on
// failure, and a surviving projection would let that fresh session adopt the
// dead one's partial transcript.
func TestReplayProjection_DiscardOnFailedLoad(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	rp, rec := replayWithRecorder()
	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID)

	rp.DiscardReplayProjection(chatID)
	if rp.hasProjection(chatID) {
		t.Error("projection survived a discard")
	}
	// Even the settle condition holding afterwards must not resurrect it.
	rp.MarkReplayLoadedAt(chatID, atLoad())
	rp.SettleReplayProjection(chatID, atExit(), true)
	if rec.calls != 0 {
		t.Errorf("discarded projection settled %d times, want 0", rec.calls)
	}
}

// TestReplayProjection_FrameWithNoLoadIsRejected pins the fallback that keeps
// agent.handleSessionUpdate's drop path meaningful: a replay frame arriving with
// no load in flight has no transcript to belong to.
func TestReplayProjection_FrameWithNoLoadIsRejected(t *testing.T) {
	rp, _ := replayWithRecorder()
	if rp.ingestReplayFrame("nobody", vibekit.ACPUpdateAgentChunk,
		replayUpdate(t, vibekit.ACPUpdateAgentChunk, "stray", "")) {
		t.Error("a replay frame was consumed with no projection open")
	}
}

// TestReplayProjection_ReloadSupersedes pins that a second load for the same
// chat (the model-switch fallback path) starts clean rather than appending to
// the first load's half-built transcript.
func TestReplayProjection_ReloadSupersedes(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	rp, rec := replayWithRecorder()

	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID)
	rp.OpenReplayProjection(chatID) // re-load
	feedOneTurn(t, rp, chatID)
	rp.MarkReplayLoadedAt(chatID, atLoad())
	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)

	if rec.calls != 1 {
		t.Fatalf("settled %d times, want 1", rec.calls)
	}
	if len(rec.msgs) != 2 {
		var shape []string
		for _, m := range rec.msgs {
			shape = append(shape, fmt.Sprintf("%s:%q", m.Role, m.Content))
		}
		t.Errorf("projected %d messages, want 2 — the first load's frames leaked in: %v",
			len(rec.msgs), shape)
	}
}

// replayWithRecorder builds the minimum these tests need, which is now a bare
// replay rather than a Runtime: the projection lifecycle touches only that type's
// own three fields, so there is no bridge, no store and no goroutine to stand up.
// It was a &Runtime{} when the six methods hung off the runtime and reached an
// embedded projectionState.
func replayWithRecorder() (*replay, *settleRecorder) {
	rec := &settleRecorder{}
	rp := &replay{projections: map[vibekit.ChatID]*loadProjection{}}
	rp.onProjection = rec.sink()
	return rp, rec
}

// hasProjection reports whether a projection is open. Test-only, and defined
// here rather than in production so it adds no exported surface.
func (rp *replay) hasProjection(chatID vibekit.ChatID) bool {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	_, ok := rp.projections[chatID]
	return ok
}

// barrierClosed reports whether the barrier has released, without waiting.
func barrierClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// The barrier has to span the SWAP, not just the wait before it. The swap is
// where the damage happens — it writes the projection's messages over the record
// — so a waiter released when the projection leaves the map proceeds to rewrite
// a transcript the swap is about to overwrite anyway, which is the whole race
// the barrier exists to close. The window is one chat-file write, not an
// instant, so a caller lands in it.
func TestReplayProjection_BarrierSpansTheSwap(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	rp := &replay{projections: map[vibekit.ChatID]*loadProjection{}}
	releasedDuringSwap := false
	rp.onProjection = func(vibekit.ChatID, []vibekit.Message, string) {
		releasedDuringSwap = barrierClosed(rp.ReplaySettled(chatID))
	}
	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID)
	rp.MarkReplayLoadedAt(chatID, atLoad())

	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)

	if releasedDuringSwap {
		t.Error("the barrier reported adopted while the swap was still writing the record")
	}
	if !barrierClosed(rp.ReplaySettled(chatID)) {
		t.Error("the barrier never released after the swap returned")
	}
}

// TWO swaps can be in flight for one chat, because a model-switch reload attaches
// a second Forward goroutine while the first is still draining. Both register
// their barrier under the same chat key, so the first swap's cleanup must delete
// only its OWN — deleting whatever sits under the key takes the superseder's
// barrier with it, and a waiter then reads adopted while a live replay is still
// writing the record.
func TestReplayProjection_ASwapDoesNotHideASupersedersBarrier(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	rp := &replay{projections: map[vibekit.ChatID]*loadProjection{}}

	// Park each swap on entry so the test decides the interleaving rather than
	// racing it. Index 0 is the original load's swap, 1 the superseder's.
	var mu sync.Mutex
	nth := 0
	entered := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	rp.onProjection = func(vibekit.ChatID, []vibekit.Message, string) {
		mu.Lock()
		n := nth
		nth++
		mu.Unlock()
		close(entered[n])
		<-release[n]
	}

	settle := func() <-chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)
		}()
		return done
	}

	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID)
	rp.MarkReplayLoadedAt(chatID, atLoad())
	firstDone := settle()
	<-entered[0]

	// The reload, opened and settled while the first swap is still parked.
	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID)
	rp.MarkReplayLoadedAt(chatID, atLoad())
	secondDone := settle()
	<-entered[1]

	// Let ONLY the first swap finish, so its cleanup runs while the superseder's
	// is still in flight.
	close(release[0])
	<-firstDone

	if barrierClosed(rp.ReplaySettled(chatID)) {
		t.Error("the barrier reads adopted while the superseding replay is still swapping")
	}

	close(release[1])
	<-secondDone
	if !barrierClosed(rp.ReplaySettled(chatID)) {
		t.Error("the barrier never released after both swaps returned")
	}
}

// TestMergeProjection covers the rule that lets the replay become the
// transcript without losing what a replay cannot speak for.
func TestMergeProjection(t *testing.T) {
	msg := func(id string, role vibekit.Role, ts int64, content string) vibekit.Message {
		return vibekit.Message{ID: id, Role: role, Ts: ts, Content: content}
	}
	event := func(id string, ts int64, kind vibekit.EventKind) vibekit.Message {
		return vibekit.Message{ID: id, Role: vibekit.RoleEvent, EventKind: kind, Ts: ts}
	}
	ids := func(ms []vibekit.Message) []string {
		out := make([]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}

	t.Run("an empty projection never clobbers the record", func(t *testing.T) {
		existing := []vibekit.Message{msg("u1", vibekit.RoleUser, 100, "hi")}
		got, _, _ := mergeProjection(existing, nil)
		if len(got) != 1 || got[0].ID != "u1" {
			t.Errorf("got %v, want the existing record preserved", ids(got))
		}
	})

	t.Run("assistant turns are superseded, not duplicated", func(t *testing.T) {
		// The tell: vibekit's assistant id and the wire's never match, so a
		// merge keyed on ids would keep both copies of every turn.
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("m-vibekit-generated", vibekit.RoleAssistant, 200, "hello"),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		if len(got) != 2 {
			t.Errorf("got %d messages %v, want 2: the assistant turn was duplicated", len(got), ids(got))
		}
		for _, m := range got {
			if m.ID == "m-vibekit-generated" {
				t.Error("the superseded assistant message survived")
			}
		}
	})

	t.Run("event messages survive, since the wire has none", func(t *testing.T) {
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			event("e1", 150, vibekit.EventModelSwitched),
			msg("m-old", vibekit.RoleAssistant, 200, "hello"),
			event("e2", 250, vibekit.EventCancelled),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "e1", "abc-say", "e2"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v (events preserved in timestamp order)", ids(got), want)
		}
	})

	t.Run("the un-replayed tail survives", func(t *testing.T) {
		// KAS's log is not fsynced, so a turn vibekit durably holds can be
		// missing from the replay. Dropping it would make the projection worse
		// than the durability stack it replaces.
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("m-old", vibekit.RoleAssistant, 200, "hello"),
			msg("u2", vibekit.RoleUser, 300, "and this"),
			msg("m-tail", vibekit.RoleAssistant, 400, "recovered mid-turn"),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "abc-say", "u2", "m-tail"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v (everything after the projection's window kept)", ids(got), want)
		}
	})

	t.Run("a projected message wins a timestamp tie", func(t *testing.T) {
		existing := []vibekit.Message{event("e1", 200, vibekit.EventCancelled)}
		projected := []vibekit.Message{msg("abc-say", vibekit.RoleAssistant, 200, "hello")}
		got, _, _ := mergeProjection(existing, projected)
		if len(got) != 2 || got[0].ID != "abc-say" {
			t.Errorf("got %v, want the projected message first at an equal timestamp", ids(got))
		}
	})

	// A plan row is RoleAssistant, so role alone dropped it — and the ACP plan
	// frame is not on the replay wire, so nothing regenerates one. Before this
	// case every resumed chat lost its plan cards for good.
	t.Run("a plan row survives, since the wire has none either", func(t *testing.T) {
		plan := vibekit.Message{
			ID:   "m-plan",
			Role: vibekit.RoleAssistant,
			Ts:   150,
			Plan: []vibekit.PlanEntry{{Content: "step one", Status: "pending"}},
		}
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			plan,
			msg("m-old", vibekit.RoleAssistant, 200, "hello"),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "m-plan", "abc-say"}
		if !slices.Equal(ids(got), want) {
			t.Fatalf("got %v, want %v (the plan row preserved in timestamp order)", ids(got), want)
		}
		if len(got[1].Plan) != 1 {
			t.Errorf("the surviving plan row carries %d entries, want 1", len(got[1].Plan))
		}
	})

	// A steer's DELIVERY STATE is a fact a replay cannot speak for: KAS's log
	// records the steer without saying whether the model consumed it, so the
	// projected row carries none. The two rows share an id (both are KAS's own
	// `steer-` id), so the projected copy supersedes — and without carrying the
	// state across, a resume silently turned "the agent never read this" into a
	// note that claims it landed.
	t.Run("a steer's delivery state survives the swap", func(t *testing.T) {
		steer := vibekit.Message{
			ID:          "steer-1",
			Role:        vibekit.RoleUser,
			Ts:          150,
			Content:     "actually target main",
			UserKind:    vibekit.UserKindSteer,
			SteerState:  vibekit.SteerStateDropped,
			SteerOrigin: vibekit.SteerOriginUser,
		}
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			steer,
			msg("m-old", vibekit.RoleAssistant, 200, "hello"),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			// What the replay projection produces: the row, no state, no origin.
			{
				ID:       "steer-1",
				Role:     vibekit.RoleUser,
				Ts:       150,
				Content:  "actually target main",
				UserKind: vibekit.UserKindSteer,
			},
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "steer-1", "abc-say"}
		if !slices.Equal(ids(got), want) {
			t.Fatalf("got %v, want %v — one row per steer, whichever copy wins", ids(got), want)
		}
		if got[1].SteerState != vibekit.SteerStateDropped {
			t.Errorf("SteerState = %q, want %q", got[1].SteerState, vibekit.SteerStateDropped)
		}
		if got[1].SteerOrigin != vibekit.SteerOriginUser {
			t.Errorf("SteerOrigin = %q, want %q", got[1].SteerOrigin, vibekit.SteerOriginUser)
		}
	})

	// The other half of the shape rule: a real reply is superseded even when it
	// happens to carry a plan, or the projection's copy and this one both render.
	t.Run("an assistant turn carrying a plan is still superseded", func(t *testing.T) {
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			{
				ID:      "m-old",
				Role:    vibekit.RoleAssistant,
				Ts:      200,
				Content: "hello",
				Plan:    []vibekit.PlanEntry{{Content: "step one", Status: "pending"}},
			},
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "abc-say"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v (a reply with content is the wire's, plan or not)", ids(got), want)
		}
	})

	// The LIVE compaction event against its projected twin — the half a derived id
	// cannot reach, because vibekit minted the live one as a uuid at compaction time.
	// Measured on the live volume: five pairs of `compacted` rows in one chat with
	// byte-identical content lengths, so the reader saw the same 12-16 KB summary twice.
	t.Run("a compaction the replay also produced is not kept twice", func(t *testing.T) {
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("m-old", vibekit.RoleAssistant, 200, "hello"),
			// vibekit's own uuid, minted when the compaction happened.
			event("01a07bf0-fa5e-7000-8000-000000000000", 200, vibekit.EventCompacted),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
			// The projection's derived id for the same boundary.
			event("abc-say-compacted", 200, vibekit.EventCompacted),
		}
		got, _, _ := mergeProjection(existing, projected)
		var compactions int
		for i := range got {
			if got[i].EventKind == vibekit.EventCompacted {
				compactions++
			}
		}
		if compactions != 1 {
			t.Errorf("got %d compaction rows %v, want exactly 1", compactions, ids(got))
		}
	})

	// The exclusion must stay NARROW: a compaction NEWER than the replay is the
	// un-fsynced-KAS-log case, and dropping it would lose a boundary vibekit durably
	// holds and nothing regenerates.
	t.Run("a compaction newer than the replay still survives", func(t *testing.T) {
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			event("e-recent", 900, vibekit.EventCompacted),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			event("abc-say-compacted", 200, vibekit.EventCompacted),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "abc-say-compacted", "e-recent"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v", ids(got), want)
		}
	})

	// And a compaction the replay did NOT produce is preserved like any other event
	// row, which is what keeps the exclusion conditional rather than a rule about kind.
	t.Run("a compaction the replay did not produce is preserved", func(t *testing.T) {
		existing := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			event("e-only-ours", 150, vibekit.EventCompacted),
		}
		projected := []vibekit.Message{
			msg("u1", vibekit.RoleUser, 100, "hi"),
			msg("abc-say", vibekit.RoleAssistant, 200, "hello"),
		}
		got, _, _ := mergeProjection(existing, projected)
		want := []string{"u1", "e-only-ours", "abc-say"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v", ids(got), want)
		}
	})
}

// replayNotif wraps a replay-tagged update in the session/update notification a
// bridge actually delivers, so a test can put the wire's own frames on notifCh.
func replayNotif(t *testing.T, kind vibekit.ACPUpdateKind, text, sub string) *vibekit.RPCResponse {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"sessionId": "old-acp",
		"update":    replayUpdate(t, kind, text, sub),
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: raw}
}

// loadedChat seeds a chat carrying an ACP session id, which is what sends its
// next spawn down the session/load path rather than session/new.
func loadedChat(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("old-acp")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
}

// awaitPatience is a test-owned patience bound, not a production budget: nothing here
// asserts how PROMPTLY the transcript is adopted, only that it is. Do NOT widen it to fix a
// failure — the settle deletes its projection, so a settle that fires EARLY drops every later
// frame and the transcript can never arrive, which no bound can outwait. The message below
// dumps what the record holds: a one-message transcript is the tell for that bug, nothing at
// all is the tell for a genuinely stuck Forward.
const awaitPatience = 20 * time.Second

func awaitReplayedTurn(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID, want string) {
	t.Helper()
	stop := time.Now().Add(awaitPatience)
	for {
		c, ok := cs.Get(t.Context(), chatID)
		if ok {
			for i := range c.Messages {
				if c.Messages[i].Content == want {
					return
				}
			}
		}
		if time.Now().After(stop) {
			var got []string
			if ok {
				for i := range c.Messages {
					got = append(got, fmt.Sprintf("%s:%q", c.Messages[i].Role, c.Messages[i].Content))
				}
			}
			t.Fatalf("the replayed turn %q never reached the chat's transcript within %v; a "+
				"resumed chat shows an empty history instead of the conversation KAS replayed. "+
				"The record holds %d message(s): %v (a short transcript means a projection "+
				"settled before the replay finished and the rest was dropped)",
				want, awaitPatience, len(got), got)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSessionLoad_AdoptsTheReplayedTranscript is the load path end to end. Both halves of
// the settle condition are wired here rather than asserted separately, because either one
// missing produces the same user-visible failure — a resumed chat whose history is gone. The
// projection must be OPEN before Forward attaches, or the frames arrive with nowhere to land,
// and the load's return must be recorded, or no settle path completes.
func TestSessionLoad_AdoptsTheReplayedTranscript(t *testing.T) {
	// A fresh bridge per spawn, because the utility bridge the rehydrate sweep
	// starts would otherwise share this one's notification channel and drain the
	// replay out from under the chat's own Forward loop. Each one carries the
	// transcript, and only the one doing a session/load replays it.
	cs := newFakeChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		b := newFakeBridge()
		b.notifsOnStart = []*vibekit.RPCResponse{
			replayNotif(t, "user_message_chunk", "ONE", ""),
			replayNotif(t, vibekit.ACPUpdateSessionInfo, "", "turn_start"),
			replayNotif(t, vibekit.ACPUpdateAgentChunk, "reply", ""),
			replayNotif(t, vibekit.ACPUpdateSessionInfo, "", "turn_end"),
		}
		return b
	}, cs)
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	const chatID vibekit.ChatID = "c1"
	loadedChat(t, cs, chatID)

	// The replay is delivered inside this call, the way KAS delivers it inside
	// session/load, so by the time the load result is recorded every frame is
	// already in the channel. Pushing them afterwards instead is what let the
	// barrier settle on a one-frame transcript.
	sb, err := h.coord.OpenBridge(t.Context(), chatID, "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br, ok := sb.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("the chat's bridge is %T, want the fake", sb.bridge)
	}
	if opts := br.lastStartOpts(); opts == nil || opts.SessionID == "" {
		// Without a named session the fake replays nothing, so every assertion
		// below would pass or fail for a reason that has nothing to do with the
		// settle. Fail as invalid rather than reporting on an empty replay.
		t.Fatalf("the chat's bridge was started with StartOpts %+v, want one naming "+
			"the stored ACP session so the fake replays a transcript", opts)
	}

	// The bridge exiting is the backstop settle, so completion no longer depends
	// on which side of the race drained the last frame.
	br.Stop()

	awaitReplayedTurn(t, cs, chatID, "reply")
}

// TestForward_ReportsEachFramesOwnPosition pins the number Forward hands the settle, the
// frame's OWN Seq off the wire. Every other chat-route case can settle by another door, so
// none notices Forward reporting a constant; this one closes both — the load position is
// recorded BEFORE any frame is folded so no post-load attempt can complete it, and the
// bridge is never stopped so no seal is coming.
func TestForward_ReportsEachFramesOwnPosition(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID vibekit.ChatID = "c1"
	loadedChat(t, cs, chatID)

	frames := []*vibekit.RPCResponse{
		replayNotif(t, "user_message_chunk", "ONE", ""),
		replayNotif(t, vibekit.ACPUpdateSessionInfo, "", "turn_start"),
		replayNotif(t, vibekit.ACPUpdateAgentChunk, "reply", ""),
		replayNotif(t, vibekit.ACPUpdateSessionInfo, "", "turn_end"),
	}

	h.replay.OpenReplayProjection(chatID)
	gen := h.coord.turns.attachForward(chatID)
	go h.coord.forwardAt(chatID, br, gen)

	// The load answered at the position of the last replayed frame, and nothing has
	// been folded yet — the ordering a consumer behind its channel produces.
	h.replay.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: uint64(len(frames))})
	if barrierClosed(h.replay.ReplaySettled(chatID)) {
		t.Fatal("the replay settled with nothing folded, so this test cannot tell " +
			"a per-frame settle from an unconditional one")
	}

	for _, f := range frames {
		br.deliver(f)
	}

	awaitReplayedTurn(t, cs, chatID, "reply")
	if br.isStopped() {
		t.Error("the bridge was stopped, so the seal could have settled this instead " +
			"of the frames' own positions")
	}
}

// TestForwardExit_SettlesALoadWhoseTrailingFramesNeverCame is the backstop neither the
// frames nor the load's own settle attempt can provide: a bridge that dies with the consumer
// short of the load's position leaves a projection whose condition can never hold again — no
// frame will advance it, and the reader's own attempt already ran and found it short. Without
// the seal at Forward's exit the chat resumes empty and the rebuild leaks for the process.
func TestForwardExit_SettlesALoadWhoseTrailingFramesNeverCame(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID vibekit.ChatID = "c1"
	loadedChat(t, cs, chatID)

	// Folded into the projection without ever being CONSUMED off a channel, which
	// is what leaves the position short: the load names a bound nothing will reach.
	h.replay.OpenReplayProjection(chatID)
	feedOneTurn(t, h.replay, chatID)
	h.replay.MarkReplayLoadedAt(chatID, atLoad())

	br.Stop() // the bridge exits with nothing further to deliver
	h.coord.Forward(chatID, br)

	awaitReplayedTurn(t, cs, chatID, "reply")
	// And the rebuild is released rather than left open forever.
	if h.replay.ingestReplayFrame(chatID, vibekit.ACPUpdateAgentChunk,
		replayUpdate(t, vibekit.ACPUpdateAgentChunk, "late", "")) {
		t.Error("a projection was still open after the bridge exited, so every later " +
			"replay frame folds into a transcript nothing will settle")
	}
}

// TestReplayProjection_ConcurrentLoadsAreIndependent pins that the rebuilds are keyed per
// chat. Two chats loading at once is ordinary: a restart with several tabs open respawns a
// bridge per chat as each is touched. If opening the second disturbed the map holding the
// first, the earlier chat's replay would be discarded mid-flight and it would resume with an
// empty history and no error, because a dropped projection looks like a chat that never
// loaded.
func TestReplayProjection_ConcurrentLoadsAreIndependent(t *testing.T) {
	rp, rec := replayWithRecorder()
	const first vibekit.ChatID = "c1"
	const second vibekit.ChatID = "c2"

	rp.OpenReplayProjection(first)
	feedOneTurn(t, rp, first)
	rp.MarkReplayLoadedAt(first, atLoad())

	// The second chat's spawn happens while the first is still in flight.
	rp.OpenReplayProjection(second)
	if !rp.hasProjection(first) {
		t.Fatal("opening a second chat's load dropped the first chat's rebuild, so that " +
			"chat resumes with an empty transcript")
	}
	feedOneTurn(t, rp, second)

	rp.SettleReplayProjection(first, atFrame(testLoadSeq), false)
	if rec.calls != 1 {
		t.Fatalf("the first chat settled %d times, want 1", rec.calls)
	}
	if len(rec.msgs) != 2 {
		t.Errorf("the first chat projected %d messages, want 2 (user + assistant)", len(rec.msgs))
	}
	if !rp.hasProjection(second) {
		t.Error("settling the first chat dropped the second chat's rebuild")
	}
}

// TestReplayProjection_SettleReportsFramesAgainstMessages pins the one diagnostic
// a settle leaves behind.
//
// The pair of counts is the whole point: many frames folding into zero messages is
// a decoding bug, and nothing else in the process would say so — the transcript
// simply comes back empty and the user reads that as a lost conversation. So the
// frame tally has to track the frames actually ingested rather than merely being
// present.
func TestReplayProjection_SettleReportsFramesAgainstMessages(t *testing.T) {
	logs := captureLogs(t)
	rp, _ := replayWithRecorder()
	const chatID vibekit.ChatID = "c1"

	rp.OpenReplayProjection(chatID)
	feedOneTurn(t, rp, chatID) // four frames: user, turn_start, reply, turn_end
	rp.MarkReplayLoadedAt(chatID, atLoad())
	rp.SettleReplayProjection(chatID, atFrame(testLoadSeq), false)

	out := logs.String()
	if !strings.Contains(out, `"msg":"replay projection settled"`) {
		t.Fatalf("a completed settle said nothing: %s", out)
	}
	if !strings.Contains(out, `"frames":4`) {
		t.Errorf("the settle line does not report the 4 frames it ingested: %s", out)
	}
	if !strings.Contains(out, `"messages":2`) {
		t.Errorf("the settle line does not report the 2 messages it projected: %s", out)
	}
}

// TestSwapProjectedTranscript_WritesOnlyWhatTheRecordDoesNotAlreadyHold covers both
// directions of the no-op guard, each of which is the other's failure mode. A resume that
// rebuilds exactly the stored transcript must not rewrite it — every write broadcasts a
// chat_updated, and a reconnect storm after a restart would push one per chat for no change.
// But the watermark is independent state: a KAS-side compaction moves it without changing the
// message count, and dropping that update leaves vibekit compacting from a stale point.
func TestSwapProjectedTranscript_WritesOnlyWhatTheRecordDoesNotAlreadyHold(t *testing.T) {
	seed := func(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID, watermark string) []vibekit.Message {
		t.Helper()
		msgs := []vibekit.Message{
			{ID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "hi"},
			{ID: "abc-say", Role: vibekit.RoleAssistant, Ts: 200, Content: "hello"},
		}
		if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
			c.Name = "A"
			c.Messages = msgs
			c.CompactionWatermark = watermark
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		return msgs
	}

	t.Run("an identical rebuild is not written back", func(t *testing.T) {
		h, cs, _ := newTestHub()
		const chatID vibekit.ChatID = "c1"
		msgs := seed(t, cs, chatID, "wm-1")
		before := bufferedSince(h, 0)

		h.replay.swapProjectedTranscript(chatID, msgs, "wm-1")

		got := extractTypes(t, bufferedSince(h, before[len(before)-1].Offset))
		if slices.Contains(got, "chat_updated") {
			t.Errorf("a replay that changed nothing rewrote the chat and broadcast %v; every "+
				"resumed tab would push an update for a transcript nobody edited", got)
		}
	})

	t.Run("a moved watermark is written even when the messages match", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, _ := newTestHub()
		const chatID vibekit.ChatID = "c1"
		msgs := seed(t, cs, chatID, "wm-1")

		h.replay.swapProjectedTranscript(chatID, msgs, "wm-2")

		chat, ok := cs.Get(t.Context(), chatID)
		if !ok {
			t.Fatal("the chat is gone after a swap")
		}
		if chat.CompactionWatermark != "wm-2" {
			t.Errorf("watermark = %q, want %q; the replay's compaction point was dropped, so "+
				"vibekit keeps compacting from a window KAS has already moved past",
				chat.CompactionWatermark, "wm-2")
		}
		// The swap reports itself, and reports nothing about failing.
		out := logs.String()
		if !strings.Contains(out, `"msg":"replay projection: transcript swapped"`) {
			t.Errorf("a completed swap said nothing: %s", out)
		}
		if strings.Contains(out, `"msg":"replay projection: swap failed"`) {
			t.Errorf("a swap that worked reported a failure: %s", out)
		}
	})
}

// swapTestProjection is the two-message transcript the announce tests swap in. Its
// ids differ from loadedChat's seed (which holds none), so the merge changes the set.
func swapTestProjection() []vibekit.Message {
	return []vibekit.Message{
		{ID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "resume"},
		{ID: "abc-say", Role: vibekit.RoleAssistant, Ts: 200, Content: "resumed"},
	}
}

// eventFor returns the first buffered event of the given type, decoded.
func eventFor(t *testing.T, events []sse.ReplayEvent, want vibekit.EventType) (vibekit.ServerEvent, bool) {
	t.Helper()
	for _, e := range events {
		var msg vibekit.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == want {
			return msg, true
		}
	}
	return vibekit.ServerEvent{}, false
}

// TestSwapProjectedTranscript_AnnouncesTheReplacement covers the one thing no other
// frame on this wire can state: that a chat's transcript was REPLACED rather than
// appended to. A header carries a count, and a count cannot tell a fill from a swap —
// so a client holding a window it believes complete has nothing to refetch on. The
// instruction is a subject_changed stamped `chat:<id>` at the version the swap's own
// write minted, so the refetch it triggers commits at exactly that version.
//
// The ORDER is asserted as well as the presence: the header has to reach the client
// before the fetch instruction, or the instruction lands against a count of zero.
func TestSwapProjectedTranscript_AnnouncesTheReplacement(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID vibekit.ChatID = "c1"
	// The tangent's own shape: a name and a session id, no messages.
	loadedChat(t, cs, chatID)
	before := bufferedSince(h, 0)
	if len(before) == 0 {
		t.Fatal("seeding the chat broadcast nothing, so there is no id to measure from")
	}

	h.replay.swapProjectedTranscript(chatID, swapTestProjection(), "wm-1")

	events := bufferedSince(h, before[len(before)-1].Offset)
	got := extractTypes(t, events)
	if !slices.Contains(got, string(vibekit.EventSubjectChanged)) {
		t.Fatalf("the swap broadcast %v and never told the client to refetch; a client whose "+
			"window is already marked loaded has nothing to refetch on", got)
	}
	if hdr, repl := slices.Index(got, string(vibekit.EventChatUpdated)),
		slices.Index(got, string(vibekit.EventSubjectChanged)); hdr > repl {
		t.Errorf("frames arrived %v; the header must precede the fetch instruction, or the client "+
			"refetches against a message count it has not been told about yet", got)
	}
	ev, ok := eventFor(t, events, vibekit.EventSubjectChanged)
	if !ok {
		t.Fatal("the fetch instruction vanished between two reads of the same buffer")
	}
	if ev.ChatID != chatID {
		t.Errorf("the instruction names chat %q, want %q; a workspace-global frame reaches "+
			"no chat's handler", ev.ChatID, chatID)
	}
	if ev.Subject == nil {
		t.Fatal("the fetch instruction carries no subject stamp, so the client's version map " +
			"cannot record which version the refetch commits at")
	}
	if ev.Subject.Kind != string(subject.KindChat) || ev.Subject.Ref != string(chatID) {
		t.Errorf("subject = %s:%s, want %s:%s; the stamp names the subject the refetch commits",
			ev.Subject.Kind, ev.Subject.Ref, subject.KindChat, chatID)
	}
	if want := cs.ChatVersion(chatID); ev.Subject.Version == "" || ev.Subject.Version != want {
		t.Errorf("subject version = %q, want %q (the version the swap's own write minted); a "+
			"stale or empty version makes the digest name this chat again after a refetch that "+
			"already landed", ev.Subject.Version, want)
	}
}

// TestSwapProjectedTranscript_AnnouncesNothingWhenTheSetIsUnchanged is the other half
// of the gate, and the half that decides whether the gate is load-bearing at all: an
// unconditional emit would pass the test above and invalidate every reader's window on
// every resume of every chat, which is a full transcript refetch per reconnect.
//
// The watermark case is the sharper one. A KAS-side compaction moves the watermark
// without touching the message set, so the record IS rewritten and a header does go
// out — but nothing was replaced, so there is nothing for a client to refetch.
func TestSwapProjectedTranscript_AnnouncesNothingWhenTheSetIsUnchanged(t *testing.T) {
	seed := func(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID, watermark string) []vibekit.Message {
		t.Helper()
		msgs := swapTestProjection()
		if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
			c.Name = "A"
			c.Messages = msgs
			c.CompactionWatermark = watermark
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		return msgs
	}

	t.Run("an identical rebuild announces nothing", func(t *testing.T) {
		h, cs, _ := newTestHub()
		const chatID vibekit.ChatID = "c1"
		msgs := seed(t, cs, chatID, "wm-1")
		before := bufferedSince(h, 0)
		if len(before) == 0 {
			t.Fatal("seeding the chat broadcast nothing, so there is no id to measure from")
		}

		h.replay.swapProjectedTranscript(chatID, msgs, "wm-1")

		got := extractTypes(t, bufferedSince(h, before[len(before)-1].Offset))
		if slices.Contains(got, string(vibekit.EventSubjectChanged)) {
			t.Errorf("a replay that changed nothing told the client to refetch (%v); every resumed "+
				"tab would refetch its whole transcript for a swap that replaced nothing", got)
		}
	})

	t.Run("a moved watermark alone announces nothing", func(t *testing.T) {
		h, cs, _ := newTestHub()
		const chatID vibekit.ChatID = "c1"
		msgs := seed(t, cs, chatID, "wm-1")
		before := bufferedSince(h, 0)
		if len(before) == 0 {
			t.Fatal("seeding the chat broadcast nothing, so there is no id to measure from")
		}

		h.replay.swapProjectedTranscript(chatID, msgs, "wm-2")

		got := extractTypes(t, bufferedSince(h, before[len(before)-1].Offset))
		if !slices.Contains(got, string(vibekit.EventChatUpdated)) {
			t.Fatalf("the watermark move wrote no record (%v), so this case is not measuring "+
				"what it claims to", got)
		}
		if slices.Contains(got, string(vibekit.EventSubjectChanged)) {
			t.Errorf("a watermark-only move told the client to refetch (%v); the message set is "+
				"unchanged, so there is nothing for a reader to refetch", got)
		}
	})
}

// TestSwapProjectedTranscript_ReplacesASameLengthSet is the gate's precision case, and the
// one a row COUNT cannot express: the merge returns as many rows as the record held and not
// one of the same ones.
//
// Reachable because mergeProjection preserves an existing row only when it is an event, a
// plan, or newer than the projection's newest, so a record of ordinary turns sitting inside
// the replayed window contributes nothing and the projection is the whole result.
func TestSwapProjectedTranscript_ReplacesASameLengthSet(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID vibekit.ChatID = "c1"

	// Two rows the merge cannot preserve: ordinary roles, no plan shape, both timestamps
	// inside the projection's window.
	stale := []vibekit.Message{
		{ID: "old-u1", Role: vibekit.RoleUser, Ts: 100, Content: "resume"},
		{ID: "old-say", Role: vibekit.RoleAssistant, Ts: 200, Content: "resumed"},
	}
	if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = stale
		c.CompactionWatermark = "wm-1"
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	before := bufferedSince(h, 0)
	if len(before) == 0 {
		t.Fatal("seeding the chat broadcast nothing, so there is no id to measure from")
	}

	// Same count, same timestamps, different ids, and the SAME watermark — so the
	// watermark arm cannot be what carries the write.
	h.replay.swapProjectedTranscript(chatID, swapTestProjection(), "wm-1")

	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatal("the chat vanished")
	}
	if len(c.Messages) != len(stale) {
		t.Fatalf("the record holds %d rows, want %d: this case measures what it claims only "+
			"while the two sets are the same length", len(c.Messages), len(stale))
	}
	if c.Messages[0].ID == stale[0].ID {
		t.Errorf("the record still holds %q, so the swap wrote nothing; a transcript KAS has "+
			"already replaced outlives the load that replaced it", c.Messages[0].ID)
	}

	got := extractTypes(t, bufferedSince(h, before[len(before)-1].Offset))
	if !slices.Contains(got, string(vibekit.EventSubjectChanged)) {
		t.Errorf("a swap that replaced every row announced %v; the row count is unchanged, so "+
			"a reader told only the count refetches nothing", got)
	}
}

// TestSwapProjectedTranscript_WritesOnACancelledLifetime is the durable-write
// class's fourth instance, and the only one that discards a whole TRANSCRIPT.
//
// The swap runs on the settle rather than the frame that triggered it and takes
// the lifetime's own context, so a shutdown landing between the two refused a
// transcript already merged in memory. It needs the REAL store: the recording
// fake ignores its context, so the assertion holds against it either way.
func TestSwapProjectedTranscript_WritesOnACancelledLifetime(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	h, cs := hubOnDisk(t, chatID)
	projected := []vibekit.Message{
		{ID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "resume"},
		{ID: "abc-say", Role: vibekit.RoleAssistant, Ts: 200, Content: "the turn KAS still held"},
	}

	h.lifecycle.shutdownCancel()

	h.replay.swapProjectedTranscript(chatID, projected, "wm-1")

	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	if len(c.Messages) != len(projected) {
		t.Fatalf("swapped transcript holds %d messages, want %d; the merge was refused at shutdown",
			len(c.Messages), len(projected))
	}
	if c.CompactionWatermark != "wm-1" {
		t.Errorf("watermark = %q, want %q", c.CompactionWatermark, "wm-1")
	}
}

// TestMergeProjection_Union covers the per-row union: which side owns which field, what
// pairs, and what each counter counts. The record row carries stamps only this process
// measured; the projected row carries what the agent stated.
func TestMergeProjection_Union(t *testing.T) {
	// rec is a persisted assistant row: KAS's id in KASMessageID, vibekit's own in ID,
	// plus the three stamps no replay carries.
	rec := func(id, kasID, content string, ts int64) vibekit.Message {
		return vibekit.Message{
			ID: id, KASMessageID: kasID, Role: vibekit.RoleAssistant, Ts: ts,
			Content: content, TurnModel: "opus-5", TurnElapsedMs: 1683,
			ChangedFiles: map[string]*vibekit.FileChange{"a.go": {LinesAdded: 3}},
		}
	}
	proj := func(kasID, content string, ts int64) vibekit.Message {
		return vibekit.Message{
			ID: kasID, KASMessageID: kasID, Role: vibekit.RoleAssistant, Ts: ts,
			Content: content, TurnOutcome: vibekit.TurnOutcomeCompleted,
		}
	}

	t.Run("a paired row keeps the record's stamps and takes the replay's account", func(t *testing.T) {
		existing := []vibekit.Message{rec("m-live", "abc-say", "hello", 200)}
		projected := []vibekit.Message{proj("abc-say", "hello, world", 150)}
		got, changed, stats := mergeProjection(existing, projected)
		if len(got) != 1 {
			t.Fatalf("merged %d rows, want 1: the turn was duplicated:\n%+v", len(got), got)
		}
		if stats.Paired != 1 || stats.Added != 0 || stats.Dropped != 0 || stats.Replaced != 0 {
			t.Errorf("stats = %+v, want Paired 1 and nothing else", stats)
		}
		if got[0].ID != "abc-say" || got[0].Content != "hello, world" {
			t.Errorf("merged row = {%q, %q}, want the replay's id and content", got[0].ID, got[0].Content)
		}
		if got[0].TurnModel != "opus-5" || got[0].TurnElapsedMs != 1683 || len(got[0].ChangedFiles) != 1 {
			t.Errorf("the record's stamps did not survive the union: %+v", got[0])
		}
		if got[0].TurnOutcome != vibekit.TurnOutcomeCompleted {
			t.Errorf("TurnOutcome = %q, want the replay's %q", got[0].TurnOutcome, vibekit.TurnOutcomeCompleted)
		}
		if !changed {
			t.Error("changed = false over a row whose content and id both moved")
		}
	})

	t.Run("a paired row newer than the projection is dropped, not preserved", func(t *testing.T) {
		// Its Ts is time.Now() at turn end and the twin's is the turn's first frame, so a
		// paired row is ROUTINELY newer. Preserving it would emit the turn twice.
		existing := []vibekit.Message{rec("m-live", "abc-say", "hello", 9_999)}
		projected := []vibekit.Message{proj("abc-say", "hello", 150)}
		got, _, stats := mergeProjection(existing, projected)
		if len(got) != 1 || stats.Paired != 1 || stats.Dropped != 0 {
			t.Errorf("merged %d rows with stats %+v, want one paired row:\n%+v", len(got), stats, got)
		}
	})

	t.Run("roles must match, so a notify row and its event twin do not pair", func(t *testing.T) {
		const id = "notify-8dc94493"
		existing := []vibekit.Message{{
			ID: id, Role: vibekit.RoleUser, Ts: 150, Content: "the step asked something",
			UserKind: vibekit.UserKindSteer, SteerState: vibekit.SteerStateRead,
		}}
		projected := []vibekit.Message{{
			ID: id, Role: vibekit.RoleEvent, EventKind: vibekit.EventStepNotice, Ts: 150,
			Content: "the step asked something",
		}}
		got, changed, stats := mergeProjection(existing, projected)
		if stats.Paired != 0 || stats.Replaced != 1 {
			t.Errorf("stats = %+v, want Paired 0 and Replaced 1", stats)
		}
		if len(got) != 1 || got[0].Role != vibekit.RoleEvent {
			t.Fatalf("merged %d rows, want the projected event row alone:\n%+v", len(got), got)
		}
		if got[0].SteerState != "" {
			t.Errorf("SteerState = %q on an event row, want empty: nothing reads it there", got[0].SteerState)
		}
		if !changed {
			t.Error("changed = false over a row whose role moved")
		}
	})

	t.Run("an empty projected outcome keeps all four conclusion fields", func(t *testing.T) {
		// The crash case: the process died with a local conclusion and KAS logged no
		// turn_end, so a per-field union would erase the failure.
		r := rec("m-live", "abc-say", "partial", 200)
		r.TurnOutcome = vibekit.TurnOutcomeCancelled
		r.TurnStopReasonRaw = "cancelled"
		r.TurnTruncated = true
		r.TurnFailureReason = "the reader cancelled"
		p := proj("abc-say", "partial", 150)
		p.TurnOutcome = ""
		got, _, _ := mergeProjection([]vibekit.Message{r}, []vibekit.Message{p})
		if got[0].TurnOutcome != vibekit.TurnOutcomeCancelled || got[0].TurnStopReasonRaw != "cancelled" ||
			!got[0].TurnTruncated || got[0].TurnFailureReason != "the reader cancelled" {
			t.Errorf("the conclusion unit did not survive an outcome-less replay: %+v", got[0])
		}
	})

	t.Run("a clean replayed outcome takes the reason with it", func(t *testing.T) {
		r := rec("m-live", "abc-say", "hi", 200)
		r.TurnOutcome = vibekit.TurnOutcomeCancelled
		r.TurnFailureReason = "the reader cancelled"
		got, _, _ := mergeProjection([]vibekit.Message{r}, []vibekit.Message{proj("abc-say", "hi", 150)})
		if got[0].TurnOutcome != vibekit.TurnOutcomeCompleted {
			t.Errorf("TurnOutcome = %q, want the replay's %q", got[0].TurnOutcome, vibekit.TurnOutcomeCompleted)
		}
		if got[0].TurnFailureReason != "" {
			t.Errorf("TurnFailureReason = %q, want empty: a completed turn cannot carry a failure sentence",
				got[0].TurnFailureReason)
		}
	})

	t.Run("a non-clean replayed outcome keeps the record's reason", func(t *testing.T) {
		r := rec("m-live", "abc-say", "hi", 200)
		r.TurnOutcome = vibekit.TurnOutcomeCancelled
		r.TurnFailureReason = "the reader cancelled"
		p := proj("abc-say", "hi", 150)
		p.TurnOutcome = vibekit.TurnOutcomeFailed
		got, _, _ := mergeProjection([]vibekit.Message{r}, []vibekit.Message{p})
		if got[0].TurnOutcome != vibekit.TurnOutcomeFailed || got[0].TurnFailureReason != "the reader cancelled" {
			t.Errorf("outcome %q with reason %q, want error carrying the record's only reason",
				got[0].TurnOutcome, got[0].TurnFailureReason)
		}
	})

	t.Run("a user row keeps the record's content and an assistant row takes the replay's", func(t *testing.T) {
		// BuildPromptBlocks appends a path reference per attachment it could not inline, so
		// the replay's user text can hold machine-added words the reader never typed.
		existing := []vibekit.Message{
			{ID: "u1", KASMessageID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "read this"},
			rec("m-live", "abc-say", "old", 200),
		}
		projected := []vibekit.Message{
			{ID: "u1", KASMessageID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "read this\n\n[file: /workspace/a.go]"},
			proj("abc-say", "new", 200),
		}
		got, _, _ := mergeProjection(existing, projected)
		if got[0].Content != "read this" {
			t.Errorf("user content = %q, want the record's %q", got[0].Content, "read this")
		}
		if got[1].Content != "new" {
			t.Errorf("assistant content = %q, want the replay's %q", got[1].Content, "new")
		}
	})

	t.Run("the record's steer state outranks a projected inference", func(t *testing.T) {
		existing := []vibekit.Message{{
			ID: "steer-1", Role: vibekit.RoleUser, Ts: 150, Content: "target main",
			UserKind: vibekit.UserKindSteer, SteerState: vibekit.SteerStateRead,
			SteerOrigin: vibekit.SteerOriginUser,
		}}
		projected := []vibekit.Message{{
			ID: "steer-1", Role: vibekit.RoleUser, Ts: 150, Content: "target main",
			UserKind: vibekit.UserKindSteer, SteerState: vibekit.SteerStateDropped,
		}}
		got, _, stats := mergeProjection(existing, projected)
		if stats.Paired != 1 {
			t.Fatalf("stats = %+v, want the steer row paired", stats)
		}
		if got[0].SteerState != vibekit.SteerStateRead || got[0].SteerOrigin != vibekit.SteerOriginUser {
			t.Errorf("steer facts = {%q, %q}, want the record's {read, user}: an undelivered correction must not read as landed",
				got[0].SteerState, got[0].SteerOrigin)
		}
	})

	t.Run("a record steer row with no state takes the projected inference", func(t *testing.T) {
		existing := []vibekit.Message{{
			ID: "steer-1", Role: vibekit.RoleUser, Ts: 150, Content: "target main",
			UserKind: vibekit.UserKindSteer,
		}}
		projected := []vibekit.Message{{
			ID: "steer-1", Role: vibekit.RoleUser, Ts: 150, Content: "target main",
			UserKind: vibekit.UserKindSteer, SteerState: vibekit.SteerStateDropped,
		}}
		got, changed, stats := mergeProjection(existing, projected)
		if stats.Paired != 1 {
			t.Fatalf("stats = %+v, want the steer row paired", stats)
		}
		if got[0].SteerState != vibekit.SteerStateDropped {
			t.Errorf("SteerState = %q, want %q: an absent state is not a state, and an unstamped row renders as delivered",
				got[0].SteerState, vibekit.SteerStateDropped)
		}
		if !changed {
			t.Error("changed = false, want true: the stamp has to reach the store or the reader never sees it")
		}
	})

	t.Run("a second projected row under one key takes no stamps", func(t *testing.T) {
		existing := []vibekit.Message{rec("m-live", "abc-say", "hello", 200)}
		projected := []vibekit.Message{
			proj("abc-say", "first", 150),
			proj("abc-say", "second", 160),
		}
		got, _, stats := mergeProjection(existing, projected)
		if stats.Paired != 1 || stats.Added != 1 {
			t.Errorf("stats = %+v, want Paired 1 and Added 1", stats)
		}
		stamped := 0
		for i := range got {
			if got[i].TurnModel == "opus-5" {
				stamped++
			}
		}
		if stamped != 1 {
			t.Errorf("the record's stamps appear on %d of %d rows, want exactly 1", stamped, len(got))
		}
	})

	t.Run("a paired compaction event is emitted once, byte-equal to the record's", func(t *testing.T) {
		// It pairs through the ID fallback, so it is CONSUMED and never reaches
		// preserveExisting — which would have dropped it, the replay having produced one.
		row := vibekit.Message{
			ID: "m1-compacted", Role: vibekit.RoleEvent, EventKind: vibekit.EventCompacted,
			Ts: 100, Content: "## Goal",
		}
		got, changed, stats := mergeProjection([]vibekit.Message{row}, []vibekit.Message{row})
		if len(got) != 1 || stats.Paired != 1 || stats.Dropped != 0 {
			t.Fatalf("merged %d rows with stats %+v, want one paired row:\n%+v", len(got), stats, got)
		}
		if !reflect.DeepEqual(got[0], row) {
			t.Errorf("merged compaction row = %+v, want the record's own", got[0])
		}
		if changed {
			t.Error("changed = true over a transcript nothing moved in")
		}
	})

	t.Run("paired counts 0 for a record that carries no agent-side id", func(t *testing.T) {
		existing := []vibekit.Message{rec("m-live", "", "hello", 100)}
		projected := []vibekit.Message{proj("abc-say", "hello", 150)}
		_, _, stats := mergeProjection(existing, projected)
		if stats.Paired != 0 || stats.Added != 1 || stats.Dropped != 1 {
			t.Errorf("stats = %+v, want Paired 0, Added 1, Dropped 1 for a legacy row", stats)
		}
	})

	t.Run("replaced counts a projected row that took an id without pairing", func(t *testing.T) {
		// Same id, roles differ, so it is a replacement rather than a pairing: the row
		// count does not grow and the id is still there.
		existing := []vibekit.Message{{ID: "abc-say", Role: vibekit.RoleUser, Ts: 100, Content: "hi"}}
		projected := []vibekit.Message{proj("abc-say", "hi", 100)}
		_, changed, stats := mergeProjection(existing, projected)
		if stats.Replaced != 1 || stats.Paired != 0 || stats.Added != 0 || stats.Dropped != 0 {
			t.Errorf("stats = %+v, want Replaced 1 and nothing else", stats)
		}
		if !changed {
			t.Error("changed = false over an id whose row was replaced")
		}
	})

	t.Run("a field-only difference is changed", func(t *testing.T) {
		// The shape sameMessageIDs cannot see: one id sequence, different values.
		r := rec("abc-say", "abc-say", "hello", 150)
		p := proj("abc-say", "hello", 150)
		p.TurnCredits = 0.115
		got, changed, stats := mergeProjection([]vibekit.Message{r}, []vibekit.Message{p})
		if stats.Paired != 1 || !sameMessageIDs([]vibekit.Message{r}, got) {
			t.Fatalf("fixture moved the id sequence, so it does not isolate a field difference: %+v", got)
		}
		if !changed {
			t.Error("changed = false over a paired row whose credits arrived from the replay")
		}
	})
}

// TestMergeProjection_UnionToolCalls covers the sub-pairing: one statement per call, from
// the side that measured it.
func TestMergeProjection_UnionToolCalls(t *testing.T) {
	recRow := func(calls ...vibekit.ToolCall) vibekit.Message {
		return vibekit.Message{
			ID: "m-live", KASMessageID: "abc-say", Role: vibekit.RoleAssistant, Ts: 200,
			Content: "ran it", ToolCalls: calls,
		}
	}
	projRow := func(calls ...vibekit.ToolCall) vibekit.Message {
		return vibekit.Message{
			ID: "abc-say", KASMessageID: "abc-say", Role: vibekit.RoleAssistant, Ts: 150,
			Content: "ran it", ToolCalls: calls,
		}
	}

	t.Run("a paired call keeps the duration and terminal the record measured", func(t *testing.T) {
		existing := []vibekit.Message{recRow(vibekit.ToolCall{
			ID: "t1", Title: "old title", Status: vibekit.ToolCompleted,
			DurationMs: 4210, TerminalID: "term-9",
		})}
		projected := []vibekit.Message{projRow(vibekit.ToolCall{
			ID: "t1", Title: "Run command", Status: vibekit.ToolCompleted,
			WorkflowID: "wf_1",
		})}
		got, _, _ := mergeProjection(existing, projected)
		if len(got[0].ToolCalls) != 1 {
			t.Fatalf("merged %d calls, want 1: %+v", len(got[0].ToolCalls), got[0].ToolCalls)
		}
		call := got[0].ToolCalls[0]
		if call.DurationMs != 4210 || call.TerminalID != "term-9" {
			t.Errorf("the record's own measurements did not survive: %+v", call)
		}
		if call.Title != "Run command" || call.WorkflowID != "wf_1" {
			t.Errorf("the replay's statements did not land: %+v", call)
		}
	})

	t.Run("a tool-free paired row yields a NIL call list", func(t *testing.T) {
		// Empty-non-nil is a write plus a subject_changed on every load: DeepEqual
		// distinguishes the two and the store omits tool_calls under omitempty.
		got, _, _ := mergeProjection([]vibekit.Message{recRow()}, []vibekit.Message{projRow()})
		if got[0].ToolCalls != nil {
			t.Errorf("ToolCalls = %#v, want nil", got[0].ToolCalls)
		}
	})

	t.Run("two projected calls under one id copy the record's stamps once", func(t *testing.T) {
		existing := []vibekit.Message{recRow(vibekit.ToolCall{
			ID: "t1", Status: vibekit.ToolCompleted, DurationMs: 4210,
		})}
		projected := []vibekit.Message{projRow(
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted},
		)}
		got, _, _ := mergeProjection(existing, projected)
		stamped := 0
		for _, c := range got[0].ToolCalls {
			if c.DurationMs == 4210 {
				stamped++
			}
		}
		if len(got[0].ToolCalls) != 2 || stamped != 1 {
			t.Errorf("%d calls with %d carrying the record's duration, want 2 and exactly 1",
				len(got[0].ToolCalls), stamped)
		}
	})

	t.Run("a segmented turn pairs each row with its own segment", func(t *testing.T) {
		// Two record rows under DIFFERENT keys, a compaction between them: SplitSegment
		// resets the latch, so each segment names its own record.
		existing := []vibekit.Message{
			{
				ID: "m-seg1", KASMessageID: "say-1", Role: vibekit.RoleAssistant, Ts: 100,
				Content: "before", ToolCalls: []vibekit.ToolCall{{ID: "t1", DurationMs: 11}},
			},
			{ID: "e1", Role: vibekit.RoleEvent, EventKind: vibekit.EventCompacted, Ts: 150},
			{
				ID: "m-seg2", KASMessageID: "say-2", Role: vibekit.RoleAssistant, Ts: 200,
				Content: "after", ToolCalls: []vibekit.ToolCall{{ID: "t2", DurationMs: 22}},
			},
		}
		projected := []vibekit.Message{
			{
				ID: "say-1", KASMessageID: "say-1", Role: vibekit.RoleAssistant, Ts: 100,
				Content: "before", ToolCalls: []vibekit.ToolCall{{ID: "t1"}},
			},
			{ID: "e1", Role: vibekit.RoleEvent, EventKind: vibekit.EventCompacted, Ts: 150},
			{
				ID: "say-2", KASMessageID: "say-2", Role: vibekit.RoleAssistant, Ts: 200,
				Content: "after", ToolCalls: []vibekit.ToolCall{{ID: "t2"}},
			},
		}
		got, _, stats := mergeProjection(existing, projected)
		if stats.Paired != 3 || len(got) != 3 {
			t.Fatalf("merged %d rows with stats %+v, want 3 pairings:\n%+v", len(got), stats, got)
		}
		if got[0].ToolCalls[0].DurationMs != 11 || got[2].ToolCalls[0].DurationMs != 22 {
			t.Errorf("a segment took the other segment's call durations: %d and %d",
				got[0].ToolCalls[0].DurationMs, got[2].ToolCalls[0].DurationMs)
		}
	})
}

// TestMergeProjection_OutcomeUnit covers rule 3: one statement about how a call ENDED, from
// whichever side has an outcome. IsOutcome rather than plain terminality is the whole test —
// `aborted` is minted locally at a close and says NOT KNOWN on either side.
func TestMergeProjection_OutcomeUnit(t *testing.T) {
	pair := func(recCall, projCall vibekit.ToolCall) vibekit.ToolCall {
		t.Helper()
		existing := []vibekit.Message{{
			ID: "m-live", KASMessageID: "abc-say", Role: vibekit.RoleAssistant, Ts: 200,
			Content: "ran it", ToolCalls: []vibekit.ToolCall{recCall},
		}}
		projected := []vibekit.Message{{
			ID: "abc-say", KASMessageID: "abc-say", Role: vibekit.RoleAssistant, Ts: 150,
			Content: "ran it", ToolCalls: []vibekit.ToolCall{projCall},
		}}
		got, _, stats := mergeProjection(existing, projected)
		if stats.Paired != 1 || len(got) != 1 || len(got[0].ToolCalls) != 1 {
			t.Fatalf("fixture did not pair one row with one call: stats %+v, rows %d", stats, len(got))
		}
		return got[0].ToolCalls[0]
	}

	t.Run("a settled record call refuses a replayed non-terminal one", func(t *testing.T) {
		got := pair(
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Output: "real output"},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolAborted},
		)
		if got.Status != vibekit.ToolCompleted || got.Output != "real output" {
			t.Errorf("call = {%q, %q}, want the record's completed outcome and its output",
				got.Status, got.Output)
		}
	})

	t.Run("an aborted record call is UPGRADED by a replayed outcome", func(t *testing.T) {
		// aborted is vibekit's own word for a call nothing could settle, so refusing the
		// replay's completed would render a tool that ran and succeeded as stopped, forever.
		got := pair(
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolAborted, TerminalID: "term-9"},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Output: "the real result", DurationMs: 812},
		)
		if got.Status != vibekit.ToolCompleted || got.Output != "the real result" {
			t.Errorf("call = {%q, %q}, want the replay's completed outcome and its output",
				got.Status, got.Output)
		}
		if got.DurationMs != 812 {
			t.Errorf("DurationMs = %d, want the replay's 812: the live writer runs after its own outcome guard, so this arm's record always carries 0",
				got.DurationMs)
		}
		if got.TerminalID != "term-9" {
			t.Errorf("TerminalID = %q, want the record's: it is outside the unit", got.TerminalID)
		}
	})

	t.Run("neither side has an outcome, so only the status moves", func(t *testing.T) {
		// A projected aborted call carries no output at all, so handing it the unit would
		// replace the record's own fragment with nothing.
		spans := []vibekit.TextSpan{{Start: 0, End: 4}}
		got := pair(
			vibekit.ToolCall{
				ID: "t1", Status: vibekit.ToolInProgress, Output: "frag",
				OutputSpans: spans, Truncated: &vibekit.ToolTruncation{OutputBytes: 9000},
			},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolAborted},
		)
		if got.Status != vibekit.ToolAborted {
			t.Errorf("Status = %q, want aborted: the projection settled a record spinner", got.Status)
		}
		if got.Output != "frag" || len(got.OutputSpans) != 1 {
			t.Errorf("the record's own fragment did not survive: %+v", got)
		}
		if got.Truncated == nil || got.Truncated.OutputBytes != 9000 {
			t.Errorf("Truncated = %+v, want the record's own cut record", got.Truncated)
		}
	})

	t.Run("the replay's arm keeps the record's diffs against an empty projected slice", func(t *testing.T) {
		diffs := []vibekit.ToolDiff{{Path: "a.go"}}
		got := pair(
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolInProgress, Diffs: diffs},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Output: "done"},
		)
		if len(got.Diffs) != 1 || got.Diffs[0].Path != "a.go" {
			t.Errorf("Diffs = %+v, want the record's: the wire sends none, so an empty projected slice states nothing",
				got.Diffs)
		}
	})

	t.Run("both sides report an outcome, so the record's unit stands", func(t *testing.T) {
		got := pair(
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolFailed, Output: "record's"},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Output: "replay's"},
		)
		if got.Status != vibekit.ToolFailed || got.Output != "record's" {
			t.Errorf("call = {%q, %q}, want the record's: a disagreement is the two paths reading ONE tool_result",
				got.Status, got.Output)
		}
	})

	t.Run("a cut input keeps its marker while the replaced output's cut goes", func(t *testing.T) {
		got := pair(
			vibekit.ToolCall{
				ID: "t1", Status: vibekit.ToolInProgress, Output: "frag",
				Input:     json.RawMessage(`{"cmd":"…"}`),
				Truncated: &vibekit.ToolTruncation{OutputBytes: 9000, InputBytes: 4096},
			},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Output: "whole output"},
		)
		if got.Truncated == nil {
			t.Fatal("Truncated = nil, want the input's cut record kept")
		}
		if got.Truncated.InputBytes != 4096 {
			t.Errorf("Truncated.InputBytes = %d, want 4096: the input is not the unit's to drop",
				got.Truncated.InputBytes)
		}
		if got.Truncated.OutputBytes != 0 {
			t.Errorf("Truncated.OutputBytes = %d, want 0: those bytes are gone, so a marker for them misreports the cut",
				got.Truncated.OutputBytes)
		}
	})

	t.Run("a replayed diff set drops the record's diff cut record", func(t *testing.T) {
		got := pair(
			vibekit.ToolCall{
				ID: "t1", Status: vibekit.ToolInProgress,
				Truncated: &vibekit.ToolTruncation{InputBytes: 4096, DiffBytes: 80_000, DiffCount: 12},
			},
			vibekit.ToolCall{
				ID: "t1", Status: vibekit.ToolCompleted,
				Diffs: []vibekit.ToolDiff{{Path: "a.go"}},
			},
		)
		if len(got.Diffs) != 1 {
			t.Fatalf("Diffs = %+v, want the replay's", got.Diffs)
		}
		if got.Truncated == nil || got.Truncated.InputBytes != 4096 {
			t.Fatalf("Truncated = %+v, want the input's cut kept", got.Truncated)
		}
		if got.Truncated.DiffBytes != 0 || got.Truncated.DiffCount != 0 {
			t.Errorf("Truncated diff cut = {%d, %d}, want zeroes beside the replay's whole diffs",
				got.Truncated.DiffBytes, got.Truncated.DiffCount)
		}
	})

	t.Run("nothing survives the cut record, so it is nil rather than a zero pointer", func(t *testing.T) {
		got := pair(
			vibekit.ToolCall{
				ID: "t1", Status: vibekit.ToolInProgress,
				Truncated: &vibekit.ToolTruncation{OutputBytes: 9000},
			},
			vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Output: "whole"},
		)
		if got.Truncated != nil {
			t.Errorf("Truncated = %+v, want nil: DeepEqual distinguishes it from a zero pointer and the store omits it",
				got.Truncated)
		}
	})
}

// TestMergeProjection_InputGates covers rule 4's three conjuncts, each closing a different
// false-`changed` source. The record side goes through the REAL store: an in-memory fixture
// sees neither the indentation and HTML escaping the write applies nor the input the write
// cuts, which is how a green test shipped beside a live defect twice in this chain.
func TestMergeProjection_InputGates(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	roundTrip := func(t *testing.T, call vibekit.ToolCall) []vibekit.Message {
		t.Helper()
		cs, err := chat.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("chat.NewStore: %v", err)
		}
		// The SECOND-load shape: after one load the merged row's ID is already the
		// projected id and its Ts the projected one, so Input is the only field that can
		// differ and `changed` isolates it.
		row := vibekit.Message{
			ID: "abc-say", KASMessageID: "abc-say", Role: vibekit.RoleAssistant, Ts: 150,
			Content: "ran it", ToolCalls: []vibekit.ToolCall{call},
		}
		if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
			c.Messages = []vibekit.Message{row}
			return true
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		c, ok := cs.Get(t.Context(), chatID)
		if !ok {
			t.Fatal("the seeded chat vanished")
		}
		return c.Messages
	}
	projRow := func(call vibekit.ToolCall) []vibekit.Message {
		return []vibekit.Message{{
			ID: "abc-say", KASMessageID: "abc-say", Role: vibekit.RoleAssistant, Ts: 150,
			Content: "ran it", ToolCalls: []vibekit.ToolCall{call},
		}}
	}

	t.Run("an equivalent input read back off disk reports no change", func(t *testing.T) {
		// The record's copy is indented with `<` as \u003c; the replay's is the compact wire
		// bytes. A byte comparison of the two is unequal on EVERY load.
		wire := json.RawMessage(`{"cmd":"grep -n '<a>' x.go","path":"x.go"}`)
		existing := roundTrip(t, vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Input: wire})
		if bytes.Equal(existing[0].ToolCalls[0].Input, wire) {
			t.Fatal("the store handed back the wire bytes verbatim, so this fixture does not exercise the normalization")
		}
		before := existing[0].ToolCalls[0].Input
		got, changed, stats := mergeProjection(existing,
			projRow(vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Input: wire}))
		if stats.Paired != 1 {
			t.Fatalf("stats = %+v, want the row paired", stats)
		}
		if !bytes.Equal(got[0].ToolCalls[0].Input, before) {
			t.Errorf("Input = %s, want the record's bytes verbatim", got[0].ToolCalls[0].Input)
		}
		if changed {
			t.Error("changed = true over two encodings of one document: a write plus a subject_changed on every load")
		}
	})

	t.Run("an input the store cut is not re-widened", func(t *testing.T) {
		// The store re-cuts on every write, so overwriting writes, gets re-cut, and repeats.
		// The FIRST merge is entitled to report a change; the loop shows itself on the next.
		big := json.RawMessage(`{"content":"` + strings.Repeat("x", 9<<10) + `"}`)
		existing := roundTrip(t, vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Input: big})
		cut := existing[0].ToolCalls[0]
		if cut.Truncated == nil || cut.Truncated.InputBytes == 0 {
			t.Fatalf("the store did not cut this input, so the fixture exercises nothing: %+v", cut.Truncated)
		}
		projected := projRow(vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Input: big})
		merged, _, _ := mergeProjection(existing, projected)

		// Feed the first merge's own result back through the store, as the next load's record.
		cs, err := chat.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("chat.NewStore: %v", err)
		}
		if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
			c.Messages = merged
			return true
		}); err != nil {
			t.Fatalf("persist the merged transcript: %v", err)
		}
		c, _ := cs.Get(t.Context(), chatID)
		if _, changed, _ := mergeProjection(c.Messages, projected); changed {
			t.Error("changed = true on the SECOND merge: the merge re-widens an input the store cuts again, forever")
		}
	})

	t.Run("a projected call stating no input keeps the record's", func(t *testing.T) {
		// sameRawJSON answers len(a) == len(b) when either side is empty, which is FALSE for
		// a non-empty record input — so without statesInput the record's input is DESTROYED.
		wire := json.RawMessage(`{"cmd":"ls"}`)
		existing := roundTrip(t, vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Input: wire})
		before := existing[0].ToolCalls[0].Input
		for _, tc := range []struct {
			name  string
			input json.RawMessage
		}{
			{name: "absent", input: nil},
			{name: "an empty object", input: json.RawMessage(`{}`)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, changed, _ := mergeProjection(existing,
					projRow(vibekit.ToolCall{ID: "t1", Status: vibekit.ToolCompleted, Input: tc.input}))
				if !bytes.Equal(got[0].ToolCalls[0].Input, before) {
					t.Errorf("Input = %s, want the record's %s", got[0].ToolCalls[0].Input, before)
				}
				if changed {
					t.Error("changed = true over a projected call that states no input")
				}
			})
		}
	})
}

// TestMergeProjection_IdempotentThroughTheStore is the property that keeps a resumed chat
// from writing on every load: merge, persist the result, merge again against the SAME
// projection, and nothing moves.
//
// It round-trips through the real store because the in-memory form skips both transformations
// that break it — MarshalIndent's escaping and storeChat's re-cut — and it carries a
// TOOL-FREE row beside a tool-bearing one, because the record's read-back ToolCalls is nil
// (omitempty) and an empty-non-nil merged list is a write on every load no other case sees.
//
// The property is one-write-then-STABLE rather than zero-write: an over-budget input the
// record never held is written once, cut by the store, and declined from then on.
func TestMergeProjection_IdempotentThroughTheStore(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	persist := func(t *testing.T, msgs []vibekit.Message) []vibekit.Message {
		t.Helper()
		cs, err := chat.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("chat.NewStore: %v", err)
		}
		if _, err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
			c.Messages = msgs
			return true
		}); err != nil {
			t.Fatalf("persist: %v", err)
		}
		c, ok := cs.Get(t.Context(), chatID)
		if !ok {
			t.Fatal("the seeded chat vanished")
		}
		return c.Messages
	}

	projected := []vibekit.Message{
		{ID: "u1", KASMessageID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "run it"},
		{
			ID: "say-1", KASMessageID: "say-1", Role: vibekit.RoleAssistant, Ts: 110,
			Content: "ran it", TurnOutcome: vibekit.TurnOutcomeCompleted,
			ToolCalls: []vibekit.ToolCall{{
				ID: "t1", Status: vibekit.ToolCompleted, Title: "Run command",
				Input: json.RawMessage(`{"cmd":"grep -n '<a>' x.go"}`), Output: "1:a",
			}},
		},
		// The tool-FREE row: 32 of 1,062 persisted assistant rows carry no calls.
		{
			ID: "say-2", KASMessageID: "say-2", Role: vibekit.RoleAssistant, Ts: 120,
			Content: "and answered", TurnOutcome: vibekit.TurnOutcomeCompleted,
		},
	}
	live := []vibekit.Message{
		{ID: "u1", KASMessageID: "u1", Role: vibekit.RoleUser, Ts: 100, Content: "run it"},
		{
			ID: "m-live-1", KASMessageID: "say-1", Role: vibekit.RoleAssistant, Ts: 111,
			Content: "ran it", TurnModel: "opus-5", TurnElapsedMs: 900,
			ToolCalls: []vibekit.ToolCall{{
				ID: "t1", Status: vibekit.ToolCompleted, Title: "Run command",
				Input: json.RawMessage(`{"cmd":"grep -n '<a>' x.go"}`), Output: "1:a",
				DurationMs: 42, TerminalID: "term-1",
			}},
		},
		{
			ID: "m-live-2", KASMessageID: "say-2", Role: vibekit.RoleAssistant, Ts: 121,
			Content: "and answered", TurnModel: "opus-5",
		},
	}

	first, changed, stats := mergeProjection(persist(t, live), projected)
	if stats.Paired != 3 {
		t.Fatalf("stats = %+v, want three pairings; the fixture does not exercise the union", stats)
	}
	if !changed {
		t.Fatal("changed = false on the FIRST merge, so the fixture asserts nothing about stability")
	}
	if first[2].ToolCalls != nil {
		t.Errorf("the tool-free row's merged ToolCalls = %#v, want nil", first[2].ToolCalls)
	}
	if first[1].TurnModel != "opus-5" || first[1].ToolCalls[0].DurationMs != 42 {
		t.Errorf("the record's stamps did not survive the first merge: %+v", first[1])
	}

	stored := persist(t, first)
	second, changedAgain, stats2 := mergeProjection(stored, projected)
	if stats2.Paired != 3 {
		t.Fatalf("stats = %+v on the second merge, want three pairings: the stamp did not re-arm the key", stats2)
	}
	if changedAgain {
		t.Errorf("changed = true on the SECOND merge: a resumed chat writes and announces on every load\nstored: %+v\nsecond: %+v",
			stored, second)
	}
	if !reflect.DeepEqual(stored, second) {
		t.Errorf("the second merge moved the transcript:\nstored: %+v\nsecond: %+v", stored, second)
	}
}
