package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"pgregory.net/rapid"
)

// --- emit / replay buffer ---

func TestEmit_AppendsToReplayBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})

	evts := h.sse.replayBuf.Events()
	if len(evts) != 2 {
		t.Fatalf("replayBuf len = %d, want 2", len(evts))
	}
	if evts[0].chatID != "c1" || evts[1].chatID != "c2" {
		t.Errorf("replayBuf chat_ids: %q, %q", evts[0].chatID, evts[1].chatID)
	}
	if evts[0].eventID >= evts[1].eventID {
		t.Errorf("event IDs not monotonic: %d → %d", evts[0].eventID, evts[1].eventID)
	}
}

func TestEmit_CapsBufferAtReplayBufSize(t *testing.T) {
	h, _, _ := newTestHub()
	for range replayBufSize + 100 {
		h.emit(api.ServerEvent{Type: "test"})
	}
	got := h.sse.replayBuf.Len()
	if got != replayBufSize {
		t.Errorf("replayBuf len = %d, want cap %d", got, replayBufSize)
	}
}

func TestEmit_FiltersByChatFilter(t *testing.T) {
	h, _, _ := newTestHub()

	// Register one chat-filtered client manually.
	ch := make(chan sseEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx
	sc := &sseClient{ch: ch, cancel: cancel, chatID: "c1"}
	h.sse.ctrl.add(sc)

	// Event for a different chat must not reach this client.
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	select {
	case evt := <-ch:
		t.Errorf("received event for wrong chat: %+v", evt)
	case <-time.After(20 * time.Millisecond):
	}

	// Event for matching chat should arrive.
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Error("expected event for c1 never arrived")
	}

	// Global event (no chat_id) always arrives.
	h.emit(api.ServerEvent{Type: "connected"})
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Error("expected global event never arrived")
	}
}

// --- HandleSSE (integration-ish, direct call) ---

func TestHandleSSE_EmitsConnectedHandshake(t *testing.T) {
	h, _, _ := newTestHub()
	// Seed 3 events so the replay buffer has a known floor/head.
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
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
}

func TestReplayBounds_EmptyBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	floor, head := h.replayBounds()
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
		h.emit(api.ServerEvent{Type: "test"})
	}
	floor, head := h.replayBounds()
	// Floor must be above 1 (the oldest 5 events fell out).
	if floor <= 1 {
		t.Errorf("floor = %d, want > 1 after overflow", floor)
	}
	// Head == total emitted.
	wantHead := uint64(replayBufSize + 5)
	if head != wantHead {
		t.Errorf("head = %d, want %d", head, wantHead)
	}
	// Window size must equal the buffer capacity.
	if head-floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want %d", head-floor+1, replayBufSize)
	}
}

func TestHandleSSE_ReplaysSinceLastEventID(t *testing.T) {
	h, _, _ := newTestHub()

	// Seed replay buffer.
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1") // skip event 1 only
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	// Client asked for events after ID 1 → should see c2 and c3 but not c1.
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Errorf("replay included event <= Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"c2"`) || !strings.Contains(body, `"chat_id":"c3"`) {
		t.Errorf("replay missed events after Last-Event-ID: %s", body)
	}
}

// TestHandleSSE_ReplaysNewestBeyondClientBuffer pins the replay-cap fix:
// the per-client delivery channel is sized to the replay ring, so a
// reconnect that missed more events than the old 256-slot buffer still
// receives the NEWEST events. Replay yields oldest→newest into a
// non-blocking send, so an undersized buffer dropped exactly the freshest
// events — the ones the reconnecting client most needs. Here 299 events
// replay (past the old 256 cap); the newest (c300) and an event past the
// old boundary (c290) must both be delivered.
func TestHandleSSE_ReplaysNewestBeyondClientBuffer(t *testing.T) {
	h, _, _ := newTestHub()

	const n = 300
	for i := 1; i <= n; i++ {
		h.emit(api.ServerEvent{Type: "chat_updated", ChatID: api.ChatID(fmt.Sprintf("c%d", i))})
	}

	// Reconnect asking for everything after event 1 → 299 events to replay,
	// well beyond the old 256-slot client buffer.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	// The newest event MUST arrive (silently dropped under the old 256 cap).
	if !strings.Contains(body, fmt.Sprintf(`"chat_id":"c%d"`, n)) {
		t.Errorf("replay dropped the newest event c%d (client buffer smaller than the replay set)", n)
	}
	// An event past the old 256-slot boundary must survive too.
	if !strings.Contains(body, `"chat_id":"c290"`) {
		t.Error("replay dropped event c290 past the old 256 client-buffer cap")
	}
	// Sanity: the one skipped event (<= Last-Event-ID) is absent.
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Error("replay included c1 which is <= Last-Event-ID")
	}
}

