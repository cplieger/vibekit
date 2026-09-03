package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The SSE transport (fan-out, replay ring, Last-Event-ID resume, slow-client
// eviction, keepalives) is github.com/cplieger/webhttp/sse and is tested
// there. These tests pin vibekit's layer: emit marshaling + chat topics, the
// connected handshake's floor/head payload, initial-state replay, and the
// draining gate.

// --- emit / replay buffer ---

func TestEmit_AppendsToReplayBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})

	evts := h.bus.fanout.Buffered()
	if len(evts) != 2 {
		t.Fatalf("replay len = %d, want 2", len(evts))
	}
	if evts[0].Event.Topic != "c1" || evts[1].Event.Topic != "c2" {
		t.Errorf("replay topics: %q, %q", evts[0].Event.Topic, evts[1].Event.Topic)
	}
	if evts[0].ID >= evts[1].ID {
		t.Errorf("event IDs not monotonic: %d → %d", evts[0].ID, evts[1].ID)
	}
	if !strings.Contains(string(evts[0].Event.Data), `"chat_id":"c1"`) {
		t.Errorf("payload not the marshaled ServerEvent: %s", evts[0].Event.Data)
	}
}

func TestEmit_CapsBufferAtReplayBufSize(t *testing.T) {
	h, _, _ := newTestHub()
	for range replayBufSize + 100 {
		h.bus.emit(vibekit.ServerEvent{Type: "test"})
	}
	floor, head := h.bus.fanout.Bounds()
	if head-floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want cap %d", head-floor+1, replayBufSize)
	}
}

func TestEmit_TopicCarriesChatID(t *testing.T) {
	// Per-chat delivery filtering is the sse library's tested behavior;
	// vibekit's contract is that emit maps ChatID onto the event topic
	// (empty ChatID = global broadcast) so that filtering applies.
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "connected"}) // global: empty topic

	got := h.bus.fanout.Buffered()
	if len(got) != 3 {
		t.Fatalf("buffered = %d events, want 3", len(got))
	}
	wantTopics := []string{"c1", "c2", ""}
	for i, e := range got {
		if e.Event.Topic != wantTopics[i] {
			t.Errorf("event %d topic = %q, want %q", i, e.Event.Topic, wantTopics[i])
		}
	}
}

// --- HandleSSE (integration-ish, direct call) ---

func TestHandleSSE_EmitsConnectedHandshake(t *testing.T) {
	h, _, _ := newTestHub()
	// Seed 3 events so the replay buffer has a known floor/head.
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"connected"`) {
		t.Errorf("SSE body missing connected event: %q", body)
	}
	// Floor == first emitted event id (1), head == last emitted id (3).
	if !strings.Contains(body, `"floor":1`) {
		t.Errorf("connected payload missing floor: %s", body)
	}
	if !strings.Contains(body, `"head":3`) {
		t.Errorf("connected payload missing head: %s", body)
	}
	// The handshake frame carries the head as its SSE id, so a client that
	// connects and immediately drops resumes from head, not 0.
	if !strings.Contains(body, "id: 3\n") {
		t.Errorf("handshake frame missing id: 3: %q", body)
	}
}

func TestReplayBounds_EmptyBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	floor, head := h.bus.fanout.Bounds()
	if floor != 0 {
		t.Errorf("empty floor = %d, want 0", floor)
	}
	if head != 0 {
		t.Errorf("empty head = %d, want 0", head)
	}
}

func TestReplayBounds_FollowsBufferWindow(t *testing.T) {
	h, _, _ := newTestHub()
	// Overflow the ring so the floor advances past 1.
	for range replayBufSize + 5 {
		h.bus.emit(vibekit.ServerEvent{Type: "test"})
	}
	floor, head := h.bus.fanout.Bounds()
	if floor <= 1 {
		t.Errorf("floor = %d, want > 1 after overflow", floor)
	}
	wantHead := uint64(replayBufSize + 5)
	if head != wantHead {
		t.Errorf("head = %d, want %d", head, wantHead)
	}
	if head-floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want %d", head-floor+1, replayBufSize)
	}
}

func TestHandleSSE_ReplaysSinceLastEventID(t *testing.T) {
	h, _, _ := newTestHub()

	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1") // skip event 1 only
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Errorf("replay included event <= Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"c2"`) || !strings.Contains(body, `"chat_id":"c3"`) {
		t.Errorf("replay missed events after Last-Event-ID: %s", body)
	}
}

// TestHandleSSE_ReplaysNewestBeyondClientBuffer pins the replay-delivery
// guarantee across a large gap: replayed frames are written directly to the
// response (not through the per-client delivery buffer), so a reconnect that
// missed hundreds of events still receives the NEWEST ones.
func TestHandleSSE_ReplaysNewestBeyondClientBuffer(t *testing.T) {
	h, _, _ := newTestHub()

	const n = 300
	for i := 1; i <= n; i++ {
		h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: vibekit.ChatID(fmt.Sprintf("c%d", i))})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, fmt.Sprintf(`"chat_id":"c%d"`, n)) {
		t.Errorf("replay dropped the newest event c%d", n)
	}
	if !strings.Contains(body, `"chat_id":"c290"`) {
		t.Error("replay dropped event c290 past the old 256 client-buffer cap")
	}
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Error("replay included c1 which is <= Last-Event-ID")
	}
}

