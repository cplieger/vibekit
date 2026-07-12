package push

// Tests for send.go: truncate, the Send preflight/debounce/preference
// gates, status-driven pruning, and the per-subscriber push() path
// (size guard, RFC 8291 body assembly, ctx merge, result logging).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		want string
		n    int
	}{
		{in: "short", n: 10, want: "short"},
		{in: "exactly10!", n: 10, want: "exactly10!"},
		{in: "this is longer than ten", n: 10, want: "this is lo..."},
		{in: "", n: 5, want: ""},
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestSend_PreferenceFiltering(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close()
	// Subscribe so Send actually reaches the preflight stage;
	// without subs the early-exit wouldn't prove the gate ran.
	s.Subscribe(api.PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/pref-test",
	})

	// With agentFinished disabled, Send for agent_finished must
	// NOT record a last-push timestamp — the preflight gate
	// short-circuits before the stamp.
	s.SetPreferences(map[api.PushKind]bool{
		api.PushKindAgentFinished: false,
		api.PushKindPermission:    true,
	})
	s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)
	s.mu.Lock()
	_, afRecorded := s.lastPush[api.PushKindAgentFinished]
	s.mu.Unlock()
	if afRecorded {
		t.Error("agentFinished=false should prevent Send from recording last-push timestamp")
	}

	// Mirror for permission.
	s.SetPreferences(map[api.PushKind]bool{
		api.PushKindAgentFinished: true,
		api.PushKindPermission:    false,
	})
	s.Send(context.Background(), "title", "body", api.PushKindPermission)
	s.mu.Lock()
	_, pnRecorded := s.lastPush[api.PushKindPermission]
	s.mu.Unlock()
	if pnRecorded {
		t.Error("permissionNeeded=false should prevent Send from recording last-push timestamp")
	}
}

func TestSend_Debounce(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close()

	// Set lastPush[agent_finished] to now to trigger debounce.
	s.mu.Lock()
	s.lastPush[api.PushKindAgentFinished] = time.Now()
	s.mu.Unlock()

	// Immediate second send should be debounced.
	// Subscribe a dummy endpoint so Send doesn't exit early on empty subs.
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/debounce-test"})

	// Record lastPush before Send.
	s.mu.Lock()
	before := s.lastPush[api.PushKindAgentFinished]
	s.mu.Unlock()

	s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)

	// lastPush should not have been updated (debounced).
	s.mu.Lock()
	after := s.lastPush[api.PushKindAgentFinished]
	s.mu.Unlock()

	if !after.Equal(before) {
		t.Error("lastPush should not change when debounced")
	}
}

// TestSend_DebouncePerType pins the per-type debounce: a recent
// agent_finished push must NOT suppress a permission push (or vice
// versa) — debounce is keyed on type so the two windows are
// independent.
func TestSend_DebouncePerType(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close()

	// Mark agent_finished as just-sent.
	s.mu.Lock()
	s.lastPush[api.PushKindAgentFinished] = time.Now()
	s.mu.Unlock()

	// permission's window is empty; a permission Send must update
	// its own last-push timestamp (not blocked by the agent_finished
	// window).
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/x"})
	s.Send(context.Background(), "title", "body", api.PushKindPermission)

	s.mu.Lock()
	permTimestamp := s.lastPush[api.PushKindPermission]
	s.mu.Unlock()
	if permTimestamp.IsZero() {
		t.Error("permission push was suppressed by agent_finished debounce window")
	}
}

// TestSend_UnknownKindRejected pins that an unknown kind is refused
// with no persisted debounce side-effect.
func TestSend_UnknownKindRejected(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close()
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/x"})
	s.Send(context.Background(), "title", "body", "what-is-this")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lastPush["what-is-this"]; ok {
		t.Error("unknown kind should not record a debounce entry")
	}
}

func TestSend_UnhealthySkips(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	s.mu.Lock()
	s.healthy = false
	s.mu.Unlock()

	// Should return immediately without panicking.
	s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)
}