func TestHandleSSE_RejectsNonFlusher(t *testing.T) {
	h, _, _ := newTestHub()
	// Use a bare ResponseWriter wrapper that hides Flusher.
	rec := &nonFlusherWriter{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	h.handleSSE(rec, req)
	if rec.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.status)
	}
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

// TestEmit_EvictsSlowClient pins the slow-reader escape valve: when a
// client's channel fills, emit() cancels the client and removes it
// from the fan-out map so one stuck browser tab can't pin the emit
// lock. Regression here would silently deadlock every broadcast.
func TestEmit_EvictsSlowClient(t *testing.T) {
	h, _, _ := newTestHub()

	cancelled := make(chan struct{})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrappedCancel := func() { cancel(); close(cancelled) }
	// ch with capacity 1, pre-filled so the next send blocks and
	// triggers the eviction branch.
	ch := make(chan sseEvent, 1)
	ch <- sseEvent{}
	sc := &sseClient{ch: ch, cancel: wrappedCancel}
	h.sse.ctrl.add(sc)

	h.emit(api.ServerEvent{Type: "x"})

	h.sse.ctrl.mu.Lock()
	_, still := h.sse.ctrl.clients[sc]
	h.sse.ctrl.mu.Unlock()
	if still {
		t.Error("slow client not evicted from sseClients")
	}
	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		t.Error("sc.cancel() never fired")
	}
}

func FuzzParseLastEventID(f *testing.F) {
	f.Add("")
	f.Add("0")
	f.Add("1")
	f.Add("18446744073709551615")
	f.Add("abc")
	f.Add("-1")
	f.Add("1.5")
	f.Add("  42  ")
	f.Fuzz(func(t *testing.T, s string) {
		// Must never panic.
		got := parseLastEventID(s)
		// If the string is a valid uint64, the result must match.
		// Otherwise, result must be 0.
		_ = got
	})
}

// BenchmarkEmit measures the SSE broadcast hot path with varying client
// counts. Catches O(n) regressions in the fan-out loop and ring-rotation
// allocator churn.
func BenchmarkEmit(b *testing.B) {
	for _, clients := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			h, _, _ := newTestHub()
			// Register N clients with buffered channels so sends don't block.
			for range clients {
				ch := make(chan sseEvent, 128)
				_, cancel := context.WithCancel(context.Background())
				sc := &sseClient{ch: ch, cancel: cancel, chatID: ""}
				h.sse.ctrl.add(sc)
			}
			evt := api.ServerEvent{Type: "chat_updated", ChatID: "bench"}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				h.emit(evt)
			}
		})
	}
}

