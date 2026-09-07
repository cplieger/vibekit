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

// emit records chat_status BEFORE publishing, so a connect snapshot is never
// behind the ring content a new client just replayed.

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

// handleSSE opens the /api/events stream. The sse library owns the transport
// (headers, Last-Event-ID replay, keepalives, slow-client eviction); vibekit owns
// the connected handshake and the initial per-client state replay.
func (rt *Runtime) handleSSE(w http.ResponseWriter, r *http.Request) {
	chatFilter := vibekit.ChatID(r.URL.Query().Get("chat_id"))
	lastRaw := adoptCursorParam(r)
	slog.Info("SSE connected", "chat_filter", logsafe.Field(string(chatFilter)), "last_event_id", logsafe.Field(lastRaw))

	// A reconnect reloads push preferences from disk so settings edited while SSE
	// was down take effect without a restart.
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

// cursorParam is the query spelling of Last-Event-ID, for the resume the browser
// cannot ask for.
const cursorParam = "last_event_id"

// adoptCursorParam resolves the replay cursor, promoting ?last_event_id= into the
// Last-Event-ID header when the header is absent. The header WINS: only the browser
// knows what its own EventSource last delivered. Promoted rather than parsed so the
// sse library stays the one replay implementation; digits only, so nothing mangled
// reaches the parser or the log.
func adoptCursorParam(r *http.Request) string {
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		return raw
	}
	raw := r.URL.Query().Get(cursorParam)
	if raw == "" || len(raw) > maxCursorDigits {
		return ""
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return ""
		}
	}
	r.Header.Set("Last-Event-ID", raw)
	return raw
}

// maxCursorDigits bounds the parameter at what a uint64 can spell.
const maxCursorDigits = 20

// streamInitialState writes the connected handshake, then replays this client's
// outstanding state so a reconnecting browser rebuilds its UI as it was.
// ConnectedPayload carries the ring floor/head so the client can detect a replay
// gap, plus the workspace root, the one server fact the client cannot derive.
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

	// No id: replayed state is synthesized, not part of the event sequence.
	writeEvent := func(evt vibekit.ServerEvent) error {
		data, err := json.Marshal(evt)
		if err != nil {
			return nil //nolint:nilerr // skip unmarshalable event, keep stream
		}
		return sw.Event(0, "", data)
	}

	if err := rt.replayPendingPermissions(writeEvent, chatFilter); err != nil {
		return err
	}

	// Beside the permissions rather than folded into them: the two registries have
	// different lifetimes (run_ask.go), and a parked run has no deadline of its own.
	if err := rt.replayPendingRunAsks(writeEvent, chatFilter); err != nil {
		return err
	}

	// ONE read of the open-turn set serves both replays below, so the busy chats
	// the second one skips are exactly the chats the first one described.
	open := rt.coord.turns.openTurns()
	if err := rt.replayTurnState(writeEvent, chatFilter, open); err != nil {
		return err
	}
	// turn_state cannot carry a chat that is waiting on a person: its client
	// handler sets `thinking`, false once the turn has ended.
	return rt.replayWaitingStatus(writeEvent, chatFilter, open)
}

// replayWaitingStatus emits a chat_status event for every chat the agent left
// waiting on a person, skipping chats replayTurnState already covered. A real
// chat_status rather than a stretched turn_state, which asserts a turn is RUNNING.
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
		// its turn is genuinely running, so an older status describes the wrong turn.
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
// turn; an absent snapshot still goes out as a bare busy signal. Reading the TURN
// rather than the prompt slot is what makes an agent-initiated turn visible at all.
// A PRIME turn is never served: its frames are a transcript replay vibekit sent
// itself, so serving them would render the preamble as conversation.
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
			// Emitted AND marked: the snapshot is the only copy of the in-flight step
			// transcript, but unmarked it makes the launching chat read as busy.
			WorkflowStep: facts.Source == vibekit.TurnSourceWorkflowStep,
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

// replayPendingPermissions sends the unresolved permission_needed events to a newly
// connected client, so dialogs survive a reconnect that outlived the ring buffer.
// EVERY unresolved request goes, however old: the agent server holds
// session/request_permission open until answered, so an old card is a live question.
func (rt *Runtime) replayPendingPermissions(writeFn func(vibekit.ServerEvent) error, chatFilter vibekit.ChatID) error {
	for _, evt := range rt.bus.pendingPerms.List(chatFilter) {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingRunAsks sends every unanswered workflow-step question to a newly
// connected client, so a reload, a second device and a transport gap converge on the
// same set. The client's dock de-duplicates by ask id, which is what lets a
// `transport:gap` clear eagerly and be followed by this burst.
func (rt *Runtime) replayPendingRunAsks(writeFn func(vibekit.ServerEvent) error, chatFilter vibekit.ChatID) error {
	for _, evt := range rt.runs.asks.List(chatFilter) {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}
