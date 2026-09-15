package agent

// What the connect hook states. The handshake's busy set is the NEGATIVE half no
// live frame carries (a chat whose turn died with the previous process is never
// contradicted otherwise), so BusyStated bounds its blast radius: a list the server
// could not state completely must retract nothing at all. On a v3 connect the
// pending set and the waiting-status set arrive as ONE aggregate frame each, whole
// and stamped, so a row resolved elsewhere while the client was away is cleared
// and the version reaches the client's map; a legacy connect keeps the per-item
// replay its decoders know and the numeric floor/head its gap arithmetic reads.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/sse/ssetest"
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// connectFrames runs one cold connect and returns the id-less application frames the
// hook wrote, decoded to their envelope, in wire order. legacy selects the connect
// shape: a legacy request sends no SSE-Wire header.
func connectFrames(t *testing.T, rt *Runtime, legacy bool) []vibekit.ServerEvent {
	t.Helper()
	rec := coldConnectAs(t, rt, legacy)
	frames, err := ssetest.ReadFrames(strings.NewReader(rec.Body.String()), 0)
	if err != nil {
		t.Fatalf("Setup: parse frames: %v", err)
	}
	var out []vibekit.ServerEvent
	for _, f := range frames {
		if f.Event != "" || f.ID != "" {
			continue // the hello, a keepalive, a replayed ring frame
		}
		var evt vibekit.ServerEvent
		if err := json.Unmarshal([]byte(f.Data), &evt); err != nil {
			t.Fatalf("Setup: frame %q is not a ServerEvent: %v", f.Data, err)
		}
		out = append(out, evt)
	}
	return out
}

// connectPayload decodes the ONE connected frame a cold connect writes.
func connectPayload(t *testing.T, rt *Runtime, _ string) vibekit.ConnectedPayload {
	t.Helper()
	return connectedOf(t, connectFrames(t, rt, false))
}

func connectedOf(t *testing.T, frames []vibekit.ServerEvent) vibekit.ConnectedPayload {
	t.Helper()
	for _, evt := range frames {
		if evt.Type != vibekit.EventConnected {
			continue
		}
		var p vibekit.ConnectedPayload
		if err := reencode(evt.Payload, &p); err != nil {
			t.Fatalf("Setup: decode connected: %v", err)
		}
		return p
	}
	t.Fatal("the cold connect wrote no connected frame")
	return vibekit.ConnectedPayload{}
}

// reencode moves a decoded `any` payload into its typed shape.
func reencode(from any, into any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func frameOfType(frames []vibekit.ServerEvent, typ vibekit.EventType) (vibekit.ServerEvent, bool) {
	for _, evt := range frames {
		if evt.Type == typ {
			return evt, true
		}
	}
	return vibekit.ServerEvent{}, false
}

func countType(frames []vibekit.ServerEvent, typ vibekit.EventType) int {
	n := 0
	for _, evt := range frames {
		if evt.Type == typ {
			n++
		}
	}
	return n
}

func busySetOf(p vibekit.ConnectedPayload) map[vibekit.ChatID]bool {
	out := make(map[vibekit.ChatID]bool, len(p.BusyChats))
	for _, id := range p.BusyChats {
		out[id] = true
	}
	return out
}

func TestConnect_StatesTheBusySet(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 3)

	p := connectPayload(t, rt, "")

	if !p.BusyStated {
		t.Fatal("a connect does not state its busy set, so the client retracts " +
			"nothing and every stale `thinking` survives the reconnect")
	}
	busy := busySetOf(p)
	for _, id := range ids {
		if !busy[id] {
			t.Errorf("chat %q is running its own prompt turn and is absent from busy_chats", id)
		}
	}
}

