package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/sse"
)

// emit is the single path for broadcasting an event to SSE clients. It
// records a chat_status before publishing (apply-before-publish is what keeps a
// connect snapshot >= the ring content a new client just replayed), then
// marshals once and hands it to the shared sse hub, which assigns the monotonic
// ID, appends to the replay ring, and fans out to subscribed clients
// (topic = chat ID; events with an empty ChatID are global).
//
// It used to fold EVERY event into a turn mirror that rebuilt the in-flight
// assistant message in parallel with buffer.Buffer. That replica is gone: the
// buffer already holds the turn and now snapshots it (buffer.Buffer.Snapshot),
// so the only thing left to record here is the status, which lives on no
// message and in no replay.
// Broadcast publishes evt to every connected client. This is the whole event
// bus contract, and it is named Broadcast rather than emit because four consumer
// interfaces spell it that way; it used to be a 3-line Runtime forward to emit,
// which put the runtime in the path of every event in the app.
func (b *bus) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	b.emit(evt)
}

// PendingPermsAdd registers an unanswered decision so a reconnecting client gets
// it replayed.
func (b *bus) PendingPermsAdd(requestID int64, evt vibekit.ServerEvent) {
	b.pendingPerms.Add(requestID, evt)
}

func (b *bus) emit(evt vibekit.ServerEvent) {
	switch evt.Type {
	case vibekit.EventChatStatus:
		if p, ok := evt.Payload.(vibekit.ChatStatusPayload); ok {
			b.chatStatus.Set(evt.ChatID, p)
		}
	case vibekit.EventTurnEnded:
		b.chatStatus.Clear(evt.ChatID)
	}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("emit marshal", "type", evt.Type, "error", err)
		return
	}
	b.fanout.Publish(sse.Event{Topic: string(evt.ChatID), Data: data})
}

// handleSSE is the /api/events handler: opens a long-lived server-sent
// events stream for the connected browser. The transport (headers,
// Last-Event-ID replay, keepalives, slow-client eviction, drain gate) is
// the sse library's; vibekit owns the draining envelope, the connected
// handshake carrying the replay bounds, and the initial per-client state
// replay (pending permissions, staged writes, per-turn trust).
func (rt *Runtime) handleSSE(w http.ResponseWriter, r *http.Request) {
	chatFilter := vibekit.ChatID(r.URL.Query().Get("chat_id"))
	lastRaw := r.Header.Get("Last-Event-ID")
	slog.Info("SSE connected", "chat_filter", chatFilter, "last_event_id", lastRaw)

	// A reconnect (any Last-Event-ID) reloads push preferences from disk so
	// settings edited while SSE was down take effect without a restart
	// (deduplicated via singleflight inside the push service).
	if lastRaw != "" && rt.push != nil {
		rt.push.ReloadPreferences(r.Context())
	}

	rt.bus.fanout.Serve(w, r,
		sse.WithTopic(string(chatFilter)),
		sse.OnConnect(func(sw *sse.Writer, floor, head uint64) error {
			return rt.streamInitialState(sw, floor, head, chatFilter)
		}),
	)
	slog.Info("SSE disconnected", "chat_filter", chatFilter)
}

// streamInitialState writes the connected handshake and then replays the
// client's outstanding state — the unanswered permission requests and the
// in-flight turn — so a reconnecting browser rebuilds its UI exactly as it
// was. A turn approval rides the permission channel, so it replays with the
// rest and needs nothing of its own.
//
// The handshake's ConnectedPayload carries the ring-buffer floor (oldest
// replayable event ID) and head (newest) so the client can detect a replay
// gap: a last-seen ID below the floor means events were lost and it must
// refetch authoritative state. It also carries the workspace root, the one
// server fact the client cannot derive and needs before it can open a file
// named by a relative path. The hook runs after the library's
// Last-Event-ID replay, so the bounds are consistent with what the client
// has already received.
func (rt *Runtime) streamInitialState(sw *sse.Writer, floor, head uint64, chatFilter vibekit.ChatID) error {
	connectedEvt := vibekit.NewEvent(vibekit.EventConnected, "", vibekit.ConnectedPayload{
		Workspace: rt.lifecycle.workDir,
		Floor:     floor,
		Head:      head,
	})
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
	writeEvent := func(evt vibekit.ServerEvent) error {
		data, err := json.Marshal(evt)
		if err != nil {
			return nil //nolint:nilerr // skip unmarshalable event, keep stream
		}
		return sw.Event(0, "", data)
	}

	// Replay any pending permissions that may have fallen out of the ring
	// buffer, so permission dialogs survive reconnects.
	if err := rt.replayPendingPermissions(writeEvent, chatFilter); err != nil {
		return err
	}

	// There is no staged-op or per-turn-trust replay. Both belonged to vibekit's
	// own staging queue. A turn approval IS a permission request, so the replay
	// above already covers it — which is the same reason it needed no new event.

	// Synthesize turn_state for every busy chat (P6): the in-flight
	// assistant message accumulated so far plus the authoritative
	// busy signal, so a client connecting mid-turn renders the
	// streaming transcript immediately and never has to guess at
	// thinking state.
	return rt.replayTurnState(writeEvent, chatFilter)
}

// replayTurnState emits one synthesized turn_state event per busy chat
// (bridge holding the prompt slot). The snapshot may be absent for a turn that
// hasn't produced content yet — the event still goes out as a bare busy signal
// (the client sets thinking without touching messages). Gating on the prompting
// state, not on snapshot presence, is what keeps a finished turn from ever being
// resurrected: an idle chat is never replayed.
//
// The in-flight message comes straight from the chat's assistant buffer, which
// is the same object the live stream and the turn-end persist read. There is no
// separate replica to drift from it.
func (rt *Runtime) replayTurnState(writeFn func(vibekit.ServerEvent) error, chatFilter vibekit.ChatID) error {
	for _, id := range rt.bridge.mgr.promptingChatIDs() {
		if chatFilter != "" && id != chatFilter {
			continue
		}
		status := rt.bus.chatStatus.Get(id)
		payload := vibekit.TurnStatePayload{
			Status:      status.Status,
			Description: status.Description,
		}
		if buf := rt.bridge.assistantBufs.Get(id); buf != nil {
			if msg, seq, ok := buf.Snapshot(); ok {
				payload.Message = &msg
				payload.ChunkSeq = seq
			} else {
				payload.ChunkSeq = seq
			}
		}
		if err := writeFn(vibekit.NewEvent(vibekit.EventTurnState, id, payload)); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingPermissions sends the unresolved permission_needed events to
// a newly connected SSE client, so permission dialogs survive reconnects
// even when the ring buffer has wrapped.
//
// EVERY unresolved request is replayed, however old. A reconnect can happen long
// after the request was raised, and on vibekit's stdio transport the agent server
// holds a session/request_permission open until it is answered or the turn is
// cancelled — it applies no deadline of its own (measured on 2.18.0; see
// pendingPermsTracker for the two read sites and why they are not a wall clock).
// So an old card is still a live question, and skipping it would strand the turn
// waiting for an answer no surface is offering any more. List returns exactly the
// set TakeIfPresent will still accept an answer for, so the card a client is
// shown and the answer the server will take cannot disagree. The ORDER is List's
// too — ascending request id, i.e. the order the agent asked — so this loop
// writes the queue rather than a set.
func (rt *Runtime) replayPendingPermissions(writeFn func(vibekit.ServerEvent) error, chatFilter vibekit.ChatID) error {
	for _, evt := range rt.bus.pendingPerms.List(chatFilter) {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}
