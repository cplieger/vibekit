package hub

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// replayUpdate builds a replay-tagged session/update `update` object.
func replayUpdate(t *testing.T, kind api.ACPUpdateKind, text, sub string) json.RawMessage {
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
	msgs      []api.Message
	watermark string
}

func (r *settleRecorder) sink() func(api.ChatID, []api.Message, string) {
	return func(_ api.ChatID, msgs []api.Message, wm string) {
		r.calls++
		r.msgs = msgs
		r.watermark = wm
	}
}

// feedOneTurn ingests a complete bracketed turn.
func feedOneTurn(t *testing.T, h *Hub, chatID api.ChatID) {
	t.Helper()
	for _, f := range []struct {
		kind api.ACPUpdateKind
		text string
		sub  string
	}{
		{"user_message_chunk", "ONE", ""},
		{api.ACPUpdateSessionInfo, "", "turn_start"},
		{api.ACPUpdateAgentChunk, "reply", ""},
		{api.ACPUpdateSessionInfo, "", "turn_end"},
	} {
		if !h.ingestReplayFrame(chatID, f.kind, replayUpdate(t, f.kind, f.text, f.sub)) {
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
	const chatID api.ChatID = "c1"

	t.Run("no settle before the load returns", func(t *testing.T) {
		h, rec := hubWithRecorder()
		h.OpenReplayProjection(chatID)
		feedOneTurn(t, h, chatID)

		h.SettleReplayProjection(chatID, 0, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times before the load returned, want 0", rec.calls)
		}
		if !h.hasProjection(chatID) {
			t.Error("projection was dropped before the load returned")
		}
	})

	t.Run("no settle while frames remain buffered", func(t *testing.T) {
		h, rec := hubWithRecorder()
		h.OpenReplayProjection(chatID)
		feedOneTurn(t, h, chatID)
		h.MarkReplayLoadDone(chatID)

		// The consumer still sees depth on the channel: undrained replay.
		h.SettleReplayProjection(chatID, 3, false)
		if rec.calls != 0 {
			t.Errorf("settled %d times with 3 frames still buffered, want 0", rec.calls)
		}
		if !h.hasProjection(chatID) {
			t.Error("projection was dropped while frames were still buffered")
		}
	})

	t.Run("settles once both halves hold", func(t *testing.T) {
		h, rec := hubWithRecorder()
		h.OpenReplayProjection(chatID)
		feedOneTurn(t, h, chatID)
		h.MarkReplayLoadDone(chatID)

		h.SettleReplayProjection(chatID, 0, false)
		if rec.calls != 1 {
			t.Fatalf("settled %d times, want exactly 1", rec.calls)
		}
		if len(rec.msgs) != 2 {
			t.Errorf("projected %d messages, want 2 (user + assistant)", len(rec.msgs))
		}
		if h.hasProjection(chatID) {
			t.Error("projection outlived its settle")
		}
	})

	t.Run("settle is idempotent", func(t *testing.T) {
		h, rec := hubWithRecorder()
		h.OpenReplayProjection(chatID)
		feedOneTurn(t, h, chatID)
		h.MarkReplayLoadDone(chatID)

		// Forward calls this after EVERY frame, so a second call with the same
		// condition must not re-swap a transcript.
		for range 4 {
			h.SettleReplayProjection(chatID, 0, false)
		}
		if rec.calls != 1 {
			t.Errorf("settled %d times, want 1: Forward calls settle per frame", rec.calls)
		}
	})

	t.Run("force settles despite buffered depth", func(t *testing.T) {
		h, rec := hubWithRecorder()
		h.OpenReplayProjection(chatID)
		feedOneTurn(t, h, chatID)
		h.MarkReplayLoadDone(chatID)

		// The bridge-exit call: no further frame can arrive to re-trigger the
		// check, so the projection must complete rather than leak.
		h.SettleReplayProjection(chatID, 7, true)
		if rec.calls != 1 {
			t.Errorf("forced settle ran %d times, want 1", rec.calls)
		}
	})

	t.Run("force still requires the load to have returned", func(t *testing.T) {
		h, rec := hubWithRecorder()
		h.OpenReplayProjection(chatID)
		feedOneTurn(t, h, chatID)

		// A bridge that died before session/load returned has no transcript to
		// adopt; forcing must not manufacture one from a partial replay.
		h.SettleReplayProjection(chatID, 0, true)
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
	const chatID api.ChatID = "c1"
	h, rec := hubWithRecorder()
	h.OpenReplayProjection(chatID)
	feedOneTurn(t, h, chatID)

	h.DiscardReplayProjection(chatID)
	if h.hasProjection(chatID) {
		t.Error("projection survived a discard")
	}
	// Even the settle condition holding afterwards must not resurrect it.
	h.MarkReplayLoadDone(chatID)
	h.SettleReplayProjection(chatID, 0, true)
	if rec.calls != 0 {
		t.Errorf("discarded projection settled %d times, want 0", rec.calls)
	}
}

// TestReplayProjection_FrameWithNoLoadIsRejected pins the fallback that keeps
// hub.handleSessionUpdate's drop path meaningful: a replay frame arriving with
// no load in flight has no transcript to belong to.
func TestReplayProjection_FrameWithNoLoadIsRejected(t *testing.T) {
	h, _ := hubWithRecorder()
	if h.ingestReplayFrame("nobody", api.ACPUpdateAgentChunk,
		replayUpdate(t, api.ACPUpdateAgentChunk, "stray", "")) {
		t.Error("a replay frame was consumed with no projection open")
	}
}

// TestReplayProjection_ReloadSupersedes pins that a second load for the same
// chat (the model-switch fallback path) starts clean rather than appending to
// the first load's half-built transcript.
func TestReplayProjection_ReloadSupersedes(t *testing.T) {
	const chatID api.ChatID = "c1"
	h, rec := hubWithRecorder()

	h.OpenReplayProjection(chatID)
	feedOneTurn(t, h, chatID)
	h.OpenReplayProjection(chatID) // re-load
	feedOneTurn(t, h, chatID)
	h.MarkReplayLoadDone(chatID)
	h.SettleReplayProjection(chatID, 0, false)

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

// hubWithRecorder builds the minimum Hub these tests need. The projection
// lifecycle touches only the embedded projectionState, so a bare Hub is enough
// — no bridge, no store, no goroutines.
func hubWithRecorder() (*Hub, *settleRecorder) {
	rec := &settleRecorder{}
	h := &Hub{}
	h.onProjection = rec.sink()
	return h, rec
}

// hasProjection reports whether a projection is open. Test-only, and defined
// here rather than in production so it adds no exported surface.
func (h *Hub) hasProjection(chatID api.ChatID) bool {
	h.projMu.Lock()
	defer h.projMu.Unlock()
	_, ok := h.projections[chatID]
	return ok
}
