package agent

// What the HANDSHAKE states about state a client already believes. Every other connect
// replay is POSITIVE — one frame per live thing — so a chat whose turn died with the
// previous process is never contradicted and its `thinking` latch is permanent. BusyChats
// is the negative half, and BusyStated is what bounds its blast radius: a list the server
// could not state completely must retract nothing at all.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// connectPayload decodes the ONE connected frame a cold connect writes.
func connectPayload(t *testing.T, rt *Runtime, query string) vibekit.ConnectedPayload {
	t.Helper()
	body := coldConnect(t, rt, query).Body.String()
	for frame := range strings.SplitSeq(body, fixtureFrameSeparator) {
		for line := range strings.SplitSeq(frame, "\n") {
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var msg struct {
				Type    vibekit.EventType        `json:"type"`
				Payload vibekit.ConnectedPayload `json:"payload"`
			}
			if err := json.Unmarshal([]byte(payload), &msg); err != nil {
				continue
			}
			if msg.Type == vibekit.EventConnected {
				return msg.Payload
			}
		}
	}
	t.Fatal("the cold connect wrote no connected frame")
	return vibekit.ConnectedPayload{}
}

func busySetOf(p vibekit.ConnectedPayload) map[vibekit.ChatID]bool {
	out := make(map[vibekit.ChatID]bool, len(p.BusyChats))
	for _, id := range p.BusyChats {
		out[id] = true
	}
	return out
}

func TestConnect_StatesTheBusySetWithNoChatFilter(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 3)

	p := connectPayload(t, rt, "?snapshot="+snapshotNone)

	if !p.BusyStated {
		t.Fatal("an unfiltered connect does not state its busy set, so the client retracts " +
			"nothing and every stale `thinking` survives the reconnect")
	}
	busy := busySetOf(p)
	for _, id := range ids {
		if !busy[id] {
			t.Errorf("chat %q is running its own prompt turn and is absent from busy_chats", id)
		}
	}
}

// A topic-filtered connect is scoped, so its list states NOTHING about the chats it omits
// — reading it as complete would clear a live turn's `thinking` on every other chat.
func TestConnect_WithhoutTheBusySetForAFilteredConnect(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, 3)

	p := connectPayload(t, rt, "?chat_id="+string(ids[0])+"&snapshot="+snapshotNone)

	if p.BusyStated {
		t.Error("a chat-filtered connect claims its busy set is COMPLETE, so the client " +
			"retracts `thinking` on every chat the filter excluded")
	}
	if len(p.BusyChats) != 0 {
		t.Errorf("a filtered connect carries %d busy chats; the list is withheld rather than "+
			"scoped, so a client cannot mistake it for the whole set", len(p.BusyChats))
	}
}

// A PRIME turn's frames are vibekit's own transcript replay and latch nothing, and a
// workflow STEP's turn belongs to its run — so neither names its chat busy, and the step
// case is the stuck-purple population the retraction exists to reach.
func TestConnect_TheBusySetExcludesPrimeAndStepTurns(t *testing.T) {
	rt := newBudgetRuntime(t)
	rt.bridge.mgr.orInsert("c-prime")
	rt.bridge.mgr.orInsert("c-step")
	rt.bridge.mgr.orInsert("c-own")
	if e := rt.coord.StartTurn(t.Context(), "c-prime", vibekit.TurnSourcePrime); e == 0 {
		t.Fatal("StartTurn refused the prime")
	}
	if e := rt.coord.StartTurn(t.Context(), "c-step", vibekit.TurnSourceWorkflowStep); e == 0 {
		t.Fatal("StartTurn refused the step")
	}
	if e := rt.coord.StartTurn(t.Context(), "c-own", vibekit.TurnSourcePrompt); e == 0 {
		t.Fatal("StartTurn refused the prompt")
	}

	busy := busySetOf(connectPayload(t, rt, "?snapshot="+snapshotNone))

	if busy["c-prime"] {
		t.Error("a prime turn names its chat busy, withholding the retraction for a turn " +
			"whose frames no client ever latched")
	}
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

	if !busySetOf(connectPayload(t, rt, "?snapshot="+snapshotNone))["c-admitted"] {
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

	p := connectPayload(t, rt, "?snapshot="+snapshotNone)

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

	p := connectPayload(t, rt, "?snapshot="+snapshotNone)

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

// The inventory is workspace-GLOBAL, so a chat filter does not scope it: withholding it
// there would make a topic-filtered client's run surfaces empty for no reason.
func TestConnect_StatesTheInventoryEvenForAFilteredConnect(t *testing.T) {
	rt := newBudgetRuntime(t)
	if err := rt.runs.leaseStore().Put(t.Context(), &runlease.Lease{WorkflowID: "wf_a", Recipe: "r", Origin: runlease.OriginManual}); err != nil {
		t.Fatalf("put: %v", err)
	}

	p := connectPayload(t, rt, "?chat_id=c1&snapshot="+snapshotNone)

	if !p.LiveRunsStated || len(p.LiveRuns) != 1 {
		t.Errorf("a filtered connect states live_runs_stated=%v with %d rows, want true with 1: "+
			"the inventory is workspace-global and has nothing to scope",
			p.LiveRunsStated, len(p.LiveRuns))
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

	fromHandshake := connectPayload(t, rt, "?snapshot="+snapshotNone).LiveRuns
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
