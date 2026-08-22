package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

// TestReplayProjection_SettleBarrier is the test that matters here: the settle
// condition is a RACE GUARD, and each half of it has to be load-bearing.
//
// session/load is issued inside bridge.Start, which blocks on the result, while
// the replay frames arrive on the Forward goroutine. The frames precede the
// result on the wire, so when Start returns they are all PUSHED — but notifCh is
// buffered (256), so Forward may not have DRAINED them. Settling on the load's
// return alone would adopt a partial transcript.
func TestReplayProjection_SettleBarrier(t *testing.T) {
	const chatID vibekit.ChatID = "c1"

	t.Run("no settle before the load returns", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)

		rp.SettleReplayProjection(chatID, 0, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times before the load returned, want 0", rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Error("projection was dropped before the load returned")
		}
	})

	t.Run("no settle while frames remain buffered", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadDone(chatID)

		// The consumer still sees depth on the channel: undrained replay.
		rp.SettleReplayProjection(chatID, 3, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times with 3 frames still buffered, want 0", rec.calls)
		}
		if !rp.hasProjection(chatID) {
			t.Error("projection was dropped while frames were still buffered")
		}
	})

	t.Run("settles once both halves hold", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadDone(chatID)

		rp.SettleReplayProjection(chatID, 0, false)
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

	t.Run("settle is idempotent", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadDone(chatID)

		// Forward calls this after EVERY frame, so a second call with the same
		// condition must not re-swap a transcript.
		for range 4 {
			rp.SettleReplayProjection(chatID, 0, false)
		}
		if rec.calls != 1 {
			t.Errorf("settled %d times, want 1: Forward calls settle per frame", rec.calls)
		}
	})

	t.Run("force settles despite buffered depth", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)
		rp.MarkReplayLoadDone(chatID)

		// The bridge-exit call: no further frame can arrive to re-trigger the
		// check, so the projection must complete rather than leak.
		rp.SettleReplayProjection(chatID, 7, true)
		if rec.calls != 1 {
			t.Errorf("forced settle ran %d times, want 1", rec.calls)
		}
	})

	t.Run("force still requires the load to have returned", func(t *testing.T) {
		rp, rec := replayWithRecorder()
		rp.OpenReplayProjection(chatID)
		feedOneTurn(t, rp, chatID)

		// A bridge that died before session/load returned has no transcript to
		// adopt; forcing must not manufacture one from a partial replay.
		rp.SettleReplayProjection(chatID, 0, true)
		if rec.calls != 0 {
			t.Errorf("forced settle ran %d times on a load that never returned, want 0", rec.calls)
		}
	})
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
	rp.MarkReplayLoadDone(chatID)
	rp.SettleReplayProjection(chatID, 0, true)
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
	rp.MarkReplayLoadDone(chatID)
	rp.SettleReplayProjection(chatID, 0, false)

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

// awaitReplayedTurn waits for the settled projection to reach the chat record.
// A deadline-bounded poll rather than a sleep: the settle happens on the Forward
// goroutine, so the test cannot know the instant it lands, and this fails closed
// with a diagnostic instead of passing whenever the machine is fast enough.
// awaitPatience bounds the replay polls below. It is a test-owned patience
// bound, not a production budget: nothing here asserts how PROMPTLY the
// transcript is adopted, only that it is, so widening it cannot hide a defect
// while a tight bound turns a starved runner into a red build. 5s expired
// exactly on a loaded CI runner for work that takes microseconds when the
// scheduler cooperates.
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
			t.Fatalf("the replayed turn %q never reached the chat's transcript within %v; a "+
				"resumed chat shows an empty history instead of the conversation KAS replayed",
				want, awaitPatience)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSessionLoad_AdoptsTheReplayedTranscript is the load path end to end: KAS
