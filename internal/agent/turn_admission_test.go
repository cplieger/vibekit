package agent

// The admission slot against the REAL registry and coordinator: holder-keyed
// refusals, the bridge-ready wake, the prime window, StartTurn's stamp timing,
// and the full prompt path from ack to recovery.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// shrinkAdmissionWait bounds a deliberately contended full-path wait so a
// refusal test does not sit out the production budget. Not parallel-safe, so
// no test using it may call t.Parallel.
func shrinkAdmissionWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := command.AdmissionWait
	command.AdmissionWait = d
	t.Cleanup(func() { command.AdmissionWait = prev })
}

func seedChat(t *testing.T, cs *fakeChatStore, id vibekit.ChatID) {
	t.Helper()
	if err := cs.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

// The refusal arm keys on the HOLDER'S SOURCE, never on bridge liveness alone:
// a shell holder answers "starting" on a bridgeless AND on a bridged chat — a
// shell has a live bridge and no steerable turn — while a prompt-class holder
// splits on its bridge.
func TestReserveTurnForPrompt_RefusalKeysOnTheHoldersSource(t *testing.T) {
	cases := []struct {
		name    string
		holder  vibekit.TurnOpenSource
		bridged bool
		want    command.AdmissionOutcome
	}{
		{name: "shell holder on a bridgeless chat answers starting", holder: vibekit.TurnSourceLocalShell, want: command.AdmissionStarting},
		{name: "shell holder on a bridged chat answers starting", holder: vibekit.TurnSourceLocalShell, bridged: true, want: command.AdmissionStarting},
		{name: "prompt holder with no bridge answers starting at the budget", holder: vibekit.TurnSourcePrompt, want: command.AdmissionStarting},
		{name: "prompt holder with a live bridge answers busy", holder: vibekit.TurnSourcePrompt, bridged: true, want: command.AdmissionBusy},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			chatID := vibekit.ChatID("c-admission-" + string(rune('a'+i)))
			seedChat(t, cs, chatID)
			if tc.bridged {
				if _, err := h.coord.OpenBridge(t.Context(), chatID, ""); err != nil {
					t.Fatalf("OpenBridge: %v", err)
				}
			}
			if !h.coord.TryReserveTurn(chatID, tc.holder) {
				t.Fatal("setup: the admission slot was already held")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation(chatID) })

			got := h.coord.ReserveTurnForPrompt(t.Context(), chatID, 60*time.Millisecond)
			if got != tc.want {
				t.Errorf("ReserveTurnForPrompt(holder %v, bridged %v) = %v, want %v", tc.holder, tc.bridged, got, tc.want)
			}
		})
	}
}