// A workflow STEP's turn belongs to its run, so it does not name its launching
// chat busy. This is the stuck-purple population the retraction exists to reach.
func TestConnect_TheBusySetExcludesStepTurns(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bridge.mgr.orInsert("c-step")
	rt.bridge.mgr.orInsert("c-own")
	if e := rt.coord.StartTurn(t.Context(), "c-step", vibekit.TurnSourceWorkflowStep); e == 0 {
		t.Fatal("StartTurn refused the step")
	}
	if e := rt.coord.StartTurn(t.Context(), "c-own", vibekit.TurnSourcePrompt); e == 0 {
		t.Fatal("StartTurn refused the prompt")
	}

	busy := busySetOf(connectPayload(t, rt, ""))

	if busy["c-step"] {
		t.Error("a workflow-step turn names its LAUNCHING chat busy, so the retraction is " +
			"withheld from exactly the population it was designed for")
	}
	if !busy["c-own"] {
		t.Error("a chat's own prompt turn is absent from busy_chats")
	}
}

// The admission window: the user row is persisted and broadcast and `thinking` is latched
// before any Turn exists, so a reconnect inside it must NOT retract. Reachable without a
// cold spawn — another device prompts, this one reconnects from background.
func TestConnect_TheBusySetIncludesAnAdmittedPromptWithNoTurnMinted(t *testing.T) {
	rt := newBudgetRuntime(t)
	if !rt.coord.TryReserveTurn("c-admitted", vibekit.TurnSourcePrompt) {
		t.Fatal("a fresh chat refused a prompt reservation")
	}
	t.Cleanup(func() { rt.coord.ReleaseTurnReservation("c-admitted") })

	if !busySetOf(connectPayload(t, rt, ""))["c-admitted"] {
		t.Error("a chat whose prompt is admitted but whose Turn is not minted is absent from " +
			"busy_chats, so the connect retraction clears `thinking` under a live prompt")
	}
}

func TestConnect_TheBusySetIsWithheldOverTheCap(t *testing.T) {
	rt := newBudgetRuntime(t)
	for i := range maxBusyChats + 1 {
		id := vibekit.ChatID("c-over-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		if !rt.coord.TryReserveTurn(id, vibekit.TurnSourcePrompt) {
			t.Fatalf("chat %q refused a reservation", id)
		}
	}

	p := connectPayload(t, rt, "")

	if p.BusyStated {
		t.Errorf("an over-cap connect claims its busy set is complete (%d chats, cap %d)",
			len(p.BusyChats), maxBusyChats)
	}
	if len(p.BusyChats) != 0 {
		t.Errorf("an over-cap connect carries a TRUNCATED list of %d chats; a partial list "+
			"read as complete would clear a live turn", len(p.BusyChats))
	}
}

// C1: the inventory rides this frame, so the square's first paint costs no round trip.
func TestConnect_CarriesEveryHeldLeaseAndStatesTheInventory(t *testing.T) {
	rt := newBudgetRuntime(t)
	store := rt.runs.leaseStore()
	if err := store.Put(t.Context(), &runlease.Lease{WorkflowID: "wf_a", ChatID: "c1", Recipe: "r", Origin: runlease.OriginManual}); err != nil {
		t.Fatalf("put wf_a: %v", err)
	}
	if err := store.Put(t.Context(), &runlease.Lease{WorkflowID: "wf_b", Recipe: "r2", Origin: runlease.OriginAgent}); err != nil {
		t.Fatalf("put wf_b: %v", err)
	}

	p := connectPayload(t, rt, "")

	if !p.LiveRunsStated {
		t.Fatal("the handshake does not state its live-run inventory, so the client pays the " +
			"GET /api/runs/live round trip on every connect")
	}
	got := map[string]bool{}
	for _, r := range p.LiveRuns {
		got[r.WorkflowID] = true
	}
	for _, want := range []string{"wf_a", "wf_b"} {
		if !got[want] {
			t.Errorf("held lease %q is absent from the handshake's live_runs", want)
		}
	}
}

