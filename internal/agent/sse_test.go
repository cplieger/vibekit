package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/sse"
	"github.com/cplieger/sse/ssetest"
	"github.com/cplieger/vibekit/internal/liveness"
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The SSE transport (fan-out, replay ring, the hello, Last-Event-ID resume,
// slow-client eviction, keepalives) is github.com/cplieger/sse and is
// tested there. These tests pin vibekit's layer: emit marshaling + chat topics,
// the frame-cap substitute, the connected handshake, the initial-state hook, and
// the draining gate.

// --- emit / replay buffer ---

func TestEmit_AppendsToReplayBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})

	evts := h.bus.fanout.Snapshot()
	if len(evts) != 2 {
		t.Fatalf("replay len = %d, want 2", len(evts))
	}
	if evts[0].Event.Topic != "c1" || evts[1].Event.Topic != "c2" {
		t.Errorf("replay topics: %q, %q", evts[0].Event.Topic, evts[1].Event.Topic)
	}
	if evts[0].Offset >= evts[1].Offset {
		t.Errorf("event offsets not monotonic: %d → %d", evts[0].Offset, evts[1].Offset)
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
	pos := h.bus.fanout.Position()
	if pos.Head-pos.Floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want cap %d", pos.Head-pos.Floor+1, replayBufSize)
	}
}

func TestEmit_TopicCarriesChatID(t *testing.T) {
	// vibekit's contract is that emit maps ChatID onto the event topic (empty
	// ChatID = global broadcast); nothing subscribes with a topic since the
	// stream is unfiltered, so the topic is diagnostic.
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "connected"}) // global: empty topic

	got := h.bus.fanout.Snapshot()
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

// TestEmit_AStampedFrameOverTheCapBecomesSubjectChanged pins the frame-cap
// substitute: a message_appended whose tool outputs sum past sse.MaxFrameBytes is
// refused by the hub, and what enters the ring instead is one subject_changed
// carrying the refused frame's stamp, with a Warn naming the type and the size. A
// frame the cap admits publishes intact.
func TestEmit_AStampedFrameOverTheCapBecomesSubjectChanged(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h, _, _ := newTestHub()

	huge := &vibekit.Message{ID: "m1", Role: vibekit.RoleAssistant}
	perCall := strings.Repeat("o", maxTurnToolOutputOne)
	for len(huge.ToolCalls)*maxTurnToolOutputOne < maxTurnToolOutputTotal {
		huge.ToolCalls = append(huge.ToolCalls, vibekit.ToolCall{ID: fmt.Sprintf("tc%d", len(huge.ToolCalls)), Output: perCall})
	}
	stamped := vibekit.NewEvent(vibekit.EventMessageAppended, "c1", huge)
	stamped.Subject = &vibekit.SubjectStamp{Kind: string(subject.KindChat), Ref: "c1", Version: "7"}
	h.bus.emit(stamped)

	small := vibekit.NewEvent(vibekit.EventToolCall, "c1", vibekit.ToolCallPayload{
		MessageID: "m1", ToolCall: vibekit.ToolCall{ID: "tc-one", Output: perCall},
	})
	h.bus.emit(small)

	ring := h.bus.fanout.Snapshot()
	if len(ring) != 2 {
		t.Fatalf("ring holds %d frames, want 2 (the substitute and the intact tool_call)", len(ring))
	}
	var substitute vibekit.ServerEvent
	if err := reencodeBytes(ring[0].Event.Data, &substitute); err != nil {
		t.Fatalf("decode ring[0]: %v", err)
	}
	if substitute.Type != vibekit.EventSubjectChanged || substitute.ChatID != "c1" {
		t.Fatalf("ring[0] = %s for %q, want subject_changed for c1", substitute.Type, substitute.ChatID)
	}
	if substitute.Subject == nil || *substitute.Subject != *stamped.Subject {
		t.Errorf("subject_changed Subject = %+v, want the refused frame's %+v", substitute.Subject, *stamped.Subject)
	}
	if !strings.Contains(string(ring[1].Event.Data), `"type":"tool_call"`) {
		t.Errorf("ring[1] is not the intact tool_call: %.80s", ring[1].Event.Data)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "type=message_appended") || !strings.Contains(logs.String(), "bytes=") {
		t.Errorf("no Warn naming message_appended and the size: %s", logs.String())
	}
}

// TestEmit_AnUnstampedFrameOverTheCapIsDroppedWithAnError: with no subject there is
// no fetch instruction to substitute, so the frame is dropped and the log is the
// only signal.
func TestEmit_AnUnstampedFrameOverTheCapIsDroppedWithAnError(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h, _, _ := newTestHub()

	h.bus.emit(vibekit.NewEvent(vibekit.EventError, "c1", vibekit.ErrorPayload{
		Code: "huge", Message: strings.Repeat("x", sse.MaxFrameBytes+1),
	}))

	if n := len(h.bus.fanout.Snapshot()); n != 0 {
		t.Errorf("ring holds %d frames, want 0: an unstamped over-cap frame has no substitute", n)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "type=error") {
		t.Errorf("no Error naming the dropped frame: %s", logs.String())
	}
}

