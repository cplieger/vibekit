package push

// Tests for send.go: the endpoint log-attr bounding, the Send
// preflight/debounce/preference gates, status-driven pruning, and the
// per-subscriber push() path (size guard, RFC 8291 body assembly, ctx
// merge, result logging).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestSendFailureLogBoundsEndpoint pins the endpoint log-attr contract at
// the send-failed site: the client-supplied subscription URL is untrusted,
// so the logged attribute rides runesafe.SanitizeSingleLineBounded — a long
// endpoint is capped at 60 bytes plus the "..." marker, and hostile control
// runes never reach the log stream raw.
//
// Runs in a synctest bubble, at the PRODUCTION retry ladder. The invalid URL
// fails in http.NewRequestWithContext before any dial, so deliver() treats it as
// transient and walks the full pushMaxAttempts ladder — one uniform pick over
// [0, 1s) then one over [0, 2s) — which cost 2.03 s of real time (measured on
// go1.27.0). Nothing here is waiting on an async effect, so this is a
// class-(b) sleep: exactly what the bubble's synthetic clock deletes. The
// alternative, collapsing pushRetryBase/pushRetryBudget the way the ladder tests
// do, would have this test assert against a fixture rather than the shipped
// budget.
func TestSendFailureLogBoundsEndpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := capture.Default(t)
		dir := t.TempDir()
		s := New(t.Context(), dir, "mailto:test@example.com")
		defer s.Close() // wait for writeLoop to drain before TempDir cleanup

		// The control bytes make the endpoint an invalid request URL, so the
		// send fails deterministically without any network I/O and logs the
		// bounded endpoint attribute on the send-failed warn line. No live
		// socket, which is what keeps the bubble's clock able to advance.
		hostile := "https://evil.example/\x1b]0;pwned\x07/" + strings.Repeat("x", 100)
		s.Subscribe(pushSubscriptionWithValidKeys(t, hostile))

		s.Send(t.Context(), "t", "b", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

		got, ok := rec.AttrValue("push: send failed", "endpoint")
		if !ok {
			t.Fatalf("no endpoint attr on the send-failed warn; logs = %q", rec.Messages())
		}
		if len(got) > 60+len("...") {
			t.Errorf("endpoint attr = %d bytes, want <= 63 (cap + marker)", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("endpoint attr %q does not end in the truncation marker", got)
		}
		if strings.ContainsAny(got, "\x1b\x07") {
			t.Errorf("endpoint attr %q carries raw control bytes; sanitization missing", got)
		}
		// The whole ladder ran in synthetic time, so the attempt count is now an
		// equality against the shipped cap rather than something the test had to
		// shorten to afford.
		if n := rec.CountExact("push: send failed"); n != pushMaxAttempts {
			t.Errorf("send-failed warns = %d, want pushMaxAttempts (%d)", n, pushMaxAttempts)
		}
	})
}

