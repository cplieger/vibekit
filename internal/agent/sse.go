package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/sse"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// wireHeader is the request header a v3 client sends; its absence marks a legacy
// (v2 bundle) connect, which still needs the numeric floor/head on `connected`
// and the per-item pending replay.
const wireHeader = "SSE-Wire"

// clientTagHeader carries the client's tag for the hub's presence table.
const clientTagHeader = "SSE-Client"

// Broadcast publishes evt to every connected client.
func (b *bus) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	b.emit(evt)
}

// PendingPermsAdd registers an unanswered decision so a reconnecting client gets
// it replayed.
func (b *bus) PendingPermsAdd(requestID int64, evt vibekit.ServerEvent) {
	b.pendingPerms.Add(requestID, evt)
}

// emit is the single publish path for live frames. It touches evt.Subject in
// exactly one case: a chat_status frame takes the stamp MergeStamped minted in the
// same critical section as the payload it publishes.
func (b *bus) emit(evt vibekit.ServerEvent) {
	switch evt.Type {
	case vibekit.EventChatStatus:
		// A producer of this event may NOT hold chatLifecycle.mu: stageStatusDesc takes it
		// and sync.Mutex is not reentrant, so that is a self-deadlock -race hangs on
		// rather than reports.
		if p, ok := evt.Payload.(vibekit.ChatStatusPayload); ok {
			// The RAW description, before the merge: Turn.statusDesc is what the agent
			// declared during THIS turn, and stageStatusDescription drops an empty one.
			b.stageStatusDesc(evt.ChatID, p.Description)
			// The MERGED payload on the wire: the client replaces both fields, so a
			// description-only declaration would delete a retained waiting_on_user there.
			// evt is a value, so this reaches the marshal below and no caller sees it.
			evt.Payload, evt.Subject = b.chatStatus.MergeStamped(evt.ChatID, p)
		}
	case vibekit.EventTurnEnded:
		b.chatStatus.ClearAtTurnEnd(evt.ChatID)
	}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("emit marshal", "type", evt.Type, "error", err)
		return
	}
	_, err = b.fanout.Publish(sse.Event{Topic: string(evt.ChatID), Data: data})
	if err == nil {
		return
	}
	if !errors.Is(err, sse.ErrFrameTooLarge) {
		slog.Error("emit publish", "type", evt.Type, "error", err)
		return
	}
	// The hub refused the frame whole. A stamped frame is not lost: subject_changed
	// carries the same stamp as a fetch instruction, and the client refetches the
	// subject through its action. An unstamped frame that large has no fetch to
	// substitute, so it is dropped and the log is the only signal.
	if evt.Subject == nil {
		slog.Error("emit: frame exceeds the hub's cap and carries no subject; dropped",
			"type", evt.Type, "chat_id", evt.ChatID, "bytes", len(data), "cap", sse.MaxFrameBytes)
		return
	}
	slog.Warn("emit: frame exceeds the hub's cap; publishing subject_changed instead",
		"type", evt.Type, "chat_id", evt.ChatID, "bytes", len(data), "cap", sse.MaxFrameBytes,
		"kind", evt.Subject.Kind, "ref", logsafe.Field(evt.Subject.Ref))
	substitute := vibekit.NewEvent(vibekit.EventSubjectChanged, evt.ChatID, vibekit.SubjectChangedPayload{})
	substitute.Subject = evt.Subject
	data, err = json.Marshal(substitute)
	if err != nil {
		slog.Error("emit marshal", "type", substitute.Type, "error", err)
		return
	}
	if _, err := b.fanout.Publish(sse.Event{Topic: string(evt.ChatID), Data: data}); err != nil {
		slog.Error("emit publish", "type", substitute.Type, "error", err)
	}
}