func reencodeBytes(data []byte, into any) error {
	return reencode(rawJSON(data), into)
}

// rawJSON lets reencode carry already-encoded bytes without a second marshal of a
// decoded map.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// --- HandleSSE (integration-ish, direct call) ---

// TestHandleSSE_AdvertisesReconnectDelay pins that the hub is CONSTRUCTED with
// the reconnect hint. Nothing else in the suite would notice its absence: the
// field is not a frame, so it carries no type and no id to assert on.
func TestHandleSSE_AdvertisesReconnectDelay(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})

	body := coldConnect(t, h, "").Body.String()
	want := fmt.Sprintf("retry: %d\n\n", liveness.ReconnectDelay.Milliseconds())
	if n := strings.Count(body, "retry: "); n != 1 {
		t.Fatalf("body carries %d retry: lines, want exactly 1 (a property of the connection, not of a frame): %q", n, body)
	}
	// Ahead of the replay and the handshake, so the delay is in effect before
	// the connection can first drop.
	if !strings.HasPrefix(body, want) {
		t.Errorf("body does not open with %q: %q", want, body)
	}
}

// TestHandleSSE_KeepaliveIsANamedIDLessFrameOutsideTheRing pins that the hub is
// CONSTRUCTED with the named keepalive: the frame carries the name the client
// listens for, no id: (so Last-Event-ID stays on the last real event), and it
// never enters the replay ring. Serial: it writes the package var the hub reads
// at construction.
func TestHandleSSE_KeepaliveIsANamedIDLessFrameOutsideTheRing(t *testing.T) {
	prev := keepaliveInterval
	keepaliveInterval = 10 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = prev })
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	headBefore := h.bus.fanout.Position().Head

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	var beats []string
	for frame := range strings.SplitSeq(body, "\n\n") {
		if strings.Contains(frame, "event: heartbeat") {
			beats = append(beats, frame)
		}
	}
	if len(beats) == 0 {
		t.Fatalf("handleSSE body carries no event: heartbeat frame, want at least one: %q", body)
	}
	for _, frame := range beats {
		hasData := false
		for line := range strings.SplitSeq(frame, "\n") {
			if strings.HasPrefix(line, "id:") {
				t.Errorf("handleSSE keepalive frame %q carries an id: line, want none", frame)
			}
			if strings.HasPrefix(line, "data: ") {
				hasData = true
			}
		}
		if !hasData {
			t.Errorf("handleSSE keepalive frame %q carries no data: line, want one so the browser dispatches it", frame)
		}
	}
	if headAfter := h.bus.fanout.Position().Head; headAfter != headBefore {
		t.Errorf("handleSSE ring head after keepalives = %d, want %d (a keepalive must not enter the replay ring)", headAfter, headBefore)
	}
}

// cursorAt is the Last-Event-ID a client that last saw offset holds on this hub.
func cursorAt(h *Runtime, offset uint64) string {
	return sse.Cursor{Epoch: h.bus.fanout.Position().Epoch, Offset: offset}.String()
}