func TestSend_PreferenceFiltering(t *testing.T) {
	// The permission leg below reaches the fan-out, so the client is the
	// in-memory one and the subscription carries REAL keys. Neither was true
	// before: encryptPayload failed on the keyless subscription and the fan-out
	// then slept the whole production retry ladder (measured 1.11 s) while
	// asserting nothing about the delivery. See newServiceOnTestServer.
	rec := &recordingHandler{}
	s, _ := newServiceOnTestServer(t, rec)
	// Subscribe so Send actually reaches the preflight stage;
	// without subs the early-exit wouldn't prove the gate ran.
	s.Subscribe(pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/pref-test"))

	// With agentFinished disabled, Send for agent_finished must
	// NOT record a last-push timestamp — the preflight gate
	// short-circuits before the stamp.
	s.SetPreferences(map[vibekit.PushKind]bool{
		vibekit.PushKindAgentFinished: false,
		vibekit.PushKindPermission:    true,
	})
	s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})
	s.mu.Lock()
	_, afRecorded := s.lastPush[debounceKey(vibekit.PushKindAgentFinished, vibekit.PushSubject{})]
	s.mu.Unlock()
	if afRecorded {
		t.Error("agentFinished=false should prevent Send from recording last-push timestamp")
	}

	// The mirror for permission is deliberately the OTHER direction. There is
	// no notify_permission setting any more, so no reachable configuration
	// hands SetPreferences a false for this kind (see floor_test.go); asserting
	// that the gate can silence it would pin a state the app cannot enter. What
	// matters is that the ask gets through with the other kind switched off.
	s.SetPreferences(map[vibekit.PushKind]bool{
		vibekit.PushKindAgentFinished: false,
		vibekit.PushKindPermission:    true,
	})
	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.PushSubject{})
	s.mu.Lock()
	_, pnRecorded := s.lastPush[debounceKey(vibekit.PushKindPermission, vibekit.PushSubject{})]
	s.mu.Unlock()
	if !pnRecorded {
		t.Error("permission push must reach the send path even with agent_finished off")
	}

	// The fan-out is now assertable, which is what the in-memory client buys
	// over a RoundTripper stub: exactly one delivery, addressed to the
	// SUBSCRIPTION's own host and path rather than rebased onto a listener, and
	// carrying the RFC 8291 content coding plus the RFC 8292 VAPID header. The
	// agent_finished leg above must contribute nothing.
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1 (only the permission send passes the gate)", len(got))
	}
	if got[0].host != "fcm.googleapis.com" || got[0].path != "/fcm/send/pref-test" {
		t.Errorf("delivered to %s%s, want fcm.googleapis.com/fcm/send/pref-test",
			got[0].host, got[0].path)
	}
	if got[0].contentEncoding != "aes128gcm" {
		t.Errorf("Content-Encoding = %q, want aes128gcm", got[0].contentEncoding)
	}
	if !strings.HasPrefix(got[0].authorization, "vapid t=") {
		t.Errorf("Authorization = %q, want a vapid t=<jwt>, k=<key> header", got[0].authorization)
	}
}

func TestSend_Debounce(t *testing.T) {
	// In-memory client: this test's subscription would otherwise be delivered
	// over the real network if the debounce gate ever stopped refusing.
	s, _ := newServiceOnTestServer(t, &recordingHandler{})

	// Set the agent_finished/global window to now to trigger debounce.
	s.mu.Lock()
	s.lastPush[debounceKey(vibekit.PushKindAgentFinished, vibekit.PushSubject{})] = time.Now()
	s.mu.Unlock()

	// Immediate second send should be debounced.
	// Subscribe a dummy endpoint so Send doesn't exit early on empty subs.
	s.Subscribe(vibekit.PushSubscription{Endpoint: "https://push.example.com/debounce-test"})

	// Record lastPush before Send.
	s.mu.Lock()
	before := s.lastPush[debounceKey(vibekit.PushKindAgentFinished, vibekit.PushSubject{})]
	s.mu.Unlock()

	s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

	// lastPush should not have been updated (debounced).
	s.mu.Lock()
	after := s.lastPush[debounceKey(vibekit.PushKindAgentFinished, vibekit.PushSubject{})]
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
	// The permission leg reaches the fan-out, so the client is the in-memory one
	// and the subscription carries real keys: the keyless one this replaced made
	// encryptPayload fail and the fan-out slept the production ladder (measured
	// 0.49 s) with no delivery to assert.
	rec := &recordingHandler{}
	s, _ := newServiceOnTestServer(t, rec)

	// Mark agent_finished as just-sent.
	s.mu.Lock()
	s.lastPush[debounceKey(vibekit.PushKindAgentFinished, vibekit.PushSubject{})] = time.Now()
	s.mu.Unlock()

	// permission's window is empty; a permission Send must update
	// its own last-push timestamp (not blocked by the agent_finished
	// window).
	s.Subscribe(pushSubscriptionWithValidKeys(t, "https://push.example.com/x"))
	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.PushSubject{})

	s.mu.Lock()
	permTimestamp := s.lastPush[debounceKey(vibekit.PushKindPermission, vibekit.PushSubject{})]
	s.mu.Unlock()
	if permTimestamp.IsZero() {
		t.Error("permission push was suppressed by agent_finished debounce window")
	}
	// The suppressed agent_finished window did not cost a delivery, and the
	// permission one bought exactly one.
	if got := rec.snapshot(); len(got) != 1 {
		t.Errorf("deliveries = %d, want 1 (the permission send only)", len(got))
	}
}