func TestSend_StatusCodePruning(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantPruned bool
	}{
		{"PrunesOn410Gone", http.StatusGone, true},
		{"PrunesOn404NotFound", http.StatusNotFound, true},
		{"KeepsOn201Created", http.StatusCreated, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			dir := t.TempDir()
			s := New(context.Background(), dir, "mailto:test@example.com")
			defer s.Close() // wait for writeLoop to drain before TempDir cleanup
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)

			if tt.wantPruned && s.HasSubscribers() {
				t.Errorf("Send did not prune subscription after %d", tt.status)
			}
			if !tt.wantPruned && !s.HasSubscribers() {
				t.Errorf("Send pruned subscription on %d", tt.status)
			}
		})
	}
}

func TestSend_TruncatesOversizePayload(t *testing.T) {
	// Capture the payload the push endpoint receives.
	var receivedPayload []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The payload is encrypted, so we can't inspect it directly.
		// Instead, verify the Send path doesn't error out.
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close() // wait for writeLoop to drain before TempDir cleanup
	s.client = srv.Client()
	s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

	// Build a body that exceeds pushBodyCap (3000 bytes).
	title := "Vibekit"
	body := strings.Repeat("x", 4000)

	// Send should not panic or error — it truncates internally.
	s.Send(context.Background(), title, body, api.PushKindAgentFinished)

	// Verify the subscriber wasn't pruned (201 = success).
	if !s.HasSubscribers() {
		t.Error("subscriber was pruned after successful oversize send")
	}

	// Verify the real invariant: after truncation the *marshaled* payload
	// (JSON envelope + escaping included) fits within pushBodyCap, so push()
	// delivers it instead of rejecting an oversize record. Sizing on the raw
	// title+body length — as this code once did — left the ~22-byte envelope
	// over the cap and the notification was silently dropped.
	gotTitle, gotBody, truncated := fitToCap(title, body)
	if !truncated {
		t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
	}
	if n := marshaledLen(gotTitle, gotBody); n > pushBodyCap {
		t.Errorf("marshaled payload = %d bytes, exceeds cap %d", n, pushBodyCap)
	}
	if !strings.HasSuffix(gotBody, "...") {
		t.Errorf("truncated body should end with '...', got suffix %q",
			gotBody[max(len(gotBody)-10, 0):])
	}

	_ = receivedPayload // used for documentation; encrypted payload can't be inspected
}