// A freed slot admits a waiter, and a free slot admits immediately.
func TestReserveTurnForPrompt_AcquiresAFreeOrFreedSlot(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if got := h.coord.ReserveTurnForPrompt(t.Context(), "c1", 60*time.Millisecond); got != command.AdmissionAcquired {
		t.Fatalf("free slot = %v, want acquired", got)
	}
	// Held: a waiter parks, and the release admits exactly it.
	answered := make(chan command.AdmissionOutcome, 1)
	go func() {
		answered <- h.coord.ReserveTurnForPrompt(context.Background(), "c1", 5*time.Second)
	}()
	select {
	case got := <-answered:
		t.Fatalf("the waiter answered %v while the slot was held", got)
	case <-time.After(50 * time.Millisecond):
	}
	h.coord.ReleaseTurnReservation("c1")
	select {
	case got := <-answered:
		if got != command.AdmissionAcquired {
			t.Fatalf("waiter = %v, want acquired after the release", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never woke on the release")
	}
	h.coord.ReleaseTurnReservation("c1")
}

// The bridge-ready wake: a waiter parked behind a prompt-class holder answers
// plain-busy the moment the holder's bridge goes live — within the wake's
// latency, not the wait budget's. The fake's startGate holds the spawn OPEN
// between the forward attach and bridge-ready, so the earlier attach-time wake
// re-parks the waiter and only the explicit bridge-ready wake can answer it.
func TestReserveTurnForPrompt_BridgeReadyWakeAnswersTheWaiter(t *testing.T) {
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	startGate := make(chan struct{})
	br.mu.Lock()
	br.startGate = startGate
	br.mu.Unlock()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
		t.Fatal("setup: the admission slot was already held")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	const budget = 10 * time.Second
	answered := make(chan command.AdmissionOutcome, 1)
	go func() {
		answered <- h.coord.ReserveTurnForPrompt(context.Background(), "c1", budget)
	}()
	spawned := make(chan error, 1)
	go func() {
		_, err := h.coord.OpenBridge(t.Context(), "c1", "")
		spawned <- err
	}()

	// The spawn is held open: the holder is prompt-class but its bridge is
	// still starting, so the waiter stays parked through the attach-time wake.
	select {
	case got := <-answered:
		t.Fatalf("the waiter answered %v before the bridge was live", got)
	case <-time.After(100 * time.Millisecond):
	}

	start := time.Now()
	close(startGate)
	if err := <-spawned; err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	select {
	case got := <-answered:
		if got != command.AdmissionBusy {
			t.Fatalf("waiter = %v, want busy: the holder is prompt-class and its bridge is live", got)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("the waiter answered %v after bridge-ready, want the wake's latency, not a later state change's", elapsed)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the bridge-ready wake never fired: the waiter would sit out the whole budget")
	}
}

// The full 409-starting path: a prompt against a shell-held chat answers 409
// with the additive `reason":"starting"` on the wire, at the wait budget.
func TestPrompt_FullPath409StartingCarriesTheReason(t *testing.T) {
	shrinkAdmissionWait(t, 60*time.Millisecond)
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourceLocalShell) {
		t.Fatal("setup: the admission slot was already held")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if body.Reason != "starting" {
		t.Errorf(`body = %s, want the additive "reason":"starting"`, rec.Body.String())
	}
	// State parity: the refused prompt's user row is persisted exactly as an
	// accepted one's.
	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 || c.Messages[0].ID != "m-1" {
		t.Errorf("messages = %+v, want the refused prompt's user row persisted", c.Messages)
	}
}

// gateSpawn parks every bridge spawn on the returned gate, signalling entry.
// It installs the hook through the PUBLIC setter — the composition root's own
// path — so these tests also pin that the setter reaches the coordinator.
func gateSpawn(h *Runtime) (entered, gate chan struct{}) {
	entered = make(chan struct{})
	gate = make(chan struct{})
	var once sync.Once
	h.SetPreBridgeSpawn(func(ctx context.Context) {
		once.Do(func() { close(entered) })
		select {
		case <-gate:
		case <-ctx.Done():
		}
	})
	return entered, gate
}

// `!echo hi` during a prompt's BLOCKED SPAWN is refused immediately: the shell
// door's reservation is a try, never a wait, and the spawn window is exactly
// when the bridge slot does not exist to refuse for it.
func TestShellDuringABlockedSpawnIsRefusedImmediately(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)
	defer close(gate)

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // the prompt goroutine is parked inside its spawn, holding the reservation

	start := time.Now()
	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"!echo hi","message_id":"m-2"}`),
	})
	elapsed := time.Since(start)
	if rec.Code != http.StatusConflict {
		t.Fatalf("shell during spawn = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Errorf("body = %q, want busy", rec.Body.String())
	}
	if elapsed > time.Second {
		t.Errorf("the shell refusal took %v, want an immediate try — never a wait", elapsed)
	}
}

// waitForTurnEnded polls the replay buffer until want turn_ended events exist
// and returns their payloads. Deadline-bounded: it fails closed with the count.
func waitForTurnEnded(t *testing.T, h *Runtime, want int) []vibekit.TurnEndedPayload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := payloadsOfType[vibekit.TurnEndedPayload](t, bufferedSince(h, 0), vibekit.EventTurnEnded)
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn_ended events = %d, want %d", len(got), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// Metering and the model are stamped at StartTurn, with the bridge live: spend
// landing during the spawn window is excluded from the turn's delta, and a
// cold chat's turn carries the model the spawn persisted rather than an empty
// latch.
func TestPromptTurn_MeteringAndModelStampAtStartTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1") // cold: no model on the record yet
	entered, gate := gateSpawn(h)

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered
	// Spend lands while the spawn is still in flight — BEFORE StartTurn. A
	// baseline stamped at admission would charge it to this turn.
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Usage.Credits = 5
		c.Usage.HasRealData = true
		return true
	}); err != nil {
		t.Fatalf("stage spend: %v", err)
	}
	close(gate)

	ended := waitForTurnEnded(t, h, 1)
	if got := ended[0].CreditsDelta; got != 0 {
		t.Errorf("CreditsDelta = %v, want 0: spend during the spawn window is not the turn's", got)
	}
	if ended[0].Model == "" {
		t.Error("turn_ended model is empty on a cold chat; StartTurn runs after the spawn persisted the session's model")
	}
}

// The prime's own turn runs untouched between the reservation and StartTurn,
// and while it is open the chat's admission holder IS the prime: a competing
// prompt answers 409-starting even though the bridge is live, and the prime
// broadcasts no turn_ended of its own.
func TestPromptTurn_PrimeLifecycleBetweenReservationAndStartTurn(t *testing.T) {
	shrinkAdmissionWait(t, 60*time.Millisecond)
	h, cs, br := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{
			{ID: "u0", Role: vibekit.RoleUser, Content: "earlier question"},
			{ID: "a0", Role: vibekit.RoleAssistant, Content: "earlier answer"},
		}
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	sb, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch
	gate := make(chan struct{})
	br.blockOn = map[string]chan struct{}{vibekit.MethodPrompt: gate}

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"the real question","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, vibekit.MethodPrompt) // the PRIME's own session/prompt is in flight

	if source, held := h.coord.AdmissionHolderSource("c1"); !held || source != vibekit.TurnSourcePrime {
		t.Fatalf("admission holder = (%v, %v), want the open PRIME turn", source, held)
	}
	// A competing prompt during the prime window: bridged chat, still starting.
	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"competing","message_id":"m-2"}`),
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "starting") {
		t.Errorf("prompt during the prime = %d %q, want 409 with reason starting", rec.Code, rec.Body.String())
	}

	close(gate)
	ended := waitForTurnEnded(t, h, 1)
	if len(ended) != 1 {
		t.Fatalf("turn_ended events = %d, want exactly the real turn's — the prime is silent", len(ended))
	}
	// Two session/prompt calls made it to the wire: the prime, then the prompt.
	prompts := 0
	for _, m := range br.callLog() {
		if m == vibekit.MethodPrompt {
			prompts++
		}
	}
	if prompts != 2 {
		t.Errorf("session/prompt calls = %d, want 2 (the prime and the real prompt)", prompts)
	}
	// The prime's frames were never persisted: the transcript gained the user
	// row only.
	c, _ := cs.Get(t.Context(), "c1")
	for i := range c.Messages {
		if strings.Contains(c.Messages[i].Content, "earlier question") && c.Messages[i].Role == vibekit.RoleAssistant {
			t.Errorf("the prime's replay leaked into the transcript: %+v", c.Messages[i])
		}
	}
}