// TestSend_UnknownKindRejected pins that an unknown kind is refused
// with no persisted debounce side-effect — and, because the client is the
// in-memory one, that the refusal happens before any delivery is attempted
// rather than being invisible behind a failed DNS lookup.
func TestSend_UnknownKindRejected(t *testing.T) {
	rec := &recordingHandler{}
	s, _ := newServiceOnTestServer(t, rec)
	s.Subscribe(vibekit.PushSubscription{Endpoint: "https://push.example.com/x"})
	s.Send(t.Context(), "title", "body", "what-is-this", vibekit.PushSubject{})
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("an unknown kind attempted %d deliveries, want 0", len(got))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lastPush[debounceKey("what-is-this", vibekit.PushSubject{})]; ok {
		t.Error("unknown kind should not record a debounce entry")
	}
}

func TestSend_UnhealthySkips(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	s.mu.Lock()
	s.healthy = false
	s.mu.Unlock()

	// Should return immediately without panicking.
	s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})
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
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))

			dir := t.TempDir()
			s := New(t.Context(), dir, "mailto:test@example.com")
			defer s.Close() // wait for writeLoop to drain before TempDir cleanup
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

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
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The payload is encrypted, so we can't inspect it directly.
		// Instead, verify the Send path doesn't error out.
		w.WriteHeader(http.StatusCreated)
	}))

	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close() // wait for writeLoop to drain before TempDir cleanup
	s.client = srv.Client()
	s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

	// Build a body that exceeds pushBodyCap (3000 bytes).
	title := "Vibekit"
	body := strings.Repeat("x", 4000)

	// Send should not panic or error — it truncates internally.
	s.Send(t.Context(), title, body, vibekit.PushKindAgentFinished, vibekit.PushSubject{})

	// Verify the subscriber wasn't pruned (201 = success).
	if !s.HasSubscribers() {
		t.Error("subscriber was pruned after successful oversize send")
	}

	// Verify the real invariant: after truncation the *marshaled* payload
	// (JSON envelope + escaping included) fits within pushBodyCap, so push()
	// delivers it instead of rejecting an oversize record. Sizing on the raw
	// title+body length — as this code once did — left the ~22-byte envelope
	// over the cap and the notification was silently dropped.
	gotTitle, gotBody, truncated := fitToCap(title, body, vibekit.PushSubject{})
	if !truncated {
		t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
	}
	if n := marshaledLen(gotTitle, gotBody, vibekit.PushSubject{}); n > pushBodyCap {
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
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := capture.Default(t)
		s.Send(t.Context(), "aa", "bb", vibekit.PushKindAgentFinished, vibekit.PushSubject{})
		if capLog.CountExact(warnMsg) > 0 {
			t.Errorf("Send warned %q for a 4-byte payload; want no warn", warnMsg)
		}
	})

	t.Run("oversize_warns_with_total_bytes", func(t *testing.T) {
		// title=10, body=4000, total=4010 — over the cap.
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := capture.Default(t)
		s.Send(t.Context(), strings.Repeat("a", 10), strings.Repeat("b", 4000),
			vibekit.PushKindAgentFinished, vibekit.PushSubject{})
		got, ok := capLog.AttrValue(warnMsg, "bytes")
		if !ok {
			t.Fatalf("Send did not warn %q for a 4010-byte payload", warnMsg)
		}
		if got != "4010" {
			t.Errorf("truncation warn bytes = %v, want 4010", got)
		}
	})

	t.Run("marshaled_at_cap_does_not_warn", func(t *testing.T) {
		// The marshaled payload size is what matters: the ~22-byte JSON
		// envelope counts toward the cap. title=978 + body=2000 marshals to
		// exactly pushBodyCap (3000), which is not over, so Send must not warn.
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		capLog := capture.Default(t)
		s.Send(t.Context(), strings.Repeat("a", 978), strings.Repeat("b", 2000),
			vibekit.PushKindAgentFinished, vibekit.PushSubject{})
		if capLog.CountExact(warnMsg) > 0 {
			t.Errorf("Send warned %q at exactly the marshaled cap; want no warn", warnMsg)
		}
	})
}