// replays the stored conversation as tagged session/update frames, and the chat's
// transcript is what they build.
//
// Both halves of the settle condition are wired here rather than asserted
// separately, because either one missing produces the same user-visible failure —
// a resumed chat whose history is gone. The projection has to be OPEN before
// Forward attaches, or the frames arrive with nowhere to land and are dropped; and
// the load's return has to be recorded, or no settle path will ever complete.
func TestSessionLoad_AdoptsTheReplayedTranscript(t *testing.T) {
	// A fresh bridge per spawn, because the utility bridge the rehydrate sweep
	// starts would otherwise share this one's notification channel and drain the
	// replay out from under the chat's own Forward loop.
	cs := newFakeChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs)
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	const chatID vibekit.ChatID = "c1"
	loadedChat(t, cs, chatID)

	sb, err := h.coord.OpenBridge(t.Context(), chatID, "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br, ok := sb.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("the chat's bridge is %T, want the fake", sb.bridge)
	}

	for _, f := range []*vibekit.RPCResponse{
		replayNotif(t, "user_message_chunk", "ONE", ""),
		replayNotif(t, vibekit.ACPUpdateSessionInfo, "", "turn_start"),
		replayNotif(t, vibekit.ACPUpdateAgentChunk, "reply", ""),
		replayNotif(t, vibekit.ACPUpdateSessionInfo, "", "turn_end"),
	} {
		br.notifCh <- f
	}
	// The bridge exiting is the backstop settle, so completion no longer depends
	// on which side of the race drained the last frame.
	br.Stop()

	awaitReplayedTurn(t, cs, chatID, "reply")
}

// TestForwardExit_SettlesALoadWhoseTrailingFramesNeverCame is the backstop the
// per-frame barrier cannot provide.
//
// The barrier fires from inside the drain loop, so it needs a frame to fire ON: a
// load whose result arrives after the last replayed frame was already consumed
// leaves a fully-built projection with nothing left to trigger it. Without the
// settle at Forward's exit that transcript is never adopted and the projection is
// never released, so the chat resumes with an empty history and the rebuild leaks
// for the life of the process.
func TestForwardExit_SettlesALoadWhoseTrailingFramesNeverCame(t *testing.T) {
	h, cs, br := newTestHub()
	const chatID vibekit.ChatID = "c1"
	loadedChat(t, cs, chatID)

	// The frames are consumed first and the load returns after them, which is the
	// ordering the barrier cannot see.
	h.replay.OpenReplayProjection(chatID)
	feedOneTurn(t, h.replay, chatID)
	h.replay.MarkReplayLoadDone(chatID)

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

// TestReplayProjection_ConcurrentLoadsAreIndependent pins that the rebuilds are
// keyed per chat and stay that way.
//
// Two chats loading at once is ordinary, not exotic: a restart with several tabs
// open respawns a bridge per chat as each is touched, and each spawn opens its own
// projection. If opening the second one disturbed the map holding the first, the
// earlier chat's replay would be discarded mid-flight — it would resume with an
// empty history and no error anywhere, because a dropped projection is
// indistinguishable from a chat that never loaded.
func TestReplayProjection_ConcurrentLoadsAreIndependent(t *testing.T) {
	rp, rec := replayWithRecorder()
	const first vibekit.ChatID = "c1"
	const second vibekit.ChatID = "c2"

	rp.OpenReplayProjection(first)
	feedOneTurn(t, rp, first)
	rp.MarkReplayLoadDone(first)

	// The second chat's spawn happens while the first is still in flight.
	rp.OpenReplayProjection(second)
	if !rp.hasProjection(first) {
		t.Fatal("opening a second chat's load dropped the first chat's rebuild, so that " +
			"chat resumes with an empty transcript")
	}
	feedOneTurn(t, rp, second)

	rp.SettleReplayProjection(first, 0, false)
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
	rp.MarkReplayLoadDone(chatID)
	rp.SettleReplayProjection(chatID, 0, false)

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

// TestSwapProjectedTranscript_WritesOnlyWhatTheRecordDoesNotAlreadyHold covers
// both directions of the no-op guard, which is why the two cases live in one test:
// each is the other's failure mode.
//
// A resume that rebuilds exactly the transcript already stored must not rewrite
// it — every write broadcasts a chat_updated, and a reconnect storm after a
// restart would push one per chat for no change. But the watermark is a second,
// independent piece of state: a compaction that happened on the KAS side moves it
// without changing the message count, and dropping that update leaves vibekit
// compacting from a stale point forever.
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