// handleSSE opens the /api/events stream. The sse library owns the transport
// (headers, the hello, Last-Event-ID replay, keepalives, slow-client eviction);
// vibekit owns the connected handshake and the initial state the client cannot
// derive from the event log.
func (rt *Runtime) handleSSE(w http.ResponseWriter, r *http.Request) {
	legacy := r.Header.Get(wireHeader) == ""
	tag := r.Header.Get(clientTagHeader)
	client := "v3"
	if legacy {
		client = "legacy"
		rt.bus.legacyConnects.Add(1)
	} else {
		rt.bus.v3Connects.Add(1)
	}
	slog.Info("SSE connected", "client", client,
		"last_event_id", logsafe.Field(r.Header.Get("Last-Event-ID")))

	// A reconnect reloads push preferences from disk so settings edited while SSE
	// was down take effect without a restart.
	if r.Header.Get("Last-Event-ID") != "" && rt.push != nil {
		rt.push.ReloadPreferences(r.Context())
	}

	rt.bus.fanout.Serve(armedWriter(&rt.bus.closeAfter, w), r,
		sse.WithClientTag(tag),
		sse.OnConnect(func(sw *sse.Writer, h sse.Hello) error {
			return rt.streamInitialState(sw, h, legacy)
		}),
	)
	slog.Info("SSE disconnected", "client", client)
}

// The connect payload's two list bounds. POLICY numbers derived from what a
// realistic reconnect must CARRY, so the gate over them fails rather than being
// raised: a connect that exceeds one is the defect, never the constant.
const (
	// maxBusyChats bounds the busy-chat list the handshake carries. A chat id is 34
	// characters, so one JSON array element is 37 bytes with its quotes and comma:
	// 256 × 37 ≈ 9.5 KiB. Deliberately far past every other bound in the system — one
	// bridge per chat at ~300 MB of process tree — so it is a ceiling on the BYTES
	// rather than a limit anyone reaches.
	maxBusyChats = 256
	// maxConnectLiveRuns bounds the live-run inventory the handshake carries. A row is
	// a wf_<16 hex> id, a c-<32 hex> chat id and a bool, so ~100 bytes of JSON:
	// 128 × 100 ≈ 12.8 KiB. The single-run rule bounds concurrent runs to a handful in
	// practice, but that is a PRODUCT rule and not a bound on this array — a
	// stale-lease accumulation is exactly what inflates it.
	maxConnectLiveRuns = 128
)

// liveTurnGETCaps bounds the in-flight turn the transcript GET carries, and every
// dimension is sized ABOVE the measured maximum so the ordinary turn is not cut at all.
//
// The reader's need on THIS channel is the WHOLE turn, not its tail. This is the one
// channel that carries the newest turn while it is in flight, and the newest turn is
// served whole unconditionally — so a cap that keeps a tail is a cap that withholds the
// reply a reader came for.
//
// Measured maxima over the live chat volume, one per dimension, so the sizing is
// checkable rather than asserted. What survives is a RUNAWAY ceiling of
// MaxTextBytes() = 10,616,832 bytes: past it the turn is cut and `truncated` says so.
// ToolOutputTotalBytes is what makes that number statable — the per-call cap stays at the
// terminal ring buffer's own 64 KiB bound, so a single call is never cut, and the
// aggregate bounds the product the per-call cap cannot.
//
// Deliberately NOT narrowed by a remaining budget: that GET serves ONE chat, so there is
// no fanout to divide. Its cost is not charged against the caller's own ?max_bytes=
// either (internal/chat's serveChatMessages): that budget bounds the WINDOW, and this cap
// bounds the live turn, as two independent bounds on one response.
var liveTurnGETCaps = buffer.SnapshotCaps{
	ReasoningBytes:       1 << 20,   // > max 774,867
	ContentBytes:         128 << 10, // > max 71,191
	BlockTextBytes:       1 << 20,   // > max 804,520
	Blocks:               8192,      // > max 3,548
	ToolCalls:            4096,      // > max 2,804
	ToolOutputBytes:      64 << 10,  // == the terminal ring buffer's own bound
	ToolOutputTotalBytes: 8 << 20,   // > max sum 3,467,593
}