// TestPush_PayloadSizeBoundary pins push's payload-size guard as
// strictly-greater-than: a payload of exactly pushBodyCap bytes passes
// the guard (and fails later decoding p256dh), while pushBodyCap+1 is
// rejected as too large before any work.
func TestPush_PayloadSizeBoundary(t *testing.T) {
	s := &Service{lifetime: t.Context()}
	sub := vibekit.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/size"}
	sub.Keys.P256dh = "###not-base64###" // invalid → "decode p256dh" once past the guard
	sub.Keys.Auth = "AAAA"

	t.Run("exactly_cap_passes_size_guard", func(t *testing.T) {
		_, _, err := s.push(t.Context(), sub, make([]byte, pushBodyCap))
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
		_, _, err := s.push(t.Context(), sub, make([]byte, pushBodyCap+1))
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
	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()
	s.client = &http.Client{Transport: errRoundTripper{}}
	sub := pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/cap")

	pushExpectNoPanic(t, s, sub, make([]byte, 10), "small")  // payload < ephemeral-key length
	pushExpectNoPanic(t, s, sub, make([]byte, 100), "large") // payload > ephemeral-key length
}

func pushExpectNoPanic(t *testing.T, s *Service, sub vibekit.PushSubscription, payload []byte, label string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("push(%s, len=%d) panicked (body capacity went negative): %v",
				label, len(payload), r)
		}
	}()
	_, _, err := s.push(t.Context(), sub, payload)
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

// TestSend_ResultStatusLogging pins the DISPOSITION each push-service status
// maps onto, which is what decides whether a notification is retried, dropped
// or the subscription forgotten.
//
// The vocabulary is deliberately three messages rather than one generic
// "unexpected status", because the three outcomes need different reactions: a
// permanent failure is vibekit's bug to fix (error level, with a hint naming
// whose bug it is), a retryable one is the push service's weather (warn, then
// try again), and an invalidated subscription is routine (info, prune).
//
// The retry ladder is collapsed to microseconds here so the 5xx case exercises
// the give-up path without sleeping through a real backoff.
func TestSend_ResultStatusLogging(t *testing.T) {
	restoreBase, restoreBudget := pushRetryBase, pushRetryBudget
	pushRetryBase, pushRetryBudget = time.Microsecond, time.Second
	t.Cleanup(func() { pushRetryBase, pushRetryBudget = restoreBase, restoreBudget })

	cases := []struct {
		name   string
		status int
		want   string // the log message this status must produce
		absent string // and one it must not
	}{
		// 400 and 401 are the same disposition by different doors: a malformed
		// request and a VAPID mismatch are both ours, and neither is helped by
		// trying again.
		{"400_permanent", http.StatusBadRequest, "push: permanent delivery failure", "push: retryable status"},
		{"401_permanent", http.StatusUnauthorized, "push: permanent delivery failure", "push: retryable status"},
		{"413_permanent", http.StatusRequestEntityTooLarge, "push: permanent delivery failure", "push: retryable status"},
		// 429 is the row the old code got wrong: a rate limit was logged and
		// abandoned exactly like a permanent refusal.
		{"429_retryable", http.StatusTooManyRequests, "push: retryable status", "push: permanent delivery failure"},
		{"500_retryable", http.StatusInternalServerError, "push: retryable status", "push: permanent delivery failure"},
		{"503_retryable", http.StatusServiceUnavailable, "push: retryable status", "push: permanent delivery failure"},
		{"410_prunes", http.StatusGone, "push: subscription invalidated", "push: permanent delivery failure"},
		{"404_prunes", http.StatusNotFound, "push: subscription invalidated", "push: permanent delivery failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))

			s := New(t.Context(), t.TempDir(), testSubject)
			defer s.Close()
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			capLog := capture.Default(t)
			s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

			if capLog.CountExact(tc.want) == 0 {
				t.Errorf("status %d: did not log %q", tc.status, tc.want)
			}
			if capLog.CountExact(tc.absent) > 0 {
				t.Errorf("status %d: logged %q, which is the wrong disposition",
					tc.status, tc.absent)
			}
			// g.Wait always returns nil, so the fan-out error is never logged.
			if capLog.CountExact("push: fan-out wait") > 0 {
				t.Errorf("status %d: logged %q though g.Wait() returns nil",
					tc.status, "push: fan-out wait")
			}
			// A clean response body drains without error.
			if capLog.CountExact("push: drain response body") > 0 {
				t.Errorf("status %d: logged %q though the drain succeeded",
					tc.status, "push: drain response body")
			}
		})
	}
}

