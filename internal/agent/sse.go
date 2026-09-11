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
			evt.Payload = b.chatStatus.Merge(evt.ChatID, p)
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
	// After the publish, never before: the heartbeat's idle gate reads this as proof
	// the stream carried bytes, and the marshal failure above carried none.
	b.notePublish()
}

// handleSSE opens the /api/events stream. The sse library owns the transport
// (headers, Last-Event-ID replay, keepalives, slow-client eviction); vibekit owns
// the connected handshake and the initial per-client state replay.
func (rt *Runtime) handleSSE(w http.ResponseWriter, r *http.Request) {
	chatFilter := vibekit.ChatID(r.URL.Query().Get("chat_id"))
	declared, stated := parseSnapshotChats(r)
	lastRaw := adoptCursorParam(r)
	// Both halves of the declaration, because the count alone cannot separate the two
	// states that answer zero: an old client that never declared (fail open) and a
	// client declaring nothing (bare busy signals only).
	slog.Info("SSE connected", "chat_filter", logsafe.Field(string(chatFilter)),
		"last_event_id", logsafe.Field(lastRaw), "snapshots_declared", stated,
		"declared_snapshots", len(declared))

	// A reconnect reloads push preferences from disk so settings edited while SSE
	// was down take effect without a restart.
	if lastRaw != "" && rt.push != nil {
		rt.push.ReloadPreferences(r.Context())
	}

	rt.bus.fanout.Serve(w, r,
		sse.WithTopic(string(chatFilter)),
		sse.OnConnect(func(sw *sse.Writer, b sse.ReplayBounds) error {
			return rt.streamInitialState(sw, b.Floor, b.Head, chatFilter, declared, stated)
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

// snapshotParam is the query spelling of "these chats are on screen", which the
// server cannot derive. A NEW parameter rather than a value on chat_id, which is the
// hub TOPIC filter: a scoped topic would take every other chat's frames off the wire
// and leave the tab dots, the sidebar and the strip dark.
const snapshotParam = "snapshot"

// snapshotNone is how a client says NO chat needs its in-flight transcript: a
// reduced boot, which wants the bare busy signals and nothing else.
//
// A SENTINEL rather than an empty value, because presence-versus-emptiness cannot
// carry this: `?snapshot=` is also what a client with no chat active yet would send,
// so the two states this parameter exists to separate would collapse again one layer
// up. An empty-valued pair is also a load-bearing character any URL canonicaliser may
// drop silently, and the drop fails OPEN — straight back to the whole payload.
const snapshotNone = "none"

// parseSnapshotChats reads the chats a client declares as on-screen, and whether it
// declared at ALL. A malformed entry is DROPPED and reported rather than failing the
// connect, because the stream is the client's only recovery channel.
//
// THREE wire states, and the second return is what separates the two an empty map
// collapses. An ABSENT (or empty-valued) parameter answers (nil, false) — an old
// client or a curl, which fails OPEN downstream so nothing withholds state a client
// needs. `?snapshot=none` answers (nil, true): the client spoke and named nothing, so
// every open chat gets the bare busy signal. A list answers (set, true).
//
// The sentinel is tested BEFORE the id loop, because ids.ValidChatID is a charset
// check rather than a shape check (internal/ids/valid.go) and so accepts this word:
// parsed as an id it would give a chat literally named `none` a snapshot the client
// asked not to receive, and would report one declared id in the log line above.
// Residual, stated: such a chat cannot be declared. Nothing mints one —
// vibekit.NewChatID is `c-` plus hex of 16 random bytes.
//
// A list whose every entry is malformed answers (empty, true), so it reads as
// "declared nothing" rather than failing open. That client HAS the parameter, so it
// can recover through turn_ended's whole message, which is the cost this package
// already accepts for an undeclared busy chat; failing open on mangled input is the
// 265 KB path instead.
func parseSnapshotChats(r *http.Request) (declared map[vibekit.ChatID]struct{}, stated bool) {
	raw := r.URL.Query().Get(snapshotParam)
	if raw == "" {
		return nil, false
	}
	if raw == snapshotNone {
		return nil, true
	}
	declared = make(map[vibekit.ChatID]struct{}, maxDeclaredSnapshotChats)
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
	return declared, true
}

// hasOpenTab reports whether this chat has a row in the tab strip, matching a
// chat-kind TabSubject by Ref.
//
// A NIL store answers TRUE for every chat. That FAIL-OPEN is deliberate: an unwired
// store must never withhold state a client needs, so a wiring mistake costs a
// redundant frame rather than every busy chat's transcript.
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

// The cold-connect payload policy. These are POLICY numbers derived from what a
// realistic reconnect must CARRY, so the gate over them fails rather than being
// raised: a connect that exceeds one is the defect, never the constant.
const (
	// maxColdConnectBytes bounds the whole payload one cold connect writes: 1 KiB
	// handshake + 10 KiB of busy chat ids + 13 KiB of live-run rows + 16 KiB of bare
	// busy signals + 16 KiB of waiting statuses + 64 KiB of permission asks + 16 KiB of
	// run asks + the snapshot budget + headroom.
	maxColdConnectBytes = 512 << 10
	// maxBusyChats bounds the busy-chat list the handshake carries. A chat id is 34
	// characters, so one JSON array element is 37 bytes with its quotes and comma:
	// 256 × 37 ≈ 9.5 KiB, about 7% of maxConnectFrameBytes. Deliberately far past every
	// other bound in the system — one bridge per chat at ~300 MB of process tree — so it
	// is a ceiling on the BYTES rather than a limit anyone reaches.
	maxBusyChats = 256
	// maxConnectLiveRuns bounds the live-run inventory the handshake carries. A row is
	// a wf_<16 hex> id, a c-<32 hex> chat id and a bool, so ~100 bytes of JSON:
	// 128 × 100 ≈ 12.8 KiB, 10% of maxConnectFrameBytes. The single-run rule bounds
	// concurrent runs to a handful in practice, but that is a PRODUCT rule and not a
	// bound on this array — a stale-lease accumulation is exactly what inflates it.
	maxConnectLiveRuns = 128
	// maxConnectFrameBytes bounds ONE frame, at 2x the per-snapshot text cap. WebKit
	// buffers a whole SSE frame before dispatching it, so one huge frame is a
	// peak-memory cost the total cannot express.
	maxConnectFrameBytes = 128 << 10
	// connectSnapshotBudget is the per-connect allowance for every turn_state
	// snapshot together, sized so at least four chats get a real snapshot before the
	// rest fall back to the bare busy signal.
	connectSnapshotBudget = 256 << 10
	// maxDeclaredSnapshotChats bounds how many chats one connect may declare as
	// on-screen, so ?snapshot= cannot ask for the payload the budget refuses.
	maxDeclaredSnapshotChats = 8
)

// connectSnapshotCaps bounds ONE turn_state snapshot, at 52 KiB of text so at least
// four chats fit inside connectSnapshotBudget. Sized from what a reader needs on a
// mid-turn reconnect: the tail of the reply being written now, where a screen of prose
// is ~2 KiB and reasoning renders collapsed.
//
// Every dimension must be set, or MaxTextBytes reports no ceiling: a zero leaves that
// dimension unbounded and the arithmetic answers 0.
var connectSnapshotCaps = buffer.SnapshotCaps{
	ReasoningBytes:  4 << 10,
	ContentBytes:    16 << 10,
	BlockTextBytes:  16 << 10,
	ToolCalls:       8,
	ToolOutputBytes: 2 << 10,
	Blocks:          64,
}

// liveTurnGETCaps bounds the in-flight turn the transcript GET carries, and every
// dimension is sized ABOVE the measured maximum so the ordinary turn is not cut at all.
//
// The reader's need on THIS channel is the WHOLE turn, not its tail. This is the one
// channel that carries the newest turn while it is in flight, and the newest turn is
// served whole unconditionally — so a cap that keeps a tail is a cap that withholds the
// reply a reader came for. connectSnapshotCaps answers the other question (a mid-turn
// reconnect wants the reply being written NOW, across up to eight chats inside one
// budget), which is why the two literals are stated separately rather than aliased.
//
// Measured maxima over the live chat volume, one per dimension, so the sizing is
// checkable rather than asserted. What survives is a RUNAWAY ceiling of
// MaxTextBytes() = 10,616,832 bytes: past it the turn is cut and `truncated` says so.
// ToolOutputTotalBytes is what makes that number statable — the per-call cap stays at the
// terminal ring buffer's own 64 KiB bound, so a single call is never cut, and the
// aggregate bounds the product the per-call cap cannot.
//
// Deliberately NOT narrowed by a remaining budget: that GET serves ONE chat, so there is
// no fanout to divide. Its cost is no longer charged against the caller's own ?max_bytes=
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

// streamInitialState writes the connected handshake, then replays this client's
// outstanding state so a reconnecting browser rebuilds its UI as it was.
// ConnectedPayload carries the ring floor/head so the client can detect a replay
// gap, plus the workspace root, the one server fact the client cannot derive.
func (rt *Runtime) streamInitialState(
	sw *sse.Writer,
	floor, head uint64,
	chatFilter vibekit.ChatID,
	declared map[vibekit.ChatID]struct{},
	stated bool,
) error {
	busy := rt.coord.turns.busyChatIDs()
	// A scoped list states nothing about the chats it omits, and an over-cap one is
	// withheld rather than truncated: on either the client retracts nothing.
	busyStated := chatFilter == "" && len(busy) <= maxBusyChats
	if !busyStated {
		if len(busy) > maxBusyChats {
			slog.Warn("connect busy-chat list withheld: over cap",
				"cap", maxBusyChats, "count", len(busy))
		}
		busy = nil
	}
	liveRuns := rt.runs.liveRunRows()
	liveRunsStated := len(liveRuns) <= maxConnectLiveRuns
	if !liveRunsStated {
		slog.Warn("connect live-run inventory withheld: over cap",
			"cap", maxConnectLiveRuns, "count", len(liveRuns))
		liveRuns = nil
	}
	connectedEvt := vibekit.NewEvent(vibekit.EventConnected, "", vibekit.ConnectedPayload{
		Workspace:      rt.lifecycle.workDir,
		BusyChats:      busy,
		LiveRuns:       liveRuns,
		Floor:          floor,
		Head:           head,
		BusyStated:     busyStated,
		LiveRunsStated: liveRunsStated,
	})
	connectedData, err := json.Marshal(connectedEvt)
	if err != nil {
		slog.Error("marshal connected event", "error", err)
		return errors.New("marshal connected event")
	}
	if err := sw.Event(head, "", connectedData); err != nil {
		return err
	}

	// No id: replayed state is synthesized, not part of the event sequence. The
	// MARSHALED byte count comes back beside the error so the budget below is exact
	// rather than estimated; a skipped event reports zero, which is what it cost.
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
	if err := rt.replayTurnState(writeEvent, chatFilter, open, declared, stated); err != nil {
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
		// Keyed on `open`, NOT on what replayTurnState emitted: the tab filter and the
		// budget can withhold a busy chat's frame, and a chat whose turn is running
		// must still suppress a stale waiting_on_user. That covers a PRIME's chat too.
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

// narrowedConnectCaps clamps the per-snapshot caps to what is LEFT of the per-connect
// budget, scaling every text dimension by one factor so a short budget shrinks the
// snapshot in proportion rather than starving one field.
//
// The floor is 1 byte and never 0: a zero dimension means UNBOUNDED, so clamping a
// field to "spend nothing" would spend everything.
//
// ToolOutputTotalBytes is deliberately NOT in the scale list, and that omission is
// load-bearing rather than an oversight: connectSnapshotCaps leaves it zero (unbounded,
// with the per-call product doing the bounding), and `scale` floors at 1 — so scaling it
// would turn an unbounded dimension into a 1-BYTE aggregate on the connect path and drop
// every tool output from every snapshot.
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
// The order must be DETERMINISTIC. Go randomises map iteration, so without the sort a
// short budget picks arbitrary winners and one fixture measures a different payload
// every run. Declared chats lead so a chat a reader is looking at gets the full cap.
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

// replayTurnState emits one synthesized turn_state event per chat with an open turn a
// client can actually SHOW, under a per-connect snapshot budget. Reading the TURN
// rather than the prompt slot is what makes an agent-initiated turn visible. A PRIME
// turn is never served: its replayed frames would render the preamble as conversation.
//
// A busy chat with NO OPEN TAB gets nothing (no row in the strip, so no dot to feed);
// an open chat the client did not DECLARE gets the bare busy signal, which is what
// makes the payload O(1) in the number of busy chats.
//
// `stated` is the client's own answer to "did you declare", from parseSnapshotChats.
// Its zero value is the fail-open one, so a caller that cannot say keeps the
// old-client behaviour rather than silently withholding every snapshot.
func (rt *Runtime) replayTurnState(
	writeFn func(vibekit.ServerEvent) (int, error),
	chatFilter vibekit.ChatID,
	open map[vibekit.ChatID]openTurnFacts,
	declared map[vibekit.ChatID]struct{},
	stated bool,
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
		// A client that never DECLARED reads as "every open chat", so an older client
		// and a curl still get snapshots — bounded by the budget rather than by the
		// parameter. A client that declared and named nothing gets none: keyed on
		// `stated` rather than on len(declared), because those are the two states an
		// empty set cannot tell apart.
		wantSnapshot := !stated || cand.declared
		// SnapshotCapped rather than Snapshot: an uncapped snapshot is a whole
		// transcript. ChunkSeq is taken either way, being a fact about the turn.
		msg, seq, cut, ok := cand.facts.Buf.SnapshotCapped(narrowedConnectCaps(remaining))
		payload.ChunkSeq = seq
		if wantSnapshot && remaining > 0 && ok {
			payload.Message = &msg
			payload.Truncated = cut
		}
		// Truncated stays FALSE on a bare signal: nothing was withheld from a payload
		// carrying no message, and a marker there teaches a reader to ignore the real
		// one.
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
