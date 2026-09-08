package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/ids"
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
	// After the publish, never before: a marshal failure returned above reached the
	// fan-out with nothing, and the heartbeat's idle gate reads this as proof the
	// stream carried bytes.
	b.notePublish()
}

// handleSSE opens the /api/events stream. The sse library owns the transport
// (headers, Last-Event-ID replay, keepalives, slow-client eviction); vibekit owns
// the connected handshake and the initial per-client state replay.
func (rt *Runtime) handleSSE(w http.ResponseWriter, r *http.Request) {
	chatFilter := vibekit.ChatID(r.URL.Query().Get("chat_id"))
	declared := parseSnapshotChats(r)
	lastRaw := adoptCursorParam(r)
	slog.Info("SSE connected", "chat_filter", logsafe.Field(string(chatFilter)),
		"last_event_id", logsafe.Field(lastRaw), "declared_snapshots", len(declared))

	// A reconnect reloads push preferences from disk so settings edited while SSE
	// was down take effect without a restart.
	if lastRaw != "" && rt.push != nil {
		rt.push.ReloadPreferences(r.Context())
	}

	rt.bus.fanout.Serve(w, r,
		sse.WithTopic(string(chatFilter)),
		sse.OnConnect(func(sw *sse.Writer, b sse.ReplayBounds) error {
			return rt.streamInitialState(sw, b.Floor, b.Head, chatFilter, declared)
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

// snapshotParam is the query spelling of "these chats are on screen", the one fact
// the server cannot derive: the active chat is per-DEVICE localStorage state, and
// vibekit's standing rule is that the server does not care which chat is visible on
// which device.
//
// A NEW parameter rather than a value on chat_id, which is the hub TOPIC filter: a
// scoped topic delivers only an exactly-matching chat's events, so reusing it would
// take every other chat's message_chunk, chat_status and tabs_changed frames off the
// wire and leave the tab dots, the sidebar and the strip dark.
const snapshotParam = "snapshot"

// parseSnapshotChats reads the chats a client declares as on-screen. Malformed
// entries are DROPPED and reported rather than failing the connect — the
// ParseCIDRs shape — because the stream is the client's only recovery channel and a
// mangled parameter must not be what keeps it closed.
//
// An EMPTY result means "declare nothing", which reads downstream as "every open
// chat", so an older client, a curl and a test fixture all still receive snapshots,
// bounded by connectSnapshotBudget rather than by the parameter.
func parseSnapshotChats(r *http.Request) map[vibekit.ChatID]struct{} {
	raw := r.URL.Query().Get(snapshotParam)
	if raw == "" {
		return nil
	}
	declared := make(map[vibekit.ChatID]struct{}, maxDeclaredSnapshotChats)
	malformed, overCap := 0, 0
	for entry := range strings.SplitSeq(raw, ",") {
		if !ids.ValidChatID(entry) {
			malformed++
			continue
		}
		if len(declared) >= maxDeclaredSnapshotChats {
			overCap++
			continue
		}
		declared[vibekit.ChatID(entry)] = struct{}{}
	}
	if malformed > 0 || overCap > 0 {
		slog.Warn("SSE snapshot parameter partly ignored", "declared", len(declared),
			"malformed", malformed, "over_cap", overCap, "cap", maxDeclaredSnapshotChats)
	}
	return declared
}

// hasOpenTab reports whether this chat has a row in the tab strip, matching a
// chat-kind TabSubject by Ref.
//
// A NIL store answers TRUE for every chat, and that FAIL-OPEN is deliberate rather
// than incidental: an unwired store must never withhold state a client needs, and
// every test in this package runs with one. Closed-by-default would break the suite
// and, worse, would silently withhold every busy chat's transcript in production on
// a wiring mistake.
func (rt *Runtime) hasOpenTab(chatID vibekit.ChatID) bool {
	if rt.tabs == nil {
		return true
	}
	subjects, _ := rt.tabs.List()
	for _, s := range subjects {
		if s.Kind == vibekit.TabKindChat && s.Ref == string(chatID) {
			return true
		}
	}
	return false
}

// The cold-connect payload policy. Every number below is derived from what a
// realistic reconnect must CARRY, not from what one was measured to carry: three
// fresh connects against the live instance came to 18,345,594 / 18,369,217 /
// 18,416,535 bytes, with every byte of the problem in six uncapped turn_state
// frames and a ~59 KB non-snapshot remainder.
//
// maxColdConnectBytes builds up worst-case as: 1 KiB for the retry: line and the
// connected handshake (measured ~220 B), 16 KiB for one bare busy signal per open
// tab (~200 B x tabs.MaxOpenTabs), 16 KiB for the waiting-status replays on the
// same bound, 64 KiB for pending permission asks (a turn approval carries a file
// list and is uncapped today), 16 KiB for pending run asks, 256 KiB for every
// snapshot together, and ~143 KiB of headroom.
//
// They are POLICY numbers, so the gate over them fails rather than being raised: a
// connect that exceeds one of these is the defect, never the constant.
const (
	// maxColdConnectBytes bounds the whole payload one cold connect writes.
	maxColdConnectBytes = 512 << 10
	// maxConnectFrameBytes bounds ONE frame, at 2x the per-snapshot text cap.
	// WebKit buffers a whole SSE frame before it dispatches it, so a single 3 MB
	// frame is a peak-memory cost in the network and parse layers that the total
	// cannot express.
	maxConnectFrameBytes = 128 << 10
	// connectSnapshotBudget is the per-connect allowance for every turn_state
	// snapshot together, sized so at least four chats get a real snapshot before
	// the rest fall back to the bare busy signal.
	connectSnapshotBudget = 256 << 10
	// maxDeclaredSnapshotChats bounds how many chats one connect may declare as
	// on-screen, so the ?snapshot= parameter cannot ask for the payload the
	// budget above exists to refuse.
	maxDeclaredSnapshotChats = 8
)

// connectSnapshotCaps bounds ONE turn_state snapshot, at 52 KiB of text
// (connectSnapshotCaps.MaxTextBytes()) so at least four chats fit inside
// connectSnapshotBudget. Sized from what a reader needs on a mid-turn reconnect —
// the tail of the reply being written now. A screen of prose is ~2 KiB, and
// reasoning renders in a <details> that is collapsed by default, so 4 KiB is
// several screens of a thing nobody is looking at yet.
//
// Every dimension is set, which is what makes MaxTextBytes report a real ceiling:
// a zero leaves that dimension unbounded and the arithmetic answers 0.
var connectSnapshotCaps = buffer.SnapshotCaps{
	ReasoningBytes:  4 << 10,
	ContentBytes:    16 << 10,
	BlockTextBytes:  16 << 10,
	ToolCalls:       8,
	ToolOutputBytes: 2 << 10,
	Blocks:          64,
}

// streamInitialState writes the connected handshake, then replays this client's
// outstanding state so a reconnecting browser rebuilds its UI as it was.
// ConnectedPayload carries the ring floor/head so the client can detect a replay
// gap, plus the workspace root, the one server fact the client cannot derive.
func (rt *Runtime) streamInitialState(
	sw *sse.Writer,
	floor, head uint64,
	chatFilter vibekit.ChatID,
	declared map[vibekit.ChatID]struct{},
) error {
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
	//
	// The MARSHALED byte count comes back beside the error so the per-connect budget
	// below is EXACT rather than estimated, and so every replay path's cost is
	// measurable. A skipped event reports zero, which is what it cost.
	writeEvent := func(evt vibekit.ServerEvent) (int, error) {
		data, err := json.Marshal(evt)
		if err != nil {
			return 0, nil //nolint:nilerr // skip unmarshalable event, keep stream
		}
		return len(data), sw.Event(0, "", data)
	}

	if err := rt.replayPendingPermissions(writeEvent, chatFilter); err != nil {
		return err
	}

	// Beside the permissions rather than folded into them: the two registries have
	// different lifetimes (run_ask.go), and a parked run has no deadline of its own.
	if err := rt.replayPendingRunAsks(writeEvent, chatFilter); err != nil {
		return err
	}

	// The steering buffer, for the same reason and on the same terms: KAS holds it,
	// nothing can read it back, and the gap door empties the client's dock without
	// promoting anything. See replayPendingSteers for what it cannot recover.
	if err := rt.replayPendingSteers(writeEvent, chatFilter); err != nil {
		return err
	}

	// ONE read of the open-turn set serves both replays below, so the busy chats
	// the second one skips are exactly the chats the first one described.
	open := rt.coord.turns.openTurns()
	if err := rt.replayTurnState(writeEvent, chatFilter, open, declared); err != nil {
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
	writeFn func(vibekit.ServerEvent) (int, error),
	chatFilter vibekit.ChatID,
	open map[vibekit.ChatID]openTurnFacts,
) error {
	for id, p := range rt.bus.chatStatus.Snapshot() {
		if chatFilter != "" && id != chatFilter {
			continue
		}
		// The skip is keyed on `open`, NOT on what replayTurnState emitted, and that
		// is load-bearing since the tab filter and the budget can withhold a busy
		// chat's frame: a chat whose turn is genuinely running must still suppress a
		// stale waiting_on_user, or a client renders the amber "answer me" dot over a
		// chat the agent is working in.
		//
		// A PRIME's chat is skipped here too, even though turn_state withholds it:
		// its turn is genuinely running, so an older status describes the wrong turn.
		if _, busy := open[id]; busy {
			continue
		}
		if p.Status != vibekit.ChatStatusWaitingOnUser {
			continue
		}
		if _, err := writeFn(vibekit.NewEvent(vibekit.EventChatStatus, id, p)); err != nil {
			return err
		}
	}
	return nil
}

// turnCandidate is one chat the connect replay may describe, carrying the two facts
// the order and the budget below turn on.
type turnCandidate struct {
	id       vibekit.ChatID
	facts    openTurnFacts
	declared bool
}

// narrowedConnectCaps clamps the per-snapshot caps to what is LEFT of the
// per-connect budget, so the last snapshot that fits is bounded by the budget rather
// than by the per-snapshot ceiling.
//
// Every text dimension is scaled by the same factor rather than one being starved:
// reasoning and content are separate fields a reader consumes together, so a short
// budget should shrink the snapshot in proportion. The floor is 1 byte and never 0 —
// a zero dimension means UNBOUNDED to buffer.SnapshotCaps, so clamping a field to
// "spend nothing" would spend everything, and MaxTextBytes would report 0.
func narrowedConnectCaps(remaining int) buffer.SnapshotCaps {
	caps := connectSnapshotCaps
	full := caps.MaxTextBytes()
	if remaining <= 0 || full <= 0 || remaining >= full {
		return caps
	}
	scale := func(n int) int { return max(1, n*remaining/full) }
	caps.ReasoningBytes = scale(caps.ReasoningBytes)
	caps.ContentBytes = scale(caps.ContentBytes)
	caps.BlockTextBytes = scale(caps.BlockTextBytes)
	caps.ToolOutputBytes = scale(caps.ToolOutputBytes)
	return caps
}

// turnStateCandidates is the chats the connect replay will describe, in WIRE ORDER:
// the two filters applied, then sorted.
//
// A DETERMINISTIC order is required, not a nicety. Go randomises map iteration, so
// without the sort a short budget picks arbitrary winners and the same fixture
// measures a different payload on every run — which makes every byte assertion over
// this path flaky. Declared chats lead so the chats a reader is actually looking at
// get the full per-snapshot cap; the rest follow by chat id.
func (rt *Runtime) turnStateCandidates(
	chatFilter vibekit.ChatID,
	open map[vibekit.ChatID]openTurnFacts,
	declared map[vibekit.ChatID]struct{},
) []turnCandidate {
	candidates := make([]turnCandidate, 0, len(open))
	for id, facts := range open {
		if chatFilter != "" && id != chatFilter {
			continue
		}
		if facts.Source == vibekit.TurnSourcePrime {
			continue
		}
		// Skipped ENTIRELY rather than downgraded to a bare signal: a chat with no
		// tab has no surface the signal could reach. Fails OPEN on an unwired store
		// (see hasOpenTab).
		if !rt.hasOpenTab(id) {
			continue
		}
		_, isDeclared := declared[id]
		candidates = append(candidates, turnCandidate{id: id, facts: facts, declared: isDeclared})
	}
	slices.SortFunc(candidates, func(a, b turnCandidate) int {
		if a.declared != b.declared {
			if a.declared {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.id, b.id)
	})
	return candidates
}

// replayTurnState emits one synthesized turn_state event per chat with an open turn
// that a client can actually SHOW, under a per-connect snapshot budget. Reading the
// TURN rather than the prompt slot is what makes an agent-initiated turn visible at
// all. A PRIME turn is never served: its frames are a transcript replay vibekit sent
// itself, so serving them would render the preamble as conversation.
//
// Two filters compose, and they answer different questions. A busy chat with NO OPEN
// TAB gets nothing at all — there is no row in the strip, so there is no dot to feed
// and no transcript to draw. An open chat the client did not DECLARE as on-screen
// gets the bare busy signal, which is what makes the payload O(1) in the number of
// busy chats: the snapshot is the expensive part and only a visible chat needs it.
func (rt *Runtime) replayTurnState(
	writeFn func(vibekit.ServerEvent) (int, error),
	chatFilter vibekit.ChatID,
	open map[vibekit.ChatID]openTurnFacts,
	declared map[vibekit.ChatID]struct{},
) error {
	candidates := rt.turnStateCandidates(chatFilter, open, declared)
	remaining := connectSnapshotBudget
	snapshots, bare, truncated := 0, 0, 0
	for _, cand := range candidates {
		status := rt.bus.chatStatus.Get(cand.id)
		payload := vibekit.TurnStatePayload{
			Status:      status.Status,
			Description: status.Description,
			// Emitted AND marked: the snapshot is the only copy of the in-flight step
			// transcript, but unmarked it makes the launching chat read as busy.
			WorkflowStep: cand.facts.Source == vibekit.TurnSourceWorkflowStep,
		}
		// An empty declared set reads as "every open chat", so an older client and a
		// curl still get snapshots — bounded by the budget rather than the parameter.
		wantSnapshot := len(declared) == 0 || cand.declared
		// SnapshotCapped rather than Snapshot: an uncapped snapshot is a whole
		// transcript, and six of them are the whole cold-connect payload. ChunkSeq is
		// taken either way — it is the watermark a client drops folded chunks
		// against, and it is a fact about the turn rather than about the snapshot.
		msg, seq, cut, ok := cand.facts.Buf.SnapshotCapped(narrowedConnectCaps(remaining))
		payload.ChunkSeq = seq
		if wantSnapshot && remaining > 0 && ok {
			// The marker travels with the snapshot so no client can read the tail as
			// complete.
			payload.Message = &msg
			payload.Truncated = cut
		}
		// Truncated stays FALSE on a bare signal: nothing was withheld from a payload
		// that carries no message, and a marker there would teach a reader to ignore
		// the one on a payload that IS cut.
		n, err := writeFn(vibekit.NewEvent(vibekit.EventTurnState, cand.id, payload))
		if err != nil {
			return err
		}
		if payload.Message == nil {
			bare++
			continue
		}
		snapshots++
		if cut {
			truncated++
		}
		remaining -= n
	}
	slog.Debug("SSE connect turn_state replay", "snapshots", snapshots, "bare_signals", bare,
		"snapshot_bytes", connectSnapshotBudget-remaining, "truncated", truncated)
	return nil
}

// replayPendingPermissions sends the unresolved permission_needed events to a newly
// connected client, so dialogs survive a reconnect that outlived the ring buffer.
// EVERY unresolved request goes, however old: the agent server holds
// session/request_permission open until answered, so an old card is a live question.
func (rt *Runtime) replayPendingPermissions(writeFn func(vibekit.ServerEvent) (int, error), chatFilter vibekit.ChatID) error {
	for _, evt := range rt.bus.pendingPerms.List(chatFilter) {
		// The byte count is discarded: this replay does not budget, and its allowance
		// is a separate line in maxColdConnectBytes.
		if _, err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}

// replayPendingRunAsks sends every unanswered workflow-step question to a newly
// connected client, so a reload, a second device and a transport gap converge on the
// same set. The client's dock de-duplicates by ask id, which is what lets a
// `transport:gap` clear eagerly and be followed by this burst.
func (rt *Runtime) replayPendingRunAsks(writeFn func(vibekit.ServerEvent) (int, error), chatFilter vibekit.ChatID) error {
	for _, evt := range rt.runs.asks.List(chatFilter) {
		// The count is discarded for replayPendingPermissions' reason.
		if _, err := writeFn(evt); err != nil {
			return err
		}
	}
	return nil
}
