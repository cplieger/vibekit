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
	"encoding/json"
	"log/slog"
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

	// STAGED, NOT SWAPPED while onProjection is nil. The projection is not yet
	// the chat's transcript: vibekit still persists its own, and adopting both
	// would double-represent the last turn (the replay carries it AND the
	// .partial recovers it). The swap arrives by filling this sink in the same
	// change that deletes that durability stack; until then the routing and the
	// barrier are exercised for real and a decoding regression is visible in the
	// log before it can reach a transcript.
	if h.onProjection != nil {
		h.onProjection(chatID, msgs, lp.proj.Watermark)
	}
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