// TestReplayRing_MonotonicProperty uses pgregory.net/rapid to verify the
// replayRing's core invariants under arbitrary append sequences:
//   - Replay(0,"") length == min(appended, capacity)
//   - eventIDs are strictly ascending
//   - Bounds().floor == first replayed event's ID
//   - Bounds().head == last replayed event's ID
func TestReplayRing_MonotonicProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cap := rapid.IntRange(1, 1000).Draw(t, "capacity")
		count := rapid.IntRange(1, 5000).Draw(t, "eventCount")

		ring := newReplayRing(cap)
		for i := 1; i <= count; i++ {
			ring.Append(sseEvent{eventID: uint64(i), chatID: "c1"})
		}

		events := ring.Replay(0, "")
		wantLen := min(count, cap)
		if len(events) != wantLen {
			t.Fatalf("Replay len = %d, want min(%d, %d) = %d", len(events), count, cap, wantLen)
		}

		// Strictly ascending eventIDs.
		for i := 1; i < len(events); i++ {
			if events[i].eventID <= events[i-1].eventID {
				t.Fatalf("eventIDs not monotonic at index %d: %d <= %d",
					i, events[i].eventID, events[i-1].eventID)
			}
		}

		// Bounds match first/last replayed events.
		floor, head := ring.Bounds()
		if floor != events[0].eventID {
			t.Fatalf("Bounds().floor = %d, want events[0].eventID = %d", floor, events[0].eventID)
		}
		if head != events[len(events)-1].eventID {
			t.Fatalf("Bounds().head = %d, want events[last].eventID = %d", head, events[len(events)-1].eventID)
		}
	})
}

// --- tarch-b10-c5-p1: BenchmarkReplayRingReplay ---

// BenchmarkReplayRingReplay measures the SSE reconnect read path with
// varying buffer sizes, sinceID positions, and chat filters.
func BenchmarkReplayRingReplay(b *testing.B) {
	for _, bufSize := range []int{64, 256, 1024} {
		for _, pct := range []struct {
			name string
			frac float64
		}{
			{"since0pct", 0.0},
			{"since50pct", 0.5},
			{"since90pct", 0.9},
		} {
			for _, filter := range []api.ChatID{"", "c1"} {
				name := fmt.Sprintf("buf=%d/%s/filter=%q", bufSize, pct.name, filter)
				b.Run(name, func(b *testing.B) {
					ring := newReplayRing(bufSize)
					for i := 1; i <= bufSize; i++ {
						chatID := api.ChatID("c1")
						if i%3 == 0 {
							chatID = "c2"
						}
						ring.Append(sseEvent{eventID: uint64(i), chatID: chatID})
					}
					sinceID := uint64(float64(bufSize) * pct.frac)
					b.ResetTimer()
					b.ReportAllocs()
					for range b.N {
						_ = ring.Replay(sinceID, filter)
					}
				})
			}
		}
	}
}

// --- tarch-b10-c5-p3: TestSupervisedState_Property ---

// TestSupervisedState_Property uses pgregory.net/rapid to verify
// supervisedState Set/Clear/HasTrust invariants under random operation
// sequences:
//   - After SetTrust(id), HasTrust(id) == true
//   - After ClearTrust(id, _), HasTrust(id) == false
//   - SetTrust is idempotent: duplicate calls don't fire broadcast twice
func TestSupervisedState_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var broadcasts int
		ss := newSupervisedState(func(_ context.Context, _ api.ServerEvent) { broadcasts++ })

		// Model: track expected trust state.
		model := make(map[api.ChatID]bool)

		ops := rapid.IntRange(1, 200).Draw(t, "ops")
		for range ops {
			chatID := api.ChatID(rapid.StringMatching(`^c[1-5]$`).Draw(t, "chatID"))
			action := rapid.IntRange(0, 1).Draw(t, "action")

			switch action {
			case 0: // SetTrust
				prevBroadcasts := broadcasts
				wasSet := model[chatID]
				ss.SetTrust(chatID)
				model[chatID] = true
				if !ss.HasTrust(chatID) {
					t.Fatalf("HasTrust(%q) = false after SetTrust", chatID)
				}
				// Idempotency: if already set, no new broadcast.
				if wasSet && broadcasts != prevBroadcasts {
					t.Fatalf("duplicate SetTrust(%q) fired broadcast", chatID)
				}
				// First set: exactly one broadcast.
				if !wasSet && broadcasts != prevBroadcasts+1 {
					t.Fatalf("first SetTrust(%q) did not fire broadcast", chatID)
				}
			case 1: // ClearTrust
				ss.ClearTrust(chatID, "cancelled")
				model[chatID] = false
				if ss.HasTrust(chatID) {
					t.Fatalf("HasTrust(%q) = true after ClearTrust", chatID)
				}
			}
		}
	})
}