// The recovery arbitrates on the CAPTURED result through the real registry: a
// wire-ended empty turn re-prompts on a fresh session. The capture order is
// load-bearing — the registry drops the retained result at the last release,
// so a ReleaseTurn before the AwaitTurn would leave the recovery blind and
// this test red.
func TestPromptTurn_EmptyWireEndedTurnRecoversThroughTheRealRegistry(t *testing.T) {
	cs := newFakeChatStore()
	gate := make(chan struct{})
	var mu sync.Mutex
	var minted []*fakeBridge
	factory := func() ACPBridge {
		mu.Lock()
		defer mu.Unlock()
		br := newFakeBridge()
		if len(minted) == 0 {
			// Only the FIRST session's prompt is held open, so the wire turn_end
			// can land while the call is in flight; the retry's flows freely.
			br.blockOn = map[string]chan struct{}{vibekit.MethodPrompt: gate}
		}
		minted = append(minted, br)
		return br
	}
	h := New(context.Background(), t.TempDir(), factory, cs)
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	seedChat(t, cs, "c1")

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	// The first bridge's prompt call is in flight; the wire closes the turn
	// EMPTY while the response is still pending.
	first := func() *fakeBridge {
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			var br *fakeBridge
			if len(minted) > 0 {
				br = minted[0]
			}
			mu.Unlock()
			if br != nil {
				return br
			}
			if time.Now().After(deadline) {
				t.Fatal("the prompt goroutine never spawned a bridge")
			}
			time.Sleep(time.Millisecond)
		}
	}()
	waitForCall(t, first, vibekit.MethodPrompt)
	first.deliver(newTurnEndMsg("end_turn"))
	close(gate)

	// The recovery tears the session down and re-prompts on a fresh one.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		respawned := len(minted) >= 2
		var second *fakeBridge
		if respawned {
			second = minted[1]
		}
		mu.Unlock()
		if respawned && slices.Contains(second.callLog(), vibekit.MethodPrompt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the empty wire-ended turn was never retried: the captured result did not reach the recovery")
		}
		time.Sleep(time.Millisecond)
	}
	// The transcript records why the session changed.
	c, _ := cs.Get(t.Context(), "c1")
	var divider bool
	for i := range c.Messages {
		if c.Messages[i].EventKind == vibekit.EventInterrupted && strings.Contains(c.Messages[i].Content, "Session refreshed") {
			divider = true
		}
	}
	if !divider {
		t.Error("no 'Session refreshed, retrying' divider was persisted")
	}
}