// liveRunRows is ONE projection with two doors, so the handshake and the endpoint cannot
// disagree about what a live run IS.
func TestLiveRunRows_AnswersIdenticallyToTheEndpoint(t *testing.T) {
	rt := newBudgetRuntime(t)
	store := rt.runs.leaseStore()
	for _, id := range []string{"wf_a", "wf_b", "wf_c"} {
		if err := store.Put(t.Context(), &runlease.Lease{WorkflowID: id, ChatID: "c1", Recipe: "r", Origin: runlease.OriginManual}); err != nil {
			t.Fatalf("put %q: %v", id, err)
		}
	}

	fromHandshake := connectPayload(t, rt, "").LiveRuns
	fromMethod := rt.runs.liveRunRows()

	if len(fromHandshake) != len(fromMethod) {
		t.Fatalf("the handshake carries %d rows and liveRunRows answers %d",
			len(fromHandshake), len(fromMethod))
	}
	byID := map[string]vibekit.LiveRun{}
	for _, r := range fromMethod {
		byID[r.WorkflowID] = r
	}
	for _, h := range fromHandshake {
		m, ok := byID[h.WorkflowID]
		if !ok {
			t.Errorf("the handshake names run %q the projection does not", h.WorkflowID)
			continue
		}
		if h != m {
			t.Errorf("run %q reads %+v on the handshake and %+v from the projection",
				h.WorkflowID, h, m)
		}
	}
}

// --- The two connect shapes ---

// TestConnect_V3CarriesTheWholePendingSetAsOneStampedFrame pins the aggregate: every
// chat's pending items in one pending_snapshot, the stamp on the envelope at the
// registry's `pending` version, and no per-item frame beside it.
func TestConnect_V3CarriesTheWholePendingSetAsOneStampedFrame(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 2)
	rt.bus.steers.SteerWaiting(ids[1], vibekit.SteerQueuedPayload{SteerID: "s1", Text: "steer text"})

	frames := connectFrames(t, rt, false)

	if got := countType(frames, vibekit.EventPendingSnapshot); got != 1 {
		t.Fatalf("a v3 connect wrote %d pending_snapshot frames, want exactly 1", got)
	}
	for _, typ := range []vibekit.EventType{vibekit.EventPermissionNeeded, vibekit.EventRunInputNeeded, vibekit.EventSteerQueued, vibekit.EventType("turn_state")} {
		if n := countType(frames, typ); n != 0 {
			t.Errorf("a v3 connect wrote %d per-item %s frames beside the aggregate", n, typ)
		}
	}
	snap, _ := frameOfType(frames, vibekit.EventPendingSnapshot)
	var payload vibekit.PendingSnapshotPayload
	if err := reencode(snap.Payload, &payload); err != nil {
		t.Fatalf("decode pending_snapshot: %v", err)
	}
	// 2 permissions + 1 run ask (seedPendingDecisions) + 1 steer.
	if len(payload.Items) != fixturePendingPerms+fixturePendingRunAsks+1 {
		t.Errorf("pending_snapshot carries %d items, want %d", len(payload.Items), fixturePendingPerms+fixturePendingRunAsks+1)
	}
	kinds := map[vibekit.EventType]int{}
	for _, raw := range payload.Items {
		var item vibekit.ServerEvent
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("pending_snapshot item is not an envelope: %v", err)
		}
		kinds[item.Type]++
	}
	if kinds[vibekit.EventPermissionNeeded] != fixturePendingPerms || kinds[vibekit.EventRunInputNeeded] != fixturePendingRunAsks || kinds[vibekit.EventSteerQueued] != 1 {
		t.Errorf("pending_snapshot item kinds = %v, want %d permission_needed, %d run_input_needed, 1 steer_queued",
			kinds, fixturePendingPerms, fixturePendingRunAsks)
	}
	version, _ := rt.versions.Current(subject.KindPending, "")
	want := vibekit.SubjectStamp{Kind: "pending", Version: version}
	if snap.Subject == nil || *snap.Subject != want {
		t.Errorf("pending_snapshot Subject = %+v, want %+v", snap.Subject, want)
	}
	if version == subject.Unminted {
		t.Error("the fixture's pending mutations moved nothing; the stamp asserts nothing")
	}
}