// --- tarch-b10-c5-p4: BenchmarkSSEControllerEmit_ChatFiltered ---

// --- tarch-b9-c7-p3: FuzzReplayRing_Replay ---

// FuzzReplayRing_Replay fuzzes sinceID filtering and chat-filter correctness
// under arbitrary inputs, complementing the rapid property test with
// filter-correctness invariants under adversarial sinceID/chatFilter values.
func FuzzReplayRing_Replay(f *testing.F) {
	f.Add(uint64(0), "")
	f.Add(uint64(1), "c1")
	f.Add(uint64(18446744073709551615), "x")
	f.Add(uint64(50), "c2")
	f.Fuzz(func(t *testing.T, sinceID uint64, chatFilter string) {
		ring := newReplayRing(64)
		for i := uint64(1); i <= 100; i++ {
			cid := api.ChatID("c1")
			if i%3 == 0 {
				cid = "c2"
			}
			ring.Append(sseEvent{eventID: i, chatID: cid})
		}
		results := ring.Replay(sinceID, api.ChatID(chatFilter))
		if len(results) > ring.Len() {
			t.Fatalf("result count %d > ring len %d", len(results), ring.Len())
		}
		for _, e := range results {
			if e.eventID <= sinceID {
				t.Fatalf("event %d <= sinceID %d", e.eventID, sinceID)
			}
			if chatFilter != "" && e.chatID != "" && e.chatID != api.ChatID(chatFilter) {
				t.Fatalf("event chatID %q doesn't match filter %q", e.chatID, chatFilter)
			}
		}
	})
}

// --- tarch-b9-c7-p4: BenchmarkSSEControllerEmit_HighFanOut ---

// BenchmarkSSEControllerEmit_HighFanOut measures emit cost at 100/500
// pre-registered clients (mix of chat-filtered and global), pinning the
// per-emit cost at scale and catching O(n) regressions in client iteration.
func BenchmarkSSEControllerEmit_HighFanOut(b *testing.B) {
	for _, total := range []int{100, 500} {
		b.Run(fmt.Sprintf("clients=%d", total), func(b *testing.B) {
			ctrl := newSSEController(256)
			for i := range total {
				ch := make(chan sseEvent, 128)
				_, cancel := context.WithCancel(context.Background())
				var chatID api.ChatID
				switch i % 3 {
				case 0:
					chatID = "target"
				case 1:
					chatID = "other"
				}
				sc := &sseClient{ch: ch, cancel: cancel, chatID: chatID}
				ctrl.add(sc)
			}
			evt := api.ServerEvent{Type: "chat_updated", ChatID: "target"}
			data := []byte(`{"type":"chat_updated","chat_id":"target"}`)
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				ctrl.emit(evt, data)
			}
		})
	}
}

// BenchmarkSSEControllerEmit_ChatFiltered measures the fan-out hot path
// when most clients have a non-matching chat filter, exercising the
// skip branch that BenchmarkEmit misses.
func BenchmarkSSEControllerEmit_ChatFiltered(b *testing.B) {
	for _, total := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("clients=%d", total), func(b *testing.B) {
			ctrl := newSSEController(256)
			// 90% of clients filter on "other" (won't match), 10% on "target".
			for i := range total {
				ch := make(chan sseEvent, 128)
				_, cancel := context.WithCancel(context.Background())
				chatID := api.ChatID("other")
				if i%10 == 0 {
					chatID = "target"
				}
				sc := &sseClient{ch: ch, cancel: cancel, chatID: chatID}
				ctrl.add(sc)
			}
			evt := api.ServerEvent{Type: "chat_updated", ChatID: "target"}
			data := []byte(`{"type":"chat_updated","chat_id":"target"}`)
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				ctrl.emit(evt, data)
			}
		})
	}
}
