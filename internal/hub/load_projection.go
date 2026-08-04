package hub

// Replay-projection lifecycle: turning a `session/load` replay into a chat
// transcript.
//
// KAS answers session/load by replaying the stored transcript as ordinary
// session/update notifications tagged `_meta.kiro.replay`. translate.Projection
// accumulates those into []api.Message; this file owns WHEN one is open, which
// frames reach it, and when it is complete.
//
// # Why completion needs care
//
// session/load is issued inside bridge.Start, which BLOCKS on the result, while
// the replay frames arrive on the bc.Forward goroutine attached just before it.
// The frames precede the result on the wire, so by the time Start returns they
// have all been PUSHED to the bridge's notifCh — but that channel is buffered
// (256), so Forward has not necessarily DRAINED them. Settling the projection
// at Start's return would therefore be a race against a partial transcript.
//
// The barrier this file uses instead needs no timeout and no new bridge API:
//
//	Forward, after processing each frame, settles when
//	  loadDone == true  AND  len(bridge.NotifCh()) == 0
//
// That is sound. Every pre-result frame was pushed before the result, and
// loadDone is set only after the result, so once loadDone is true no pre-result
// push is still outstanding. The channel is FIFO, so an empty channel observed
// from the CONSUMER — after it finished processing a frame — means every one of
// those frames has been consumed. Post-result catalog frames may keep the
// channel non-empty and delay the settle by an iteration, which is harmless.
//
// Forward also settles once more after its range loop ends (the bridge closed
// notifCh), so a load whose trailing frames never arrive still completes rather
// than leaking a projection. A load that never returned leaves loadDone false
// and the projection is discarded, which is right: a failed session/load has no
// transcript to adopt.

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/translate"
)

// loadProjection is one in-flight session/load's accumulating transcript.
// Guarded by Hub.projMu; the fields are not independently safe.
type loadProjection struct {
	proj *translate.Projection
	// frames counts replay frames ingested, for the settle log — a load that
	// projects zero messages from many frames is a decoding bug, and the two
	// numbers together say so.
	frames   int
	loadDone bool
}

// OpenReplayProjection starts a projection for a chat about to session/load.
// A projection already open for that chat is discarded: the only way to reach
// this twice is a re-load (model-switch fallback), whose replay supersedes.
func (h *Hub) OpenReplayProjection(chatID api.ChatID) {
	h.projMu.Lock()
	defer h.projMu.Unlock()
	if h.projections == nil {
		h.projections = make(map[api.ChatID]*loadProjection)
	}
	if _, dup := h.projections[chatID]; dup {
		slog.Debug("replay projection: superseding an open one", "chat_id", chatID)
	}
	h.projections[chatID] = &loadProjection{proj: translate.NewProjection(newMessageID)}
}

// MarkReplayLoadDone records that the session/load RPC has returned, which is
// half of the settle condition. Called from the spawn goroutine.
func (h *Hub) MarkReplayLoadDone(chatID api.ChatID) {
	h.projMu.Lock()
	defer h.projMu.Unlock()
	if lp := h.projections[chatID]; lp != nil {
		lp.loadDone = true
	}
}

// DiscardReplayProjection drops a chat's projection unsettled. Used when the
// load failed, so a half-built transcript cannot be adopted later.
func (h *Hub) DiscardReplayProjection(chatID api.ChatID) {
	h.projMu.Lock()
	defer h.projMu.Unlock()
	if _, open := h.projections[chatID]; open {
		delete(h.projections, chatID)
		slog.Debug("replay projection: discarded", "chat_id", chatID)
	}
}

// ingestReplayFrame folds one replay-tagged frame into the chat's open
// projection. Reports whether a projection consumed it, so the caller can fall
// back to dropping when a replay arrives with no load in flight.
func (h *Hub) ingestReplayFrame(chatID api.ChatID, kind api.ACPUpdateKind, raw json.RawMessage) bool {
	h.projMu.Lock()
	defer h.projMu.Unlock()
	lp := h.projections[chatID]
	if lp == nil {
		return false
	}
	lp.proj.Ingest(kind, raw)
	lp.frames++
	return true
}

// SettleReplayProjection completes a chat's projection when the load has
// returned and every replayed frame has been drained. `buffered` is the
// consumer's own view of the notification channel's remaining depth; see this
// file's header for why that is the sound barrier. `force` settles regardless
// of the channel depth, for the bridge-exit call.
//
// It is a no-op when no projection is open, so callers may call it per frame.
func (h *Hub) SettleReplayProjection(chatID api.ChatID, buffered int, force bool) {
	h.projMu.Lock()
	lp := h.projections[chatID]
	if lp == nil || !lp.loadDone || (buffered > 0 && !force) {
		h.projMu.Unlock()
		return
	}
	delete(h.projections, chatID)
	h.projMu.Unlock()

	msgs := lp.proj.Messages()
	slog.Info("replay projection settled",
		"chat_id", chatID,
		"frames", lp.frames,
		"messages", len(msgs),
		"watermark", lp.proj.Watermark,
		"forced", force)

	if h.onProjection != nil {
		h.onProjection(chatID, msgs, lp.proj.Watermark)
	}
}

