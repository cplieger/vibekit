package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// sseEvent is one serialized outbound message held in the replay buffer.
type sseEvent struct {
	chatID  api.ChatID
	data    []byte
	eventID uint64
}

// sseClient is one connected SSE subscriber.
type sseClient struct {
	ch     chan sseEvent
	cancel context.CancelFunc
	chatID api.ChatID
}

// emit is the single path for broadcasting an event to SSE clients. It
// marshals the event once, assigns a monotonic ID, appends to the replay
// ring buffer, and fans out to subscribed clients.
func (h *Hub) emit(evt api.ServerEvent) {
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("emit marshal", "type", evt.Type, "error", err)
		return
	}
	h.sse.ctrl.emit(evt, data)
}

// parseLastEventID parses the Last-Event-ID header value. Empty or
// malformed values produce 0 (the client either hasn't seen any
// events yet, or the header was mangled by a proxy). Parse failures
// are logged at Debug so an operator tracing a spurious gap-detector
// flash can correlate with upstream header mangling.
func parseLastEventID(raw string) uint64 {
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		slog.Debug("SSE Last-Event-ID parse failed", "raw", raw, "error", err)
		return 0
	}
	return n
}

// handleSSE is the /api/events handler: opens a long-lived server-sent
// events stream for the connected browser.
func (h *Hub) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Gate new SSE connections once Shutdown has flipped draining.
	// Without this a last-minute reconnect can register in sseClients
	// after Shutdown's client-cancel loop has already run, leaving a
	// goroutine holding the ResponseWriter past hub teardown.
	if h.lifecycle.draining.Load() {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, api.ErrorJSON("shutting down"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		api.InternalError(w, errors.New("streaming not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	// no-transform is RFC 7234 §5.2.2.4: intermediaries MUST NOT transform
	// the body. Caddy's encode middleware honors this (isEncodeAllowed in
	// modules/caddyhttp/encode/encode.go), as do nginx, ALB, and Cloudflare.
	// Without it, a compressing proxy may wrap SSE in gzip, which buffers
	// per-event flushes and breaks live event delivery on some clients.
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	chatFilter := api.ChatID(r.URL.Query().Get("chat_id"))
	sc := &sseClient{
		ch:     make(chan sseEvent, 256),
		cancel: cancel,
		chatID: chatFilter,
	}

	// Replay events since Last-Event-ID, if provided. This makes SSE
	// resilient across transient network drops.
	lastID := parseLastEventID(r.Header.Get("Last-Event-ID"))

	h.sse.ctrl.add(sc)
	h.replaySinceLastID(ctx, sc, lastID, chatFilter)

	defer func() {
		h.sse.ctrl.remove(sc)
	}()

	slog.Info("SSE connected", "chat_filter", chatFilter, "last_event_id", lastID)

	// Send the connected handshake plus a replay of all pending
	// per-client state (permissions, staged writes, per-turn trust).
	// Returns false if the client disconnected mid-replay or the
	// handshake failed to marshal.
	if !h.streamInitialState(ctx, w, flusher, chatFilter) {
		return
	}

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("SSE disconnected", "chat_filter", chatFilter)
			return
		case e := <-sc.ch:
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.eventID, e.data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// replaySinceLastID replays buffered events newer than lastID to a
// reconnecting client. A lastID of 0 (first connect or a header mangled
// by a proxy) is a no-op. On reconnect it also reloads push preferences
// from disk so settings edited while SSE was down take effect without a
// restart (deduplicated via singleflight).
func (h *Hub) replaySinceLastID(ctx context.Context, sc *sseClient, lastID uint64, chatFilter api.ChatID) {
	if lastID == 0 {
		return
	}
	if h.push != nil {
		h.push.ReloadPreferences(ctx)
	}
	for _, e := range h.sse.ctrl.replay.Replay(lastID, chatFilter) {
		select {
		case sc.ch <- e:
		default:
		}
	}
}

// streamInitialState writes the connected handshake and then replays the
// client's outstanding state — pending permissions, staged Supervised
// writes, and per-turn trust — so a reconnecting browser rebuilds its UI
// exactly as it was. It returns false (caller should stop) when the
// handshake fails to marshal or the connection is cancelled mid-replay.
//
// The handshake's ConnectedPayload carries the ring-buffer floor (oldest
// replayable event ID) and head (newest) so the client can detect a
// replay gap: a last-seen ID below the floor means events were lost and
// it must refetch authoritative state.
func (h *Hub) streamInitialState(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, chatFilter api.ChatID) bool {
	floor, head := h.replayBounds()
	connectedEvt := api.NewEvent(api.EventConnected, "", api.ConnectedPayload{Floor: floor, Head: head})
	connectedData, err := json.Marshal(connectedEvt)
	if err != nil {
		slog.Error("marshal connected event", "error", err)
		return false
	}
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", head, connectedData)
	flusher.Flush()

	// Replay any pending permissions that may have fallen out of the
	// ring buffer. This ensures permission dialogs survive reconnects.
	h.replayPendingPermissions(w, flusher, chatFilter)
	if ctx.Err() != nil {
		return false
	}

	// writeEvent serializes one event to the stream; shared by the
	// staged-change and per-turn-trust replays below.
	writeEvent := func(evt api.ServerEvent) {
		if data, err := json.Marshal(evt); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}

	// Replay every outstanding Supervised-mode staged op so the client
	// rebuilds its pending pill and per-card Accept/Reject buttons
	// exactly as they were before the disconnect.
	h.replayPendingChanges(writeEvent, chatFilter)
	if ctx.Err() != nil {
		return false
	}

	// Replay per-turn trust state. Without this, a reconnect mid-turn
	// silently reverts the Supervised pill to plain "Supervised" even
	// though the perTurnTrust flag is still active — the user would have
	// no way to tell their trust decision was still in force.
	h.replayPendingTrust(writeEvent, chatFilter)
	if ctx.Err() != nil {
		return false
	}

	flusher.Flush()
	return true
}

// replayBounds returns (floor, head) of the current replay buffer, both
// inclusive. Floor is the oldest event ID still replayable; head is the
// newest. When the buffer is empty, floor == 0 and head == current seq.
// Clients with last-seen-id < floor know they missed events.
func (h *Hub) replayBounds() (floor, head uint64) {
	return h.sse.ctrl.bounds()
}

// replayPendingPermissions sends any unresolved permission_needed events
// to a newly connected SSE client. Ensures permission dialogs survive
// reconnects even if the ring buffer has wrapped.
func (h *Hub) replayPendingPermissions(w http.ResponseWriter, flusher http.Flusher, chatFilter api.ChatID) {
	perms := h.sse.pendingPerms.List(chatFilter)
	for _, evt := range perms {
		if data, err := json.Marshal(evt); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}
	flusher.Flush()
}

// replayPendingChanges sends every outstanding pending op for a new
// SSE client. Called from handleSSE after replayPendingPermissions.
// The replay uses pending_change_added events so client handlers are
// identical to the live path.
func (h *Hub) replayPendingChanges(writeFn func(api.ServerEvent), chatFilter api.ChatID) {
	var chatIDs []api.ChatID
	if chatFilter != "" {
		chatIDs = []api.ChatID{chatFilter}
	} else {
		chatIDs = h.listChatIDsWithPending()
	}
	for _, id := range chatIDs {
		for _, snap := range h.perm.pending.ListForChat(id) {
			writeFn(api.NewEvent(api.EventPendingChangeAdded, id, api.PendingChangeAddedPayload{Change: snap}))
		}
	}
}

// replayPendingTrust emits a pending_trust_enabled event for every
// chat that currently has perTurnTrust set. Called from handleSSE
// after replayPendingChanges. Keeps the Supervised pill's "Trusted ·
// this turn" state alive across reconnects.
func (h *Hub) replayPendingTrust(writeFn func(api.ServerEvent), chatFilter api.ChatID) {
	ids := h.perm.supervised.TrustedChatIDs(chatFilter)
	for _, id := range ids {
		writeFn(api.NewEvent(api.EventPendingTrustEnabled, id, api.PendingTrustEnabledPayload{}))
	}
}