// streamInitialState is the OnConnect body: the connected handshake, then the
// workspace-wide state a client cannot derive from the event log. A v3 connect gets
// two aggregate frames, pending_snapshot and status_snapshot, each the WHOLE set
// (possibly empty) plus its stamp; a legacy connect gets the per-item replay its
// decoders know and numeric floor/head on `connected`. Every frame is id-less and
// unnamed: the v2 bundle reads through onmessage, which a named event never reaches.
// `connected` carries no Subject: one scalar stamp cannot vouch for several subjects.
func (rt *Runtime) streamInitialState(sw *sse.Writer, h sse.Hello, legacy bool) error {
	busy := rt.coord.turns.busyChatIDs()
	// An over-cap list is withheld rather than truncated: on either the client
	// retracts nothing.
	busyStated := len(busy) <= maxBusyChats
	if !busyStated {
		slog.Warn("connect busy-chat list withheld: over cap", "cap", maxBusyChats, "count", len(busy))
		busy = nil
	}
	liveRuns := rt.runs.liveRunRows()
	liveRunsStated := len(liveRuns) <= maxConnectLiveRuns
	if !liveRunsStated {
		slog.Warn("connect live-run inventory withheld: over cap",
			"cap", maxConnectLiveRuns, "count", len(liveRuns))
		liveRuns = nil
	}
	connected := vibekit.ConnectedPayload{
		Workspace:      rt.lifecycle.workDir,
		BusyChats:      busy,
		LiveRuns:       liveRuns,
		BusyStated:     busyStated,
		LiveRunsStated: liveRunsStated,
	}
	if legacy {
		// The v2 bundle's gap arithmetic reads numbers: floor 0 means "not resumed,
		// refetch", which is what the hello's Resumed already decided.
		var floor uint64
		if h.Resumed {
			floor = h.Floor
		}
		head := h.Head
		connected.Floor, connected.Head = &floor, &head
	}
	writeEvent := func(evt vibekit.ServerEvent) error {
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("connect: marshal frame", "type", evt.Type, "error", err)
			return nil //nolint:nilerr // skip the unmarshalable frame, keep the stream
		}
		return sw.Event("", data)
	}
	if err := writeEvent(vibekit.NewEvent(vibekit.EventConnected, "", connected)); err != nil {
		return err
	}
	if legacy {
		return rt.replayLegacyState(writeEvent)
	}
	pending, pendingStamp := rt.pendingSnapshotStamped()
	pendingFrame := vibekit.NewEvent(vibekit.EventPendingSnapshot, "", pending)
	pendingFrame.Subject = pendingStamp
	if err := writeEvent(pendingFrame); err != nil {
		return err
	}
	status, statusStamp := rt.bus.chatStatus.SnapshotStamped(rt.coord.turns.openTurns())
	statusFrame := vibekit.NewEvent(vibekit.EventStatusSnapshot, "", status)
	statusFrame.Subject = statusStamp
	return writeEvent(statusFrame)
}

// replayLegacyState is the per-item replay a v2 bundle's decoders know: every
// pending permission, run ask and steer, then every retained waiting status for a
// chat that is not busy. Kept for the life of the last v2 bundle.
func (rt *Runtime) replayLegacyState(writeEvent func(vibekit.ServerEvent) error) error {
	if err := rt.replayPendingPermissions(writeEvent); err != nil {
		return err
	}
	// Beside the permissions rather than folded into them: the two registries have
	// different lifetimes (run_ask.go), and a parked run has no deadline of its own.
	if err := rt.replayPendingRunAsks(writeEvent); err != nil {
		return err
	}
	// The steering buffer, for the same reason and on the same terms: KAS holds it,
	// nothing can read it back, and the gap door empties the client's dock without
	// promoting anything. See replayPendingSteers for what it cannot recover.
	if err := rt.replayPendingSteers(writeEvent); err != nil {
		return err
	}
	return rt.replayWaitingStatus(writeEvent, rt.coord.turns.openTurns())
}

// replayWaitingStatus emits a chat_status event for every chat the agent left
// waiting on a person and whose turn is not running. Keyed on `open` because a
// chat whose turn is running must still suppress a stale waiting_on_user.
func (rt *Runtime) replayWaitingStatus(
	writeFn func(vibekit.ServerEvent) error,
	open map[vibekit.ChatID]openTurnFacts,
) error {
	for id, p := range rt.bus.chatStatus.Snapshot() {
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

// replayPendingPermissions sends the unresolved permission_needed events to a newly
// connected client, so dialogs survive a reconnect that outlived the ring buffer.
// EVERY unresolved request goes, however old: the agent server holds
// session/request_permission open until answered, so an old card is a live question.
func (rt *Runtime) replayPendingPermissions(writeFn func(vibekit.ServerEvent) error) error {
	for _, evt := range rt.bus.pendingPerms.List("") {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingRunAsks sends every unanswered workflow-step question to a newly
// connected client, so a reload, a second device and a transport gap converge on the
// same set. The client's dock de-duplicates by ask id.
func (rt *Runtime) replayPendingRunAsks(writeFn func(vibekit.ServerEvent) error) error {
	for _, evt := range rt.runs.asks.List("") {
		if err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}