// swapProjectedTranscript makes a settled replay the chat's transcript, merged
// with what a replay cannot speak for (see mergeProjection).
//
// It runs on the Forward goroutine, so it must not hold projMu — the store
// mutation below can block, and a replay frame arriving meanwhile needs that
// lock. SettleReplayProjection releases it before calling this.
//
// No broadcast. A load happens on bridge spawn, which a client triggers by
// prompting or opening the chat, and the client fetches messages on
// activation — so the swapped transcript is what the next fetch returns.
// Connected clients that are looking at a stale copy of a chat they did not
// touch will not see the correction until they refetch, which is the same
// window the gap/refetch path already covers.
func (h *Hub) swapProjectedTranscript(chatID api.ChatID, msgs []api.Message, watermark string) {
	var before, after int
	err := h.chatStore.Mutate(h.lifecycle.shutdownCtx, chatID, func(c *api.Chat, exists bool) bool {
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
	// Logged at info with both counts because a SHRINK is the signal worth
	// seeing: it means the replay covered turns the record no longer holds.
	slog.Info("replay projection: transcript swapped",
		"chat_id", chatID, "was", before, "now", after, "projected", len(msgs))
}

// projectionState is the Hub state behind the calls above. Embedded rather than
// spread across Hub's field list so the mutex cannot drift from what it guards.
// Field order is govet fieldalignment's: the pointer-bearing fields first, the
// pointer-free mutex last.
type projectionState struct {
	projections map[api.ChatID]*loadProjection
	// onProjection receives a settled transcript. Nil until the swap lands, so
	// today a settle only logs. Called WITHOUT projMu held: the swap writes the
	// chat store, and holding the projection lock across that would let a store
	// mutation and a replay frame deadlock against each other.
	onProjection func(chatID api.ChatID, msgs []api.Message, watermark string)
	projMu       sync.Mutex
}

// mergeProjection decides the transcript to persist after a replay: the
// projection's messages, plus the ones vibekit holds that a replay cannot
// speak for.
//
// A blind replace is wrong in two directions, and each needs a different rule
// because message identity differs by role:
//
//   - USER and ASSISTANT messages inside the projection's window are the
//     wire's. Note that only the user half has matching ids — KAS echoes back
//     the messageId vibekit sent on session/prompt, while an assistant turn
//     carries KAS's own `<uuid>-say`. So a merge keyed on assistant ids would
//     keep every existing turn AND add the projected one, duplicating the whole
//     transcript. The projection supersedes them wholesale instead.
//   - EVENT messages are not on the replay wire at all. cancelled,
//     model_switched, interrupted and compaction_failed are vibekit's own
//     record of what happened to a turn, so a replace would silently drop every
//     badge on every resume. They are preserved wherever they sit.
//
// Anything newer than the projection's last message is preserved regardless of
// role. That is the un-replayed tail: KAS's log is NOT fsynced (no fsync calls
// in the 2.16.0 bundle, and appendMessagesQuietly swallows a failed persist), so
// a turn vibekit durably holds can legitimately be absent from a replay. Losing
// it to a resume would make the projection strictly worse than the stack it
// replaces.
//
// Ordering is by timestamp, which the projection can only be trusted to produce
// because it takes `_meta.kiro.timestamp` from the wire.
func mergeProjection(existing, projected []api.Message) []api.Message {
	if len(projected) == 0 {
		// Nothing was projected — an empty session, or a decode that produced
		// nothing. Either way the existing record is the better answer.
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

	out := make([]api.Message, 0, len(projected)+len(existing))
	out = append(out, projected...)
	// Indexed rather than ranged by value: api.Message is 216 bytes, which
	// gocritic's rangeValCopy flags.
	for i := range existing {
		if _, dup := projectedIDs[existing[i].ID]; dup {
			continue
		}
		if existing[i].Role == api.RoleEvent || existing[i].Ts > newest {
			out = append(out, existing[i])
		}
	}

	// Stable sort so same-timestamp messages keep the order they were added,
	// which puts a projected message ahead of a preserved one at the same
	// instant — the projected copy is the more complete of the two.
	slices.SortStableFunc(out, func(a, b api.Message) int {
		return cmp.Compare(a.Ts, b.Ts)
	})
	return out
}
