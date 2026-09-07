package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

// TestHandleSSE_AdvertisesReconnectDelay pins that the hub is CONSTRUCTED with
// the reconnect hint. Nothing else in the suite would notice its absence: the
// field is not a frame, so it carries no type and no id to assert on.
func TestHandleSSE_AdvertisesReconnectDelay(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	want := fmt.Sprintf("retry: %d\n\n", reconnectDelay.Milliseconds())
	if n := strings.Count(body, "retry: "); n != 1 {
		t.Fatalf("body carries %d retry: lines, want exactly 1 (a property of the connection, not of a frame): %q", n, body)
	}
	// Ahead of the replay and the handshake, so the delay is in effect before
	// the connection can first drop.
	if !strings.HasPrefix(body, want) {
		t.Errorf("body does not open with %q: %q", want, body)
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

// Replayed frames go straight to the response, not through the per-client delivery
// buffer, so a reconnect that missed hundreds of events still receives the NEWEST.
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

// TestHandleSSE_ReplaysFromTheCursorQueryParameter covers the resume the browser
// cannot ask for: EventSource sends Last-Event-ID on ITS OWN retry and on nothing
// else, so every reconnect the client drives itself carries the cursor as a query
// parameter instead.
func TestHandleSSE_ReplaysFromTheCursorQueryParameter(t *testing.T) {
	h, _, _ := newTestHub()

	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?last_event_id=2", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"chat_id":"c1"`) || strings.Contains(body, `"chat_id":"c2"`) {
		t.Errorf("replay included an event at or below the cursor: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"c3"`) {
		t.Errorf("replay missed the event after the cursor: %s", body)
	}
}

// TestHandleSSE_HeaderOutranksTheCursorParameter pins which side wins when both
// are present. Only the browser knows which event its own EventSource last
// delivered, so a stale parameter left on the URL may never override it.
func TestHandleSSE_HeaderOutranksTheCursorParameter(t *testing.T) {
	h, _, _ := newTestHub()

	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?last_event_id=2", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"chat_id":"c2"`) {
		t.Errorf("the parameter overrode the header: c2 was not replayed: %s", body)
	}
}

func TestAdoptCursorParam(t *testing.T) {
	cases := map[string]struct {
		url    string
		header string
		want   string
	}{
		"promotes a digit string":      {url: "/api/events?last_event_id=42", want: "42"},
		"header wins":                  {url: "/api/events?last_event_id=42", header: "7", want: "7"},
		"no cursor at all":             {url: "/api/events", want: ""},
		"empty parameter":              {url: "/api/events?last_event_id=", want: ""},
		"rejects a non-digit":          {url: "/api/events?last_event_id=12a", want: ""},
		"rejects a sign":               {url: "/api/events?last_event_id=-1", want: ""},
		"rejects a leading space":      {url: "/api/events?last_event_id=%2012", want: ""},
		"rejects past uint64 digits":   {url: "/api/events?last_event_id=123456789012345678901", want: ""},
		"accepts twenty digits":        {url: "/api/events?last_event_id=12345678901234567890", want: "12345678901234567890"},
		"header wins over a bad param": {url: "/api/events?last_event_id=nope", header: "9", want: "9"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.header != "" {
				req.Header.Set("Last-Event-ID", tc.header)
			}
			got := adoptCursorParam(req)
			if got != tc.want {
				t.Errorf("adoptCursorParam(%q, header %q) = %q, want %q", tc.url, tc.header, got, tc.want)
			}
			// The replay stays one code path, so the promotion has to be VISIBLE to
			// the sse library — which only ever reads the header.
			if h := req.Header.Get("Last-Event-ID"); h != tc.want {
				t.Errorf("header after adopt = %q, want %q", h, tc.want)
			}
		})
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

// Asserted through the MUX because the gate is a route wrapper applied at
// registration: a test calling handleSSE directly would bypass it and pass whether or
// not it is wired. An ungated route is checked too, because the gate must NOT become
// global — a health probe during wind-down is what reports the wind-down.
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

// The two replays that run AFTER the handshake, so also the fact that the handshake's
// write does not end the hook. The event log alone is not enough: a permission dialog
// that aged out of the ring leaves the agent blocked on an answer nothing renders, and
// a client connecting mid-turn has no event telling it the chat is busy. An early
// return anywhere in the hook drops the rest silently — the stream still looks healthy.
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

// The run-ask half, whose reason is stronger than the permission's: a parked run has
// no deadline of its own and the event does not re-fire, so a reload with no replay
// leaves the run parked with nothing on screen to answer it.
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

// A reconnect re-reads the notification toggles because the config may have been
// edited while SSE was down. A FRESH connection does not — the process just read them
// — and re-reading on every one costs a disk read plus a singleflight round per page
// load.
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

// The one field a client needs to apply a replayed step turn's transcript WITHOUT
// reading the chat as busy. The event is emitted rather than skipped because the
// snapshot is the only copy of an in-flight step's transcript, but unmarked the
// client latches `thinking` and re-latches on every reconnect with nothing to clear
// it, since a step's own turn_end is dropped by the workflow attribution gate.
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
			err := h.replayTurnState(func(evt vibekit.ServerEvent) (int, error) {
				if evt.Type != vibekit.EventTurnState {
					return 0, nil
				}
				p, ok := evt.Payload.(vibekit.TurnStatePayload)
				if !ok {
					t.Fatalf("turn_state payload = %T, want vibekit.TurnStatePayload", evt.Payload)
				}
				got = append(got, p)
				return 0, nil
			}, chatID, h.coord.turns.openTurns(), nil)
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

// replayedTurnState drives replayTurnState over one chat and returns its single
// turn_state payload. The two cap tests below differ only in how much the buffer
// holds, so the drive belongs in one place.
func replayedTurnState(t *testing.T, h *Runtime, chatID vibekit.ChatID) vibekit.TurnStatePayload {
	t.Helper()
	got := orderedTurnStates(t, h, chatID, nil)
	if len(got) != 1 {
		t.Fatalf("turn_state events = %d, want 1", len(got))
	}
	return got[0].payload
}

// replayedTurn is one turn_state the replay wrote. The chat id is kept beside the
// payload because the payload does not carry it and the ordering and filter tests
// below assert on WHICH chats were served.
type replayedTurn struct {
	chatID  vibekit.ChatID
	payload vibekit.TurnStatePayload
}

// orderedTurnStates drives replayTurnState and returns every turn_state it wrote, in
// WIRE ORDER — a slice rather than a map, because the order is what two of the tests
// below assert.
func orderedTurnStates(
	t *testing.T,
	rt *Runtime,
	chatFilter vibekit.ChatID,
	declared map[vibekit.ChatID]struct{},
) []replayedTurn {
	t.Helper()
	var got []replayedTurn
	err := rt.replayTurnState(func(evt vibekit.ServerEvent) (int, error) {
		if evt.Type != vibekit.EventTurnState {
			return 0, nil
		}
		p, ok := evt.Payload.(vibekit.TurnStatePayload)
		if !ok {
			t.Fatalf("turn_state payload = %T, want vibekit.TurnStatePayload", evt.Payload)
		}
		got = append(got, replayedTurn{chatID: evt.ChatID, payload: p})
		data, err := json.Marshal(evt)
		if err != nil {
			t.Fatalf("marshal turn_state for %q: %v", evt.ChatID, err)
		}
		// The REAL marshaled length, so the budget under test is charged what the wire
		// would charge it. A test-side zero would make the budget unspendable and every
		// assertion over it vacuous.
		return len(data), nil
	}, chatFilter, rt.coord.turns.openTurns(), declared)
	if err != nil {
		t.Fatalf("replayTurnState: %v", err)
	}
	return got
}

// servedChats is the chat ids a replay described, in wire order.
func servedChats(turns []replayedTurn) []vibekit.ChatID {
	ids := make([]vibekit.ChatID, 0, len(turns))
	for _, tn := range turns {
		ids = append(ids, tn.chatID)
	}
	return ids
}

// openBigTurn opens a turn on chatID and fills its buffer past every dimension of
// connectSnapshotCaps. The deltas go through the buffer's own Append* methods, so
// the flat-field/Blocks duplication is real: a cap reaching only one carrier
// halves the payload where it must divide it.
func openBigTurn(t *testing.T, h *Runtime, chatID vibekit.ChatID) {
	t.Helper()
	h.translateACPEvent(chatID, newChunkMsg("the reply opens here"))
	facts, ok := h.coord.turns.openTurns()[chatID]
	if !ok {
		t.Fatal("no open turn after a chunk; the fixture cannot exercise the cap")
	}
	facts.Buf.AppendThinkingDelta(strings.Repeat("r", 3<<20), "")
	facts.Buf.AppendTextDelta(strings.Repeat("c", 1<<20), "")
	for i := range 20 {
		facts.Buf.AppendToolCall(&vibekit.ToolCall{
			ID:     fmt.Sprintf("tool-%d", i),
			Output: strings.Repeat("o", 100<<10),
		})
	}
}

// TestReplayTurnState_MarksACappedSnapshotTruncated pins the marker to the CUT.
// Without it a client renders the tail of a 10 MB turn as the whole reply, which
// is the mistake design.md §3 retracted — the cap is admissible only because the
// payload says it happened.
func TestReplayTurnState_MarksACappedSnapshotTruncated(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	openBigTurn(t, h, chatID)

	got := replayedTurnState(t, h, chatID)
	if got.Message == nil {
		t.Fatal("the snapshot was withheld; a bounded snapshot is the point, not no snapshot")
	}
	if !got.Truncated {
		t.Error("truncated = false over a turn holding 3 MiB of reasoning; the client reads the tail as complete")
	}
	// The cap reached BOTH carriers of the turn's text. Reasoning is stored in
	// buf.Reasoning and in Blocks[i].Thinking, so a flat-field-only cap leaves
	// megabytes on the wire while reporting a bound.
	if got := len(got.Message.Reasoning); got > connectSnapshotCaps.ReasoningBytes {
		t.Errorf("reasoning = %d bytes, want <= %d", got, connectSnapshotCaps.ReasoningBytes)
	}
	blockText := 0
	for _, b := range got.Message.Blocks {
		blockText += len(b.Text) + len(b.Thinking)
	}
	if blockText > connectSnapshotCaps.BlockTextBytes {
		t.Errorf("block text = %d bytes, want <= %d; the second copy of the turn is uncapped",
			blockText, connectSnapshotCaps.BlockTextBytes)
	}
	if n := len(got.Message.ToolCalls); n > connectSnapshotCaps.ToolCalls {
		t.Errorf("tool calls = %d, want <= %d", n, connectSnapshotCaps.ToolCalls)
	}
	for _, tc := range got.Message.ToolCalls {
		if len(tc.Output) > connectSnapshotCaps.ToolOutputBytes {
			t.Errorf("%s output = %d bytes, want <= %d", tc.ID, len(tc.Output), connectSnapshotCaps.ToolOutputBytes)
		}
	}
}

// TestReplayTurnState_ASmallTurnIsNotMarkedTruncated is the other direction, and
// it is the one that keeps the marker meaningful: a client that sees `truncated`
// on every reconnect learns to ignore it.
func TestReplayTurnState_ASmallTurnIsNotMarkedTruncated(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	h.translateACPEvent(chatID, newChunkMsg("a short reply"))

	got := replayedTurnState(t, h, chatID)
	if got.Message == nil {
		t.Fatal("the snapshot was withheld for a turn well inside every cap")
	}
	if got.Truncated {
		t.Error("truncated = true for a 13-byte reply; the client would show a withheld-output note for nothing")
	}
	if got.Message.Content != "a short reply" {
		t.Errorf("content = %q, want it untouched", got.Message.Content)
	}
}

// --- the connect replay's two filters and its budget ---

// openSmallTurn opens a PROMPT-sourced turn on chatID and gives it just enough
// content for a snapshot to exist. Small deliberately: the tests below assert WHICH
// chats are served and in what order, never what each one costs. A prime-sourced
// turn would be withheld outright and an unstarted buffer reports no snapshot, so
// both facts are set here rather than assumed.
func openSmallTurn(tb testing.TB, rt *Runtime, id vibekit.ChatID, text string) {
	tb.Helper()
	rt.bridge.mgr.orInsert(id)
	if epoch := rt.coord.StartTurn(tb.Context(), id, vibekit.TurnSourcePrompt); epoch == 0 {
		tb.Fatalf("StartTurn(%q) refused, so the chat is not busy and nothing is replayed", id)
	}
	buf := rt.liveTurnBuffer(id)
	if buf == nil {
		tb.Fatalf("no live turn buffer for %q, so there is nothing to snapshot", id)
	}
	if !buf.StartTurn("m-" + string(id)) {
		tb.Fatalf("turn for %q was already started, so the fixture is not the one filling it", id)
	}
	buf.AppendTextDelta(text, "")
}

// declaredSet is the shape a parsed ?snapshot= parameter hands replayTurnState.
func declaredSet(ids ...vibekit.ChatID) map[vibekit.ChatID]struct{} {
	set := make(map[vibekit.ChatID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// TestReplayTurnState_SkipsAChatWithNoOpenTab pins the SERVER-side filter, the half
// that needs no client change: a busy chat with no row in the strip has no dot to
// feed and no transcript to draw, so its frame is pure cost. Skipped entirely rather
// than downgraded to a bare signal, because there is no surface the signal reaches.
func TestReplayTurnState_SkipsAChatWithNoOpenTab(t *testing.T) {
	rt := newBudgetRuntime(t)
	const tabbed vibekit.ChatID = "c-tabbed"
	const untabbed vibekit.ChatID = "c-untabbed"
	openSmallTurn(t, rt, tabbed, "the reader is looking at this")
	openSmallTurn(t, rt, untabbed, "nobody has this open")
	openBudgetChatTab(t, rt, tabbed)

	got := servedChats(orderedTurnStates(t, rt, "", nil))

	want := []vibekit.ChatID{tabbed}
	if !slices.Equal(got, want) {
		t.Errorf("replayTurnState served %v, want %v: a chat with no tab must get nothing at all",
			got, want)
	}
}

// TestReplayTurnState_ServesEveryBusyChatWhenTheTabStoreIsUnwired is the FAIL-OPEN
// guard, and it is why the rest of this package's tests keep passing: they all run on
// a runtime with no tab store. A closed default would break the suite and, far worse,
// would silently withhold every busy chat's transcript in production the first time
// the store was left unwired.
func TestReplayTurnState_ServesEveryBusyChatWhenTheTabStoreIsUnwired(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })
	if rt.tabs != nil {
		t.Fatal("the fixture wired a tab store, so it cannot exercise the unwired path")
	}
	const first vibekit.ChatID = "c-1"
	const second vibekit.ChatID = "c-2"
	openSmallTurn(t, rt, first, "one")
	openSmallTurn(t, rt, second, "two")

	got := orderedTurnStates(t, rt, "", nil)

	want := []vibekit.ChatID{first, second}
	if ids := servedChats(got); !slices.Equal(ids, want) {
		t.Fatalf("replayTurnState served %v, want %v: an unwired store must treat every chat as open",
			ids, want)
	}
	for _, tn := range got {
		if tn.payload.Message == nil {
			t.Errorf("%q got a bare signal; an unwired store must not withhold the snapshot either",
				tn.chatID)
		}
	}
}

// TestReplayTurnState_ServesTheDeclaredChatsSnapshotAndABareSignalForTheRest pins the
// second filter, the one the client drives. An open-but-undeclared chat is still
// BUSY, so it must keep saying so — it just does not need the transcript nobody is
// looking at.
func TestReplayTurnState_ServesTheDeclaredChatsSnapshotAndABareSignalForTheRest(t *testing.T) {
	rt := newBudgetRuntime(t)
	const onScreen vibekit.ChatID = "c-on-screen"
	const background vibekit.ChatID = "c-background"
	openSmallTurn(t, rt, onScreen, "the reply the reader is watching")
	openSmallTurn(t, rt, background, "a reply nobody has open")
	openBudgetChatTab(t, rt, onScreen)
	openBudgetChatTab(t, rt, background)

	got := orderedTurnStates(t, rt, "", declaredSet(onScreen))

	if len(got) != 2 {
		t.Fatalf("turn_state events = %d, want 2: an undeclared chat is still busy", len(got))
	}
	for _, tn := range got {
		switch tn.chatID {
		case onScreen:
			if tn.payload.Message == nil {
				t.Error("the declared chat got a bare signal, so the visible transcript is lost")
			}
		case background:
			if tn.payload.Message != nil {
				t.Error("an undeclared chat carried a snapshot; that is the payload being cut")
			}
			if tn.payload.Truncated {
				t.Error("truncated = true on a bare signal: nothing was withheld from a payload " +
					"carrying no message, and a marker here teaches a reader to ignore a real one")
			}
		default:
			t.Errorf("unexpected chat %q in the replay", tn.chatID)
		}
	}
}

// TestReplayTurnState_ServesDeclaredChatsFirstWhenTheBudgetIsShort covers the order
// AND the budget, which are one mechanism: six uncapped snapshots are ~333 KB against
// a 256 KB budget, so SOMETHING has to be refused, and which chats are refused must
// not depend on Go's map iteration order.
func TestReplayTurnState_ServesDeclaredChatsFirstWhenTheBudgetIsShort(t *testing.T) {
	t.Run("declared chats lead the wire and keep their snapshots", func(t *testing.T) {
		rt := newBudgetRuntime(t)
		ids := busyChatsWithHugeTurns(t, rt, fixtureBusyChats)
		// Declared chats that sort LAST by id, so a sort that ignored the declaration
		// would put them at the end of the wire behind four bare signals.
		lateA, lateB := ids[len(ids)-2], ids[len(ids)-1]

		got := orderedTurnStates(t, rt, "", declaredSet(lateA, lateB))

		if lead := servedChats(got)[:2]; !slices.Equal(lead, []vibekit.ChatID{lateA, lateB}) {
			t.Errorf("the wire leads with %v, want the declared chats %v first",
				lead, []vibekit.ChatID{lateA, lateB})
		}
		for _, tn := range got {
			declared := tn.chatID == lateA || tn.chatID == lateB
			if declared && tn.payload.Message == nil {
				t.Errorf("declared chat %q got a bare signal", tn.chatID)
			}
			if !declared && tn.payload.Message != nil {
				t.Errorf("undeclared chat %q carried a snapshot", tn.chatID)
			}
		}
	})

	t.Run("with every chat declared the budget cuts the tail by id", func(t *testing.T) {
		rt := newBudgetRuntime(t)
		ids := busyChatsWithHugeTurns(t, rt, fixtureBusyChats)

		got := orderedTurnStates(t, rt, "", declaredSet(ids...))

		if served := servedChats(got); !slices.Equal(served, ids) {
			t.Fatalf("replayTurnState served %v, want every busy chat in id order %v", served, ids)
		}
		// The budget is what makes this fail if it is deleted: six full snapshots fit
		// under no ceiling, so a run with none refused is a run with no budget.
		snapshots := 0
		for _, tn := range got {
			if tn.payload.Message != nil {
				snapshots++
			}
		}
		if snapshots == len(ids) {
			t.Errorf("all %d declared chats got a full snapshot, so nothing bounded the connect: "+
				"six of them are ~%d bytes of text against a %d byte budget",
				snapshots, len(ids)*connectSnapshotCaps.MaxTextBytes(), connectSnapshotBudget)
		}
		if snapshots == 0 {
			t.Error("no declared chat got a snapshot; the budget refused the payload it exists to admit")
		}
		// The refusals are a SUFFIX by id: the budget spends in wire order, so a chat
		// that got a snapshot cannot follow one that did not.
		seenBare := false
		for _, tn := range got {
			if tn.payload.Message == nil {
				seenBare = true
				continue
			}
			if seenBare {
				t.Errorf("%q carried a snapshot after an earlier chat was refused, so the budget "+
					"is not being spent in wire order", tn.chatID)
			}
		}
	})
}

// TestReplayTurnState_IsDeterministicallyOrdered is the assertion the budget makes
// necessary. Go randomises map iteration, so an unsorted candidate list picks
// arbitrary winners: the payload would differ run to run over an identical fixture,
// which makes every byte assertion over this path flaky rather than wrong.
func TestReplayTurnState_IsDeterministicallyOrdered(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, fixtureBusyChats)

	// Both the ORDER and which chats the budget served, because a stable order with
	// unstable winners is the same defect one field along.
	type shape struct {
		id       vibekit.ChatID
		snapshot bool
	}
	var first []shape
	for i := range 20 {
		got := make([]shape, 0, len(ids))
		for _, tn := range orderedTurnStates(t, rt, "", nil) {
			got = append(got, shape{id: tn.chatID, snapshot: tn.payload.Message != nil})
		}
		if i == 0 {
			first = got
			continue
		}
		if !slices.Equal(got, first) {
			t.Fatalf("iteration %d replayed %v, iteration 0 replayed %v: the replay order depends "+
				"on Go's map iteration", i, got, first)
		}
	}
	if len(first) != len(ids) {
		t.Errorf("replayed %d chats, want %d: the fixture is not the one being ordered",
			len(first), len(ids))
	}
}

func TestParseSnapshotChats(t *testing.T) {
	overCap := make([]string, 0, maxDeclaredSnapshotChats+2)
	wantOverCap := make([]vibekit.ChatID, 0, maxDeclaredSnapshotChats)
	for i := range maxDeclaredSnapshotChats + 2 {
		id := fmt.Sprintf("c-%02d", i)
		overCap = append(overCap, id)
		if i < maxDeclaredSnapshotChats {
			wantOverCap = append(wantOverCap, vibekit.ChatID(id))
		}
	}

	cases := []struct {
		name string
		// query is the raw parameter VALUE, already decoded, or absent when nil.
		query *string
		want  []vibekit.ChatID
	}{
		{name: "absent, which reads as declare nothing", query: nil, want: nil},
		{name: "present but empty", query: ptr(""), want: nil},
		{name: "one chat", query: ptr("c-1"), want: []vibekit.ChatID{"c-1"}},
		{
			name:  "several chats",
			query: ptr("c-1,c-2,c-3"),
			want:  []vibekit.ChatID{"c-1", "c-2", "c-3"},
		},
		{
			name:  "a malformed entry is dropped and the rest still declared",
			query: ptr("c-1,../etc/passwd,c-2"),
			want:  []vibekit.ChatID{"c-1", "c-2"},
		},
		{
			name: "every entry malformed reads as declare nothing rather than failing the connect",
			// The stream is the client's only recovery channel, so a mangled parameter
			// must not be the thing that keeps it closed.
			query: ptr("../,,%00"),
			want:  nil,
		},
		{
			name:  "a blank between separators is dropped",
			query: ptr("c-1,,c-2"),
			want:  []vibekit.ChatID{"c-1", "c-2"},
		},
		{
			name:  "over the cap, truncated to the first entries",
			query: ptr(strings.Join(overCap, ",")),
			want:  wantOverCap,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/events"
			if tc.query != nil {
				target += "?" + url.Values{snapshotParam: {*tc.query}}.Encode()
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)

			got := parseSnapshotChats(req)

			ids := make([]vibekit.ChatID, 0, len(got))
			for id := range got {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.want) {
				t.Errorf("parseSnapshotChats(%q) = %v, want %v", target, ids, tc.want)
			}
		})
	}
}

// ptr is the address-of helper the table above needs to tell an ABSENT parameter from
// a present-and-empty one; the two are different inputs with the same result.
func ptr[T any](v T) *T { return &v }