// Shutdown mid-Call: the blocked prompt call unwinds, the turn finalizes, the
// in-flight count drains, and Shutdown returns.
func TestPromptTurn_ShutdownMidCallDrainsTheTurn(t *testing.T) {
	h, cs, br := newTestHub()
	seedChat(t, cs, "c1")
	gate := make(chan struct{})
	defer close(gate)
	br.blockOn = map[string]chan struct{}{vibekit.MethodPrompt: gate}

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	waitForCall(t, br, vibekit.MethodPrompt)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the in-flight prompt goroutine did not drain", err)
	}
}

// Shutdown between the ack and the goroutine's first real step: the in-flight
// registration happened BEFORE the ack, so Shutdown waits for the turn rather
// than racing past it, and the turn still reaches a terminal broadcast.
func TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)
	defer close(gate)

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v: the pre-goroutine window is registered in-flight before the ack", err)
	}
	// The goroutine ran to a terminal signal under shutdown rather than being
	// abandoned mid-flight: either the turn completed or its failure broadcast.
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(vibekit.EventTurnEnded)); missing != nil {
		if missingErr := missingEvents(types, string(vibekit.EventError)); missingErr != nil {
			t.Errorf("events = %v, want a terminal turn_ended or error for the in-flight prompt", types)
		}
	}
}

// A cancel landing between the ack and BeginPromptCall neither wedges the chat
// nor strands the turn: the cancel answers 200 with nothing to arm against,
// the turn completes, and the chat accepts the next prompt.
func TestPromptTurn_CancelBetweenAckAndBeginPromptCall(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered // parked in the spawn: no BeginPromptCall yet

	if rec := postCmd(t, h, vibekit.ClientCommand{Type: vibekit.CmdCancel, ChatID: "c1"}); rec.Code != http.StatusOK {
		t.Fatalf("cancel in the spawn window = %d, want 200", rec.Code)
	}
	close(gate)
	waitForTurnEnded(t, h, 1)

	// The chat is not wedged: the slots released, so a fresh prompt is admitted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := postCmd(t, h, vibekit.ClientCommand{
			Type: "prompt", ChatID: "c1",
			Payload: json.RawMessage(`{"text":"again","message_id":"m-2"}`),
		})
		if rec.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a follow-up prompt never got in: last answer %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForTurnEnded(t, h, 2)
}