// TestSend_OversizeTruncationWarn verifies the truncation breadcrumb:
// Send logs "push: payload too large, truncating" with the byte total
// exactly when len(title)+len(body) exceeds pushBodyCap, and stays
// quiet at or below the cap.
func TestSend_OversizeTruncationWarn(t *testing.T) {
	const warnMsg = "push: payload too large, truncating"

	t.Run("small_does_not_warn", func(t *testing.T) {
		// total=4 bytes is well under the cap.
		s := New(context.Background(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := installLogCapture(t)
		s.Send(context.Background(), "aa", "bb", api.PushKindAgentFinished)
		if capLog.has(warnMsg) {
			t.Errorf("Send warned %q for a 4-byte payload; want no warn", warnMsg)
		}
	})

	t.Run("oversize_warns_with_total_bytes", func(t *testing.T) {
		// title=10, body=4000, total=4010 — over the cap.
		s := New(context.Background(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := installLogCapture(t)
		s.Send(context.Background(), strings.Repeat("a", 10), strings.Repeat("b", 4000),
			api.PushKindAgentFinished)
		rec, ok := capLog.find(warnMsg)
		if !ok {
			t.Fatalf("Send did not warn %q for a 4010-byte payload", warnMsg)
		}
		if got, ok := asInt64(rec.attrs["bytes"]); !ok || got != 4010 {
			t.Errorf("truncation warn bytes = %v, want 4010", rec.attrs["bytes"])
		}
	})

	t.Run("marshaled_at_cap_does_not_warn", func(t *testing.T) {
		// The marshaled payload size is what matters: the ~22-byte JSON
		// envelope counts toward the cap. title=978 + body=2000 marshals to
		// exactly pushBodyCap (3000), which is not over, so Send must not warn.
		s := New(context.Background(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := installLogCapture(t)
		s.Send(context.Background(), strings.Repeat("a", 978), strings.Repeat("b", 2000),
			api.PushKindAgentFinished)
		if capLog.has(warnMsg) {
			t.Errorf("Send warned %q at exactly the marshaled cap; want no warn", warnMsg)
		}
	})
}

// TestPush_PayloadSizeBoundary pins push's payload-size guard as
// strictly-greater-than: a payload of exactly pushBodyCap bytes passes
// the guard (and fails later decoding p256dh), while pushBodyCap+1 is
// rejected as too large before any work.
func TestPush_PayloadSizeBoundary(t *testing.T) {
	s := &Service{ctx: context.Background()}
	sub := api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/size"}
	sub.Keys.P256dh = "###not-base64###" // invalid → "decode p256dh" once past the guard
	sub.Keys.Auth = "AAAA"

	t.Run("exactly_cap_passes_size_guard", func(t *testing.T) {
		_, err := s.push(context.Background(), sub, make([]byte, pushBodyCap))
		if err == nil {
			t.Fatalf("push(payload=%d) err = nil, want a downstream error", pushBodyCap)
		}
		if strings.Contains(err.Error(), "payload too large") {
			t.Errorf("push(payload=%d) rejected as too large; %d is not > %d",
				pushBodyCap, pushBodyCap, pushBodyCap)
		}
		if !strings.Contains(err.Error(), "decode p256dh") {
			t.Errorf("push(payload=%d) err = %v, want a decode p256dh error", pushBodyCap, err)
		}
	})

	t.Run("over_cap_rejected", func(t *testing.T) {
		_, err := s.push(context.Background(), sub, make([]byte, pushBodyCap+1))
		if err == nil || !strings.Contains(err.Error(), "payload too large") {
			t.Errorf("push(payload=%d) err = %v, want payload-too-large", pushBodyCap+1, err)
		}
	})
}

// TestPush_BodyCapacityStaysPositive verifies push computes a
// non-negative make([]byte, 0, cap) capacity for the RFC 8291 body
// across payload sizes (a sign error would drive the capacity negative
// and panic). Each call reaches the forced transport error, proving it
// traversed the body assembly without panicking.
func TestPush_BodyCapacityStaysPositive(t *testing.T) {
	s := New(context.Background(), t.TempDir(), testSubject)
	defer s.Close()
	s.client = &http.Client{Transport: errRoundTripper{}}
	sub := pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/cap")

	pushExpectNoPanic(t, s, sub, make([]byte, 10), "small")  // payload < ephemeral-key length
	pushExpectNoPanic(t, s, sub, make([]byte, 100), "large") // payload > ephemeral-key length
}

func pushExpectNoPanic(t *testing.T, s *Service, sub api.PushSubscription, payload []byte, label string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("push(%s, len=%d) panicked (body capacity went negative): %v",
				label, len(payload), r)
		}
	}()
	_, err := s.push(context.Background(), sub, payload)
	if err == nil {
		t.Errorf("push(%s) err = nil, want forced transport error", label)
		return
	}
	// Reaching the forced transport error proves push() traversed the
	// body assembly, so a negative-capacity panic would be on this path.
	if !strings.Contains(err.Error(), "forced transport error") {
		t.Errorf("push(%s) err = %v, want forced transport error", label, err)
	}
}

// TestSend_ResultStatusLogging pins the per-result logging: a >=400
// status that isn't 410/404 logs "unexpected status"; a 2xx does not.
// The fan-out wait (g.Wait always returns nil) and a clean body drain
// never log.
func TestSend_ResultStatusLogging(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantUnexpec bool
	}{
		{"400_logs_unexpected", http.StatusBadRequest, true},
		{"500_logs_unexpected", http.StatusInternalServerError, true},
		{"201_no_unexpected", http.StatusCreated, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			s := New(context.Background(), t.TempDir(), testSubject)
			defer s.Close()
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			capLog := installLogCapture(t)
			s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)

			if got := capLog.has("push: unexpected status"); got != tc.wantUnexpec {
				t.Errorf("status %d: logged unexpected-status = %v, want %v",
					tc.status, got, tc.wantUnexpec)
			}
			// g.Wait always returns nil → the fan-out error is never logged.
			if capLog.has("push: fan-out wait") {
				t.Errorf("status %d: logged %q though g.Wait() returns nil",
					tc.status, "push: fan-out wait")
			}
			// A clean response body drains without error.
			if capLog.has("push: drain response body") {
				t.Errorf("status %d: logged %q though the drain succeeded",
					tc.status, "push: drain response body")
			}
		})
	}
}

// TestPush_MergesCancelledServiceCtx verifies that when the service
// context is already cancelled, push merges it into the request
// context, so a request to a blocking server is cancelled immediately
// (context.Canceled) rather than blocking until the caller's deadline.
func TestPush_MergesCancelledServiceCtx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the request ctx is cancelled
	}))
	defer srv.Close()

	s := New(context.Background(), t.TempDir(), testSubject)
	defer s.Close()
	s.client = srv.Client()
	sub := pushSubscriptionWithValidKeys(t, srv.URL)

	s.cancel() // cancel the service ctx

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callerCancel()

	_, err := s.push(callerCtx, sub, []byte(`{"title":"t","body":"b"}`))
	if err == nil {
		t.Fatalf("push with cancelled service ctx returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("push err = %v; want context.Canceled (merged cancelled service ctx)", err)
	}
}

