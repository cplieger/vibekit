package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
		got := mergeProjection(existing, nil)
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
		got := mergeProjection(existing, projected)
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
		got := mergeProjection(existing, projected)
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
		got := mergeProjection(existing, projected)
		want := []string{"u1", "abc-say", "u2", "m-tail"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v (everything after the projection's window kept)", ids(got), want)
		}
	})

	t.Run("a projected message wins a timestamp tie", func(t *testing.T) {
		existing := []vibekit.Message{event("e1", 200, vibekit.EventCancelled)}
		projected := []vibekit.Message{msg("abc-say", vibekit.RoleAssistant, 200, "hello")}
		got := mergeProjection(existing, projected)
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
		got := mergeProjection(existing, projected)
		want := []string{"u1", "m-plan", "abc-say"}
		if !slices.Equal(ids(got), want) {
			t.Fatalf("got %v, want %v (the plan row preserved in timestamp order)", ids(got), want)
		}
		if len(got[1].Plan) != 1 {
			t.Errorf("the surviving plan row carries %d entries, want 1", len(got[1].Plan))
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
		got := mergeProjection(existing, projected)
		want := []string{"u1", "abc-say"}
		if !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v (a reply with content is the wire's, plan or not)", ids(got), want)
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
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
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
		if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
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

		got := extractTypes(t, bufferedSince(h, before[len(before)-1].ID))
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
