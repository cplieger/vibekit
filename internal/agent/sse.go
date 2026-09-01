package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2/sse"
)

// emit is the single path for broadcasting an event to SSE clients. It
// records a chat_status before publishing (apply-before-publish keeps a
// connect snapshot >= the ring content a new client just replayed), then
// marshals once and hands it to the shared sse hub, which assigns the
// monotonic ID, appends to the replay ring, and fans out to subscribed
// clients (topic = chat ID; events with an empty ChatID are global).

// Broadcast publishes evt to every connected client.
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
		b.chatStatus.ClearAtTurnEnd(evt.ChatID)
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
	slog.Info("SSE connected", "chat_filter", logsafe.Field(string(chatFilter)), "last_event_id", logsafe.Field(lastRaw))

	// A reconnect (any Last-Event-ID) reloads push preferences from disk so
	// settings edited while SSE was down take effect without a restart
	// (deduplicated via singleflight inside the push service).
	if lastRaw != "" && rt.push != nil {
		rt.push.ReloadPreferences(r.Context())
	}

	rt.bus.fanout.Serve(w, r,
		sse.WithTopic(string(chatFilter)),
		sse.OnConnect(func(sw *sse.Writer, b sse.ReplayBounds) error {
			return rt.streamInitialState(sw, b.Floor, b.Head, chatFilter)
		}),
	)
	slog.Info("SSE disconnected", "chat_filter", logsafe.Field(string(chatFilter)))
}

// streamInitialState writes the connected handshake and then replays the
// client's outstanding state — unanswered permission requests and the
// in-flight turn — so a reconnecting browser rebuilds its UI exactly as it
// was. A turn approval rides the permission channel and needs nothing of
// its own. ConnectedPayload carries the ring-buffer floor/head so the client
// can detect a replay gap, and the workspace root, the one server fact the
// client cannot derive and needs before opening a file by relative path.
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

	// Synthesize turn_state for every chat with an OPEN TURN: the in-flight
	// assistant message accumulated so far plus the authoritative busy signal, so
	// a client connecting mid-turn renders the streaming transcript immediately
	// and never has to guess at thinking state.
	//
	// ONE read of the open-turn set serves both replays below, so the busy chats
	// the second one skips are exactly the chats the first one described.
	open := rt.coord.turns.openTurns()
	if err := rt.replayTurnState(writeEvent, chatFilter, open); err != nil {
		return err
	}
	// Then the chats that are NOT busy but are waiting on a person. turn_state
	// cannot carry those: its client handler sets `thinking`, which is false for a
	// chat whose turn has ended.
	return rt.replayWaitingStatus(writeEvent, chatFilter, open)
}

// replayWaitingStatus emits a chat_status event for every chat the agent left
// waiting on a person, skipping chats replayTurnState already covered.
//
// The client renders waiting_on_user as a dot that outlives the turn; without
// this a reader who refreshed, or a second device joining later, saw a blank
// dot on the one chat that actually wanted them. Replays a real chat_status
// rather than stretching turn_state, since turn_state asserts a turn is
// RUNNING — a different claim.
func (rt *Runtime) replayWaitingStatus(
	writeFn func(vibekit.ServerEvent) error,
	chatFilter vibekit.ChatID,
	open map[vibekit.ChatID]openTurnFacts,
) error {
	for id, p := range rt.bus.chatStatus.Snapshot() {
		if chatFilter != "" && id != chatFilter {
			continue
		}
		// A PRIME's chat is skipped here too, even though turn_state withholds it:
		// the turn is genuinely running, so re-asserting a status the agent declared
		// before it would describe the wrong turn.
		if _, busy := open[id]; busy {
			continue
		}
		if p.Status != vibekit.ChatStatusWaitingOnUser {
			continue
		}
		if err := writeFn(vibekit.NewEvent(vibekit.EventChatStatus, id, p)); err != nil {
			return err
		}
	}
	return nil
}

// replayTurnState emits one synthesized turn_state event per chat with an open
// turn. An absent snapshot still goes out as a bare busy signal.
//
// Reading the TURN rather than the prompt slot is what makes an agent-initiated
// turn visible at all — that turn holds no slot. A PRIME turn is never served:
// its frames are a transcript replay vibekit sent itself, so serving them
// would render the preamble as conversation and then lose it on reload.
func (rt *Runtime) replayTurnState(
	writeFn func(vibekit.ServerEvent) error,
	chatFilter vibekit.ChatID,
	open map[vibekit.ChatID]openTurnFacts,
) error {
	for id, facts := range open {
		if chatFilter != "" && id != chatFilter {
			continue
		}
		if facts.Source == vibekit.TurnSourcePrime {
			continue
		}
		status := rt.bus.chatStatus.Get(id)
		payload := vibekit.TurnStatePayload{
			Status:      status.Status,
			Description: status.Description,
		}
		if msg, seq, ok := facts.Buf.Snapshot(); ok {
			payload.Message = &msg
			payload.ChunkSeq = seq
		} else {
			payload.ChunkSeq = seq
		}
		if err := writeFn(vibekit.NewEvent(vibekit.EventTurnState, id, payload)); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingPermissions sends the unresolved permission_needed events to
// a newly connected SSE client, so permission dialogs survive reconnects
// even when the ring buffer has wrapped. EVERY unresolved request is
// replayed, however old: the agent server holds a session/request_permission
// open until answered or cancelled with no deadline of its own, so an old
// card is still a live question. List returns exactly the set
// TakeIfPresent will still accept, in the order the agent asked.
func (rt *Runtime) replayPendingPermissions(writeFn func(vibekit.ServerEvent) error, chatFilter vibekit.ChatID) error {
	for _, evt := range rt.bus.pendingPerms.List(chatFilter) {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}