// TestConnect_V3EmptySetsAreOneFrameEach pins the case that matters: a per-item
// replay of an empty set writes nothing, so a row resolved elsewhere during the gap
// would stay on screen. An empty snapshot frame clears it and carries the stamp.
func TestConnect_V3EmptySetsAreOneFrameEach(t *testing.T) {
	rt := newBudgetRuntime(t)

	frames := connectFrames(t, rt, false)

	if len(frames) != 3 {
		t.Fatalf("a v3 connect on an empty workspace wrote %d frames, want 3 (connected, pending_snapshot, status_snapshot): %+v", len(frames), frames)
	}
	if frames[0].Type != vibekit.EventConnected || frames[1].Type != vibekit.EventPendingSnapshot || frames[2].Type != vibekit.EventStatusSnapshot {
		t.Fatalf("frame order = [%s %s %s], want [connected pending_snapshot status_snapshot]", frames[0].Type, frames[1].Type, frames[2].Type)
	}
	var pending vibekit.PendingSnapshotPayload
	if err := reencode(frames[1].Payload, &pending); err != nil || pending.Items == nil || len(pending.Items) != 0 {
		t.Errorf("empty pending_snapshot payload = %+v (%v), want items: []", frames[1].Payload, err)
	}
	var status vibekit.StatusSnapshotPayload
	if err := reencode(frames[2].Payload, &status); err != nil || status.Rows == nil || len(status.Rows) != 0 {
		t.Errorf("empty status_snapshot payload = %+v (%v), want rows: []", frames[2].Payload, err)
	}
	for i, kind := range []string{"", "pending", "status"} {
		if i == 0 {
			if frames[0].Subject != nil {
				t.Errorf("connected carries Subject %+v; it combines several subjects and must carry none", *frames[0].Subject)
			}
			continue
		}
		want := vibekit.SubjectStamp{Kind: kind, Version: subject.Unminted}
		if frames[i].Subject == nil || *frames[i].Subject != want {
			t.Errorf("%s Subject = %+v, want %+v", frames[i].Type, frames[i].Subject, want)
		}
	}
}

// TestConnect_V3StatusSnapshotCarriesTheWaitingSetMinusBusyChats pins the second
// aggregate and its busy filter: a chat whose turn is running must still suppress
// a stale waiting_on_user.
func TestConnect_V3StatusSnapshotCarriesTheWaitingSetMinusBusyChats(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bus.chatStatus.Merge("c-waiting", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser, Description: "pick one"})
	rt.bus.chatStatus.Merge("c-working", vibekit.ChatStatusPayload{Status: "in_progress"})
	rt.bus.chatStatus.Merge("c-busy", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser})
	rt.bridge.mgr.orInsert("c-busy")
	if e := rt.coord.StartTurn(t.Context(), "c-busy", vibekit.TurnSourcePrompt); e == 0 {
		t.Fatal("StartTurn refused")
	}

	frames := connectFrames(t, rt, false)

	snap, ok := frameOfType(frames, vibekit.EventStatusSnapshot)
	if !ok {
		t.Fatal("a v3 connect wrote no status_snapshot")
	}
	if n := countType(frames, vibekit.EventChatStatus); n != 0 {
		t.Errorf("a v3 connect wrote %d per-row chat_status frames beside the aggregate", n)
	}
	var payload vibekit.StatusSnapshotPayload
	if err := reencode(snap.Payload, &payload); err != nil {
		t.Fatalf("decode status_snapshot: %v", err)
	}
	if len(payload.Rows) != 1 || payload.Rows[0].ChatID != "c-waiting" || payload.Rows[0].Description != "pick one" {
		t.Errorf("status_snapshot rows = %+v, want the one non-busy waiting row", payload.Rows)
	}
	version, _ := rt.versions.Current(subject.KindStatus, "")
	want := vibekit.SubjectStamp{Kind: "status", Version: version}
	if snap.Subject == nil || *snap.Subject != want {
		t.Errorf("status_snapshot Subject = %+v, want %+v", snap.Subject, want)
	}
}