// TestSend_RetriesThenSucceeds pins the point of the whole classification: a
// notification that would have been lost to one 429 is delivered.
//
// It also pins the attempt CAP, because a retry loop with no ceiling on a
// service that answers 429 forever is a goroutine leak wearing a fix's clothes.
func TestSend_RetriesThenSucceeds(t *testing.T) {
	restoreBase, restoreBudget := pushRetryBase, pushRetryBudget
	pushRetryBase, pushRetryBudget = time.Microsecond, time.Second
	t.Cleanup(func() { pushRetryBase, pushRetryBudget = restoreBase, restoreBudget })

	t.Run("429_then_201_delivers", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if attempts.Add(1) == 1 {
				w.Header().Set("Retry-After", "0") // 0 is ignored; own backoff applies
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

		if got := attempts.Load(); got != 2 {
			t.Errorf("attempts = %d, want 2 (one 429 then one success)", got)
		}
		if capLog.CountExact("push: delivered after retry") == 0 {
			t.Error("a delivery that needed a retry was not reported as one")
		}
	})

	t.Run("persistent_429_stops_at_the_cap", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

		if got := attempts.Load(); got != int32(pushMaxAttempts) {
			t.Errorf("attempts = %d, want pushMaxAttempts (%d)", got, pushMaxAttempts)
		}
		if capLog.CountExact("push: giving up, attempts exhausted") == 0 {
			t.Error("gave up without saying so")
		}
	})

	t.Run("a_retry_past_the_budget_is_not_made", func(t *testing.T) {
		// Retry-After longer than the notification is useful for: the delay is
		// honoured as a REFUSAL rather than by sleeping through it.
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusServiceUnavailable)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

		if got := attempts.Load(); got != 1 {
			t.Errorf("attempts = %d, want 1 (the retry lands past the budget)", got)
		}
		if capLog.CountExact("push: giving up, retry would land past the notification's usefulness") == 0 {
			t.Error("dropped a notification without naming the budget as the reason")
		}
	})
	t.Run("a_first_attempt_delivery_is_not_reported_as_a_retry", func(t *testing.T) {
		// The line exists so a reader can tell a delivery that needed the retry
		// loop from an ordinary one. Emitting it for every success inverts that:
		// the vendor's transient 429s become invisible in a log where every push
		// claims to have retried.
		var attempts atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.WriteHeader(http.StatusCreated)
		}))

		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.client = srv.Client()
		s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

		capLog := capture.Default(t)
		s.Send(t.Context(), "title", "body", vibekit.PushKindAgentFinished, vibekit.PushSubject{})

		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1: this case has to deliver first try", got)
		}
		if n := capLog.CountExact("push: delivered after retry"); n != 0 {
			t.Errorf("a first-attempt delivery reported %d retry line(s), want 0", n)
		}
	})
}

// TestParseRetryAfter covers both legal header forms plus the values a caller
// must treat as "no instruction": absent, unparseable, zero, and a date that
// has already passed. Each of those must yield 0 so the caller stays on its own
// backoff schedule rather than retrying immediately or waiting forever.
func TestParseRetryAfter(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)

	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Errorf("seconds form: got %v, want 12s", got)
	}
	if got := parseRetryAfter(future); got <= 0 || got > 31*time.Second {
		t.Errorf("date form: got %v, want a positive delay under ~30s", got)
	}
	for _, in := range []string{"", "soon", "0", "-5", past} {
		if got := parseRetryAfter(in); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0", in, got)
		}
	}
}

// TestPush_MergesCancelledServiceCtx verifies that when the service
// context is already cancelled, push merges it into the request
// context, so a request to a blocking server is cancelled immediately
// (context.Canceled) rather than blocking until the caller's deadline.
//
// The handler must NOT wait on r.Context().Done() alone. This request
// carries a body the handler never reads, and net/http defers the
// background read that detects a client disconnect until the body hits
// EOF (measured on go1.27.0: unread body, no cancellation; body drained,
// cancellation is immediate), so the request context stays live for as
// long as the handler runs. Server.Close then waits for the handler
// while the handler waits for Close, and the package dies on the 10-minute
// test timeout. The request only reaches the server on the race where the
// transport flushes it before it observes the already-cancelled context,
// which is what made the deadlock intermittent rather than constant.
// unblock is the test-owned exit: its cleanup registers AFTER the server's,
// so LIFO ordering closes it before Close starts waiting.
func TestPush_MergesCancelledServiceCtx(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-unblock:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(unblock) })

	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()
	s.client = srv.Client()
	sub := pushSubscriptionWithValidKeys(t, srv.URL)

	s.cancel() // cancel the service ctx

	callerCtx, callerCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer callerCancel()

	_, _, err := s.push(callerCtx, sub, []byte(`{"title":"t","body":"b"}`))
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

