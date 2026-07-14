package hub

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/webhttp/sse"
)

// emit is the single path for broadcasting an event to SSE clients. It
// marshals the event once and hands it to the shared sse hub, which assigns
// the monotonic ID, appends to the replay ring, and fans out to subscribed
// clients (topic = chat ID; events with an empty ChatID are global).
func (h *Hub) emit(evt api.ServerEvent) {
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("emit marshal", "type", evt.Type, "error", err)
		return
	}
	h.sse.hub.Publish(sse.Event{Topic: string(evt.ChatID), Data: data})
}

// handleSSE is the /api/events handler: opens a long-lived server-sent
// events stream for the connected browser. The transport (headers,
// Last-Event-ID replay, keepalives, slow-client eviction, drain gate) is
// the sse library's; vibekit owns the draining envelope, the connected
// handshake carrying the replay bounds, and the initial per-client state
// replay (pending permissions, staged writes, per-turn trust).
func (h *Hub) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Gate new SSE connections once Shutdown has flipped draining, with
	// vibekit's own envelope (the library's drain gate 503s as a backstop
	// after hub.Shutdown, closing the last-instant-reconnect race).
	if h.lifecycle.draining.Load() {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, api.ErrorJSON("shutting down"))
		return
	}

	chatFilter := api.ChatID(r.URL.Query().Get("chat_id"))
	lastRaw := r.Header.Get("Last-Event-ID")
	slog.Info("SSE connected", "chat_filter", chatFilter, "last_event_id", lastRaw)

	// A reconnect (any Last-Event-ID) reloads push preferences from disk so
	// settings edited while SSE was down take effect without a restart
	// (deduplicated via singleflight inside the push service).
	if lastRaw != "" && h.push != nil {
		h.push.ReloadPreferences(r.Context())
	}

	h.sse.hub.Serve(w, r,
		sse.WithTopic(string(chatFilter)),
		sse.OnConnect(func(sw *sse.Writer, floor, head uint64) error {
			return h.streamInitialState(sw, floor, head, chatFilter)
		}),
	)
	slog.Info("SSE disconnected", "chat_filter", chatFilter)
}

// streamInitialState writes the connected handshake and then replays the
// client's outstanding state — pending permissions, staged Supervised
// writes, and per-turn trust — so a reconnecting browser rebuilds its UI
// exactly as it was.
//
// The handshake's ConnectedPayload carries the ring-buffer floor (oldest
// replayable event ID) and head (newest) so the client can detect a replay
// gap: a last-seen ID below the floor means events were lost and it must
// refetch authoritative state. The hook runs after the library's
// Last-Event-ID replay, so the bounds are consistent with what the client
// has already received.
func (h *Hub) streamInitialState(sw *sse.Writer, floor, head uint64, chatFilter api.ChatID) error {
	connectedEvt := api.NewEvent(api.EventConnected, "", api.ConnectedPayload{Floor: floor, Head: head})
	connectedData, err := json.Marshal(connectedEvt)
	if err != nil {
		slog.Error("marshal connected event", "error", err)
		return errors.New("marshal connected event")
	}
	if err := sw.Event(head, "", connectedData); err != nil {
		return err
	}

	// writeEvent serializes one replayed state event to the stream (no id:
	// replayed state is synthesized, not part of the event sequence).
	writeEvent := func(evt api.ServerEvent) error {
		data, err := json.Marshal(evt)
		if err != nil {
			return nil //nolint:nilerr // skip unmarshalable event, keep stream
		}
		return sw.Event(0, "", data)
	}

	// Replay any pending permissions that may have fallen out of the ring
	// buffer, so permission dialogs survive reconnects.
	if err := h.replayPendingPermissions(writeEvent, chatFilter); err != nil {
		return err
	}

	// Replay every outstanding Supervised-mode staged op so the client
	// rebuilds its pending pill and per-card Accept/Reject buttons.
	if err := h.replayPendingChanges(writeEvent, chatFilter); err != nil {
		return err
	}

	// Replay per-turn trust state. Without this, a reconnect mid-turn
	// silently reverts the Supervised pill to plain "Supervised" even
	// though the perTurnTrust flag is still active.
	return h.replayPendingTrust(writeEvent, chatFilter)
}

// replayBounds returns (floor, head) of the current replay buffer, both
// inclusive. Floor is the oldest event ID still replayable; head is the
// newest. Clients with last-seen-id < floor know they missed events.
func (h *Hub) replayBounds() (floor, head uint64) {
	return h.sse.hub.Bounds()
}

// replayPendingPermissions sends any unresolved permission_needed events to
// a newly connected SSE client, so permission dialogs survive reconnects
// even when the ring buffer has wrapped.
func (h *Hub) replayPendingPermissions(writeFn func(api.ServerEvent) error, chatFilter api.ChatID) error {
	for _, evt := range h.sse.pendingPerms.List(chatFilter) {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingChanges sends every outstanding pending op for a new SSE
// client. The replay uses pending_change_added events so client handlers
// are identical to the live path.
func (h *Hub) replayPendingChanges(writeFn func(api.ServerEvent) error, chatFilter api.ChatID) error {
	var chatIDs []api.ChatID
	if chatFilter != "" {
		chatIDs = []api.ChatID{chatFilter}
	} else {
		chatIDs = h.listChatIDsWithPending()
	}
	for _, id := range chatIDs {
		for _, snap := range h.perm.pending.ListForChat(id) {
			if err := writeFn(api.NewEvent(api.EventPendingChangeAdded, id, api.PendingChangeAddedPayload{Change: snap})); err != nil {
				return err
			}
		}
	}
	return nil
}

// replayPendingTrust emits a pending_trust_enabled event for every chat
// that currently has perTurnTrust set, keeping the Supervised pill's
// "Trusted · this turn" state alive across reconnects.
func (h *Hub) replayPendingTrust(writeFn func(api.ServerEvent) error, chatFilter api.ChatID) error {
	for _, id := range h.perm.supervised.TrustedChatIDs(chatFilter) {
		if err := writeFn(api.NewEvent(api.EventPendingTrustEnabled, id, api.PendingTrustEnabledPayload{})); err != nil {
			return err
		}
	}
	return nil
}