// TestConnect_LegacyKeepsThePerItemReplayAndNumericBounds pins the overlap for the
// v2 bundle: no aggregate frames, one frame per pending item and waiting row, and
// `connected` carrying floor/head as JSON numbers.
func TestConnect_LegacyKeepsThePerItemReplayAndNumericBounds(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 2)
	rt.bus.steers.SteerWaiting(ids[1], vibekit.SteerQueuedPayload{SteerID: "s1", Text: "steer text"})
	rt.bus.chatStatus.Merge("c-waiting", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser})
	rt.bus.emit(vibekit.ServerEvent{Type: vibekit.EventChatUpdated, ChatID: "c1"})

	frames := connectFrames(t, rt, true)

	for _, typ := range []vibekit.EventType{vibekit.EventPendingSnapshot, vibekit.EventStatusSnapshot, vibekit.EventType("turn_state")} {
		if n := countType(frames, typ); n != 0 {
			t.Errorf("a legacy connect wrote %d %s frames; the v2 bundle has no decoder for it", n, typ)
		}
	}
	if got := countType(frames, vibekit.EventPermissionNeeded); got != fixturePendingPerms {
		t.Errorf("legacy connect replayed %d permission_needed frames, want %d", got, fixturePendingPerms)
	}
	if got := countType(frames, vibekit.EventRunInputNeeded); got != fixturePendingRunAsks {
		t.Errorf("legacy connect replayed %d run_input_needed frames, want %d", got, fixturePendingRunAsks)
	}
	if got := countType(frames, vibekit.EventSteerQueued); got != 1 {
		t.Errorf("legacy connect replayed %d steer_queued frames, want 1", got)
	}
	if got := countType(frames, vibekit.EventChatStatus); got != 1 {
		t.Errorf("legacy connect replayed %d chat_status frames, want 1 (the waiting row)", got)
	}
	p := connectedOf(t, frames)
	if p.Floor == nil || p.Head == nil {
		t.Fatalf("legacy connected carries floor=%v head=%v, want both numbers", p.Floor, p.Head)
	}
	if *p.Floor != 0 {
		t.Errorf("legacy fresh connect floor = %d, want 0 (not resumed, so the v2 client refetches)", *p.Floor)
	}
	if head := rt.bus.fanout.Position().Head; *p.Head != head {
		t.Errorf("legacy connected head = %d, want the ring head %d", *p.Head, head)
	}
	// The v2 client reads numbers, so the wire must carry numbers, not strings.
	body := coldConnectAs(t, rt, true).Body.String()
	if !strings.Contains(body, `"floor":0`) || !strings.Contains(body, `"head":`+string(rune('0'+int(*p.Head)))) {
		t.Errorf("legacy connected does not carry numeric floor/head: %s", body)
	}
}

// TestConnect_V3ConnectedCarriesNoFloorOrHead: a v3 client takes those facts from
// the library's hello, so the application frame omits them.
func TestConnect_V3ConnectedCarriesNoFloorOrHead(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bus.emit(vibekit.ServerEvent{Type: vibekit.EventChatUpdated, ChatID: "c1"})

	p := connectPayload(t, rt, "")
	if p.Floor != nil || p.Head != nil {
		t.Errorf("v3 connected carries floor=%v head=%v, want neither", p.Floor, p.Head)
	}
	// The hello carries its own floor/head; the assertion is about the application
	// frame, so it reads that frame's bytes alone.
	frames, err := ssetest.ReadFrames(strings.NewReader(coldConnectAs(t, rt, false).Body.String()), 0)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	for _, f := range frames {
		if f.Event == "" && strings.Contains(f.Data, `"type":"connected"`) && (strings.Contains(f.Data, `"floor"`) || strings.Contains(f.Data, `"head"`)) {
			t.Errorf("v3 connected frame still carries floor/head on the wire: %s", f.Data)
		}
	}
}

// TestHandleSSE_CountsConnectsByWireGeneration pins the observability counter the
// legacy overlap is retired on.
func TestHandleSSE_CountsConnectsByWireGeneration(t *testing.T) {
	rt := newBudgetRuntime(t)
	coldConnectAs(t, rt, true)
	coldConnectAs(t, rt, false)
	coldConnectAs(t, rt, false)
	if got := rt.bus.legacyConnects.Load(); got != 1 {
		t.Errorf("legacy_connect = %d, want 1", got)
	}
	if got := rt.bus.v3Connects.Load(); got != 2 {
		t.Errorf("v3_connect = %d, want 2", got)
	}
}