func TestHandleSSE_RejectsNonFlusher(t *testing.T) {
	h, _, _ := newTestHub()
	rec := &nonFlusherWriter{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	h.handleSSE(rec, req)
	if rec.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.status)
	}
}

// TestRegisterRoutes_DrainingGate asserts the drain refusal through the MUX, not
// by calling a handler directly. That is the whole point: the gate is a route
// wrapper applied at registration (agent.refuseWhenDraining), so a test that calls
// handleSSE or the dispatcher directly would bypass it and pass whether or not it
// is wired. Both gated routes are checked, and one ungated route is checked too,
// because the gate must NOT become global — the middleware chain also covers
// /api/health and the static assets, and a health probe during wind-down is what
// reports the wind-down.
func TestRegisterRoutes_DrainingGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"event stream", http.MethodGet, "/api/events", ""},
		{"command", http.MethodPost, "/api/command", `{"type":"create_chat","request_id":"r1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			h.lifecycle.draining.Store(true)

			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			// A bounded context so a REGRESSION fails fast instead of hanging:
			// without the gate the event stream opens and blocks forever, which
			// would turn this test into a 10-minute timeout rather than a failure
			// naming the status it got.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			req := httptest.NewRequest(tc.method, tc.path, body).WithContext(ctx)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 while draining", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "shutting down") {
				t.Errorf("body = %q, want vibekit's shutting-down envelope", rec.Body.String())
			}
		})
	}

	t.Run("an ungated route still answers while draining", func(t *testing.T) {
		h, _, _ := newTestHub()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		h.lifecycle.draining.Store(true)

		req := httptest.NewRequest(http.MethodGet, "/api/config-template", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusServiceUnavailable {
			t.Error("an ungated route answered 503: the drain gate has become global")
		}
	})
}

// --- Helper: a ResponseWriter with no Flusher ---

type nonFlusherWriter struct {
	hdr    http.Header
	body   strings.Builder
	status int
}

func (w *nonFlusherWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}
func (w *nonFlusherWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *nonFlusherWriter) WriteHeader(code int)        { w.status = code }

// BenchmarkEmit measures the marshal+publish hot path (ring append; fan-out
// scaling is benchmarked in the sse library).
func BenchmarkEmit(b *testing.B) {
	h, _, _ := newTestHub()
	evt := vibekit.ServerEvent{Type: "chat_updated", ChatID: "bench"}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.bus.emit(evt)
	}
}

// TestSupervisedState_Property is GONE with supervisedState. Its invariants were
// about per-turn TRUST — a way to wave past vibekit's per-write staging gate for
// the rest of a turn. KAS reviews the whole turn in one approval, so there is no
// per-write gate and nothing to trust past.

// TestHandleSSE_ReplaysTheStateAClientCannotDeriveFromTheEventLog pins the two
// replays that run AFTER the handshake, and therefore the fact that the handshake's
// write does not end the hook.
//
// Both exist because the event log alone is not enough. A permission dialog that
// aged out of the replay ring would leave the agent blocked on an answer the
// reconnected client never renders — the turn hangs with no visible cause. And a
// client that connects mid-turn has no event to tell it a chat is busy, so it draws
// an idle chat that is actually streaming. An early return anywhere in the hook
// drops the rest silently: the stream still opens and looks healthy.
func TestHandleSSE_ReplaysTheStateAClientCannotDeriveFromTheEventLog(t *testing.T) {
	h, _, br := newTestHub()

	// A pending ask that predates this connection.
	h.bus.pendingPerms.Add(9, vibekit.NewEvent(vibekit.EventPermissionNeeded, "c1",
		vibekit.PermissionNeededPayload{RequestID: 9}))
	// And a chat with an OPEN TURN, which is what synthesizes turn_state. The turn
	// rather than the prompt slot: an agent-initiated turn holds no slot, and it is
	// the class this replay exists for.
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	if h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt) == 0 {
		t.Fatal("the fixture could not open a turn")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?chat_id=c1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"connected"`) {
		t.Fatalf("no handshake, so the stream never opened: %q", body)
	}
	if !strings.Contains(body, `"type":"permission_needed"`) {
		t.Errorf("a pending permission was not replayed, so the agent stays blocked on an "+
			"answer this client will never show: %q", body)
	}
	if !strings.Contains(body, `"type":"turn_state"`) {
		t.Errorf("a chat mid-turn was replayed as nothing, so a client connecting during a turn "+
			"draws it idle: %q", body)
	}
}