func TestHandleSSE_ReplaysSinceLastEventID(t *testing.T) {
	h, _, _ := newTestHub()

	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set(wireHeader, "1")
	req.Header.Set("Last-Event-ID", cursorAt(h, 1)) // skip event 1 only
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Errorf("replay included event <= Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"c2"`) || !strings.Contains(body, `"chat_id":"c3"`) {
		t.Errorf("replay missed events after Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"resumed":true`) {
		t.Errorf("the hello does not report the cursor as resumed: %s", body)
	}
}

// A resume past the reply cap is a gap the digest reconciles rather than a
// kilo-frame replay: the hello says so and no ring frame is replayed.
func TestHandleSSE_AResumePastTheReplyCapIsAGap(t *testing.T) {
	h, _, _ := newTestHub()
	for i := 1; i <= replyMaxEvents+50; i++ {
		h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: vibekit.ChatID(fmt.Sprintf("c%d", i))})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set(wireHeader, "1")
	req.Header.Set("Last-Event-ID", cursorAt(h, 1))
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	frames, err := ssetest.ReadFrames(strings.NewReader(rec.Body.String()), 0)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	replayed := 0
	for _, f := range frames {
		if f.ID != "" {
			replayed++
		}
	}
	if replayed != 0 {
		t.Errorf("a resume %d frames behind replayed %d ring frames, want 0 past the %d cap", replyMaxEvents+49, replayed, replyMaxEvents)
	}
	if !strings.Contains(rec.Body.String(), `"verdict":"gap_budget"`) {
		t.Errorf("the hello does not report gap_budget: %.300s", rec.Body.String())
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

// The state the hook writes AFTER the handshake, so also the fact that the
// handshake's write does not end the hook. The event log alone is not enough: a
// permission dialog that aged out of the ring leaves the agent blocked on an answer
// nothing renders. On a v3 connect it rides the aggregate; on a legacy connect its
// own frame. Neither shape synthesizes a turn_state: a chat mid-turn is named in
// busy_chats and its content comes from GET /api/chats/{id}.
func TestHandleSSE_ReplaysTheStateAClientCannotDeriveFromTheEventLog(t *testing.T) {
	h, _, br := newTestHub()

	h.bus.pendingPerms.Add(9, vibekit.NewEvent(vibekit.EventPermissionNeeded, "c1",
		vibekit.PermissionNeededPayload{RequestID: 9}))
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	if h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt) == 0 {
		t.Fatal("the fixture could not open a turn")
	}

	for _, legacy := range []bool{false, true} {
		frames := connectFrames(t, h, legacy)
		if _, ok := frameOfType(frames, vibekit.EventConnected); !ok {
			t.Fatalf("legacy=%v: no handshake, so the stream never opened", legacy)
		}
		if !busySetOf(connectedOf(t, frames))["c1"] {
			t.Errorf("legacy=%v: the chat mid-turn is absent from busy_chats", legacy)
		}
		if n := countType(frames, vibekit.EventType("turn_state")); n != 0 {
			t.Errorf("legacy=%v: the connect synthesized %d turn_state frames; that channel is gone", legacy, n)
		}
		if legacy {
			if n := countType(frames, vibekit.EventPermissionNeeded); n != 1 {
				t.Errorf("legacy connect replayed %d permission_needed frames, want 1", n)
			}
			continue
		}
		snap, ok := frameOfType(frames, vibekit.EventPendingSnapshot)
		if !ok {
			t.Fatal("v3 connect wrote no pending_snapshot")
		}
		var payload vibekit.PendingSnapshotPayload
		if err := reencode(snap.Payload, &payload); err != nil {
			t.Fatalf("decode pending_snapshot: %v", err)
		}
		if len(payload.Items) != 1 || !strings.Contains(string(payload.Items[0]), `"type":"permission_needed"`) {
			t.Errorf("v3 pending_snapshot items = %s, want the one permission_needed envelope", payload.Items)
		}
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

	body := coldConnectAs(t, h, true).Body.String()
	if !strings.Contains(body, `"type":"run_input_needed"`) {
		t.Fatalf("a parked step's question was not replayed, so the run stays parked with "+
			"nothing on screen to answer it: %q", body)
	}
	if !strings.Contains(body, "which branch?") {
		t.Errorf("the replayed ask carried no question, and no endpoint has one: %q", body)
	}
	v3 := coldConnectAs(t, h, false).Body.String()
	if !strings.Contains(v3, "which branch?") {
		t.Errorf("the v3 pending_snapshot does not carry the question: %q", v3)
	}

	// After the answer there is nothing to replay: the claim deleted the entry, so
	// a second connection must not re-offer a card whose request is settled.
	if _, ok := h.runs.asks.TakeIfPresent("wf_1", "a1"); !ok {
		t.Fatal("Setup: the ask could not be claimed")
	}
	if after := coldConnectAs(t, h, true).Body.String(); strings.Contains(after, `"type":"run_input_needed"`) {
		t.Errorf("an answered ask was replayed to a later connection: %q", after)
	}
	if after := coldConnectAs(t, h, false).Body.String(); strings.Contains(after, "which branch?") {
		t.Errorf("an answered ask rode a later v3 pending_snapshot: %q", after)
	}
}

// A reconnect re-reads the notification toggles because the config may have been
// edited while SSE was down. A FRESH connection does not — the process just read them
// — and re-reading on every one costs a disk read plus a singleflight round per page
// load.
func TestHandleSSE_ReloadsPushPreferencesOnlyForAReconnect(t *testing.T) {
	cases := []struct {
		name        string
		lastEventID bool
		wantReloads int32
	}{
		{name: "a reconnect re-reads them", lastEventID: true, wantReloads: 1},
		{name: "a fresh connection does not", lastEventID: false, wantReloads: 0},
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
			if tc.lastEventID {
				req.Header.Set("Last-Event-ID", cursorAt(h, 2))
			}
			h.handleSSE(httptest.NewRecorder(), req)

			if got := fp.reloads.Load(); got != tc.wantReloads {
				t.Errorf("ReloadPreferences called %d times with a Last-Event-ID %v, want %d",
					got, tc.lastEventID, tc.wantReloads)
			}
		})
	}
}

// openSmallTurn opens a prompt turn on id and gives its buffer a started message
// with text, the fixture the transcript GET's live_turn is read from.
func openSmallTurn(tb testing.TB, rt *Runtime, id vibekit.ChatID, text string) {
	tb.Helper()
	rt.bridge.mgr.orInsert(id)
	if epoch := rt.coord.StartTurn(tb.Context(), id, vibekit.TurnSourcePrompt); epoch == 0 {
		tb.Fatalf("StartTurn(%q) refused, so the chat is not busy", id)
	}
	buf := rt.liveTurnBuffer(id)
	if buf == nil {
		tb.Fatalf("no live turn buffer for %q, so there is nothing to fill", id)
	}
	if opened, _ := buf.StartTurn("m-" + string(id)); !opened {
		tb.Fatalf("turn for %q was already started, so the fixture is not the one filling it", id)
	}
	buf.AppendTextDelta(text, "")
}