// TestFitToCap_ChargesTheMarkerInsideTheCap pins the tightened budget the
// runesafe Capped pair brought. The composition this replaced put the marker
// OUTSIDE the byte cap, so fitToCap had to subtract the marker width from every
// cap it asked for — spending those bytes twice and landing len(pushTruncMarker)
// bytes short of the vendor budget on every trim. With the marker charged inside
// the cap the arithmetic is the overflow alone, so a single pass lands the
// marshaled payload on pushBodyCap exactly: the notification keeps every byte the
// vendor will accept. An assertion of "at most the cap" (FuzzPayloadTruncation's
// job) cannot see that regression return, which is why this one is exact.
func TestFitToCap_ChargesTheMarkerInsideTheCap(t *testing.T) {
	title := "Vibekit"
	body := strings.Repeat("x", 4000)

	gotTitle, gotBody, truncated := fitToCap(title, body, vibekit.PushSubject{})
	if !truncated {
		t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
	}
	if n := marshaledLen(gotTitle, gotBody, vibekit.PushSubject{}); n != pushBodyCap {
		t.Errorf("marshaled payload = %d bytes, want exactly %d: the trim must spend the whole budget, marker included", n, pushBodyCap)
	}
	if !strings.HasSuffix(gotBody, pushTruncMarker) {
		t.Errorf("trimmed body should end with %q, got suffix %q",
			pushTruncMarker, gotBody[max(len(gotBody)-10, 0):])
	}
	if gotTitle != title {
		t.Errorf("title = %q, want it untouched: the body absorbed the overflow", gotTitle)
	}
}

// TestFitToCap_KeepsTheBodysCRLFAxis pins which sanitize policy each field gets,
// the property that made the local capMultiline helper exist before the library
// offered the CR/LF-keeping axis. A notification body is legitimately multi-line,
// so its newlines must survive the trim while every other control rune is
// rewritten; a title is a single-line sink, so its newlines must not survive.
// Collapsing both onto one policy is the regression this guards.
func TestFitToCap_KeepsTheBodysCRLFAxis(t *testing.T) {
	t.Run("body keeps newlines, loses other control runes", func(t *testing.T) {
		body := "a\x1bb\nc" + strings.Repeat("x", 4000)
		gotTitle, gotBody, truncated := fitToCap("Vibekit", body, vibekit.PushSubject{})
		if !truncated {
			t.Fatalf("fitToCap reported no truncation for a %d-byte body", len(body))
		}
		if !strings.Contains(gotBody, "\n") {
			t.Errorf("body lost its newline: %q", gotBody[:min(len(gotBody), 10)])
		}
		if strings.Contains(gotBody, "\x1b") {
			t.Error("body kept a raw ESC; the sanitize half of the trim did not run")
		}
		if n := marshaledLen(gotTitle, gotBody, vibekit.PushSubject{}); n > pushBodyCap {
			t.Errorf("marshaled payload = %d bytes, exceeds cap %d", n, pushBodyCap)
		}
	})

	t.Run("title loses newlines", func(t *testing.T) {
		title := "a\x1bb\nc" + strings.Repeat("y", 4000)
		gotTitle, gotBody, truncated := fitToCap(title, "", vibekit.PushSubject{})
		if !truncated {
			t.Fatalf("fitToCap reported no truncation for a %d-byte title", len(title))
		}
		if strings.ContainsAny(gotTitle, "\n\r\x1b") {
			t.Errorf("title kept a record-forging rune: %q", gotTitle[:min(len(gotTitle), 10)])
		}
		if n := marshaledLen(gotTitle, gotBody, vibekit.PushSubject{}); n > pushBodyCap {
			t.Errorf("marshaled payload = %d bytes, exceeds cap %d", n, pushBodyCap)
		}
	})
}