// TestEncryptPayload_buildsRFC8291WireBody pins the aes128gcm body
// assembly: a valid subscription yields salt(16) || rs=4096(4) ||
// idlen(1) || ephemeral P-256 key(65) || ciphertext(payload +
// 1-byte 0x02 delimiter + 16-byte GCM tag). Every error gate on the success path
// (ephemeral keygen, ECDH, salt read, key/nonce derivation, AES cipher,
// GCM) must be skipped for valid input, so a gate inverted to return on
// the happy path yields a nil/short body and fails the length + header
// checks below.
func TestEncryptPayload_buildsRFC8291WireBody(t *testing.T) {
	sub := pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/wire")
	payload := []byte(`{"title":"t","body":"hello"}`)

	body, err := encryptPayload(sub, payload)
	if err != nil {
		t.Fatalf("encryptPayload err = %v, want nil for a valid subscription", err)
	}

	const ephLen = 65 // P-256 uncompressed point: 0x04 || X(32) || Y(32)
	const gcmTag = 16
	// salt(16) + rs(4) + idlen(1) + ephemeral key + (payload + 1-byte 0x02 delimiter + tag)
	wantLen := 16 + 4 + 1 + ephLen + (len(payload) + 1 + gcmTag)
	if len(body) != wantLen {
		t.Fatalf("len(body) = %d, want %d (salt+rs+idlen+ephKey+ciphertext)", len(body), wantLen)
	}
	// rs (record size) field is bytes 16..20, big-endian 4096 = 0x00001000.
	if body[16] != 0x00 || body[17] != 0x00 || body[18] != 0x10 || body[19] != 0x00 {
		t.Errorf("record-size header = % x, want 00 00 10 00 (4096)", body[16:20])
	}
	// idlen byte (offset 20) is the ephemeral public-key length.
	if body[20] != ephLen {
		t.Errorf("ephemeral key length byte = %d, want %d", body[20], ephLen)
	}
}