// TestHandleSSE_ReplaysAParkedStepsQuestion pins the run-ask half of the connect
// replay.
//
// The reason is stronger than the permission's: a parked run has no deadline of
// its own, so an ask this client saw an hour ago is still the only thing between
// that run and its next step — and the event does not re-fire, so a reload with no
// replay leaves the run parked with nothing on screen to answer it.
func TestHandleSSE_ReplaysAParkedStepsQuestion(t *testing.T) {
	h, _, _ := newTestHub()
	h.runs.asks.Add(&runAsk{
		chatID: "c1",
		payload: vibekit.RunInputNeededPayload{
			WorkflowID: "wf_1", AskID: "a1", NodeID: "review", Question: "which branch?",
		},
	})

	replay := func(t *testing.T) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/api/events?chat_id=c1", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.handleSSE(rec, req)
		return rec.Body.String()
	}

	body := replay(t)
	if !strings.Contains(body, `"type":"run_input_needed"`) {
		t.Fatalf("a parked step's question was not replayed, so the run stays parked with "+
			"nothing on screen to answer it: %q", body)
	}
	if !strings.Contains(body, "which branch?") {
		t.Errorf("the replayed ask carried no question, and no endpoint has one: %q", body)
	}

	// After the answer there is nothing to replay: the claim deleted the entry, so
	// a second connection must not re-offer a card whose request is settled.
	if _, ok := h.runs.asks.TakeIfPresent("wf_1", "a1"); !ok {
		t.Fatal("Setup: the ask could not be claimed")
	}
	if after := replay(t); strings.Contains(after, `"type":"run_input_needed"`) {
		t.Errorf("an answered ask was replayed to a later connection: %q", after)
	}
}

// TestHandleSSE_ReloadsPushPreferencesOnlyForAReconnect pins which connection
// re-reads the notification toggles from disk.
//
// A reconnect is the case that needs it: the config may have been edited while SSE
// was down, and a container restart is not required to pick that up. A FRESH
// connection is not — the process just read them at startup — and re-reading on
// every one turns each page load into a disk read plus a singleflight round.
func TestHandleSSE_ReloadsPushPreferencesOnlyForAReconnect(t *testing.T) {
	cases := []struct {
		name        string
		lastEventID string
		wantReloads int32
	}{
		{name: "a reconnect re-reads them", lastEventID: "2", wantReloads: 1},
		{name: "a fresh connection does not", lastEventID: "", wantReloads: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newFakeChatStore()
			fp := &recordingPush{sends: make(chan string, 1)}
			h := New(context.Background(), t.TempDir(),
				func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
			cs.Bus = h
			h.mcpRegistry.SignalReady()
			h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
			h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})

			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
			if tc.lastEventID != "" {
				req.Header.Set("Last-Event-ID", tc.lastEventID)
			}
			h.handleSSE(httptest.NewRecorder(), req)

			if got := fp.reloads.Load(); got != tc.wantReloads {
				t.Errorf("ReloadPreferences called %d times with Last-Event-ID %q, want %d",
					got, tc.lastEventID, tc.wantReloads)
			}
		})
	}
}

// TestReplayTurnState_MarksAStepDrivenTurnAsTheRunsOwn pins the one field a client
// needs to apply a replayed step turn's transcript WITHOUT reading the chat as busy.
//
// The event is emitted rather than skipped because the snapshot is the only copy of
// an in-flight step's transcript — nothing persists it — so skipping it loses the
// step's output on every refresh. Unmarked, though, the client's turn_state handler
// latches `thinking`, and it re-latches on every reconnect for the length of the
// run with nothing to clear it, since a step's own turn_end is dropped by the
// workflow attribution gate.
func TestReplayTurnState_MarksAStepDrivenTurnAsTheRunsOwn(t *testing.T) {
	cases := []struct {
		name string
		msg  *vibekit.RPCResponse
		want bool
	}{
		{
			name: "a workflow step's fold",
			msg:  newStepChunkMsg("the step wrote this", "wf-1", "root/step"),
			want: true,
		},
		{
			name: "the chat's own agent-initiated turn",
			msg:  newChunkMsg("the agent woke itself"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			const chatID vibekit.ChatID = "c1"
			_ = cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

			h.translateACPEvent(chatID, tc.msg)

			var got []vibekit.TurnStatePayload
			err := h.replayTurnState(func(evt vibekit.ServerEvent) error {
				if evt.Type != vibekit.EventTurnState {
					return nil
				}
				p, ok := evt.Payload.(vibekit.TurnStatePayload)
				if !ok {
					t.Fatalf("turn_state payload = %T, want vibekit.TurnStatePayload", evt.Payload)
				}
				got = append(got, p)
				return nil
			}, chatID, h.coord.turns.openTurns())
			if err != nil {
				t.Fatalf("replayTurnState: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("turn_state events = %d, want 1: the in-flight transcript is the only "+
					"copy there is, so it must still be replayed", len(got))
			}
			if got[0].WorkflowStep != tc.want {
				t.Errorf("workflow_step = %v, want %v", got[0].WorkflowStep, tc.want)
			}
			if got[0].Message == nil {
				t.Error("the snapshot was withheld, so the step's streamed output is lost on refresh")
			}
		})
	}
}
