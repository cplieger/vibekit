package push

// Tests added by mutant-killing unit vibekit-u16. Targets surviving
// gremlins mutants in persist.go, send.go, and service.go.
//
// All identifiers defined here are prefixed gk_vibekit_u16_ to avoid
// collisions with sibling units that may share this package.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// --- shared log-capture infrastructure -------------------------------------

type gk_vibekit_u16_logRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type gk_vibekit_u16_logCapture struct {
	mu   sync.Mutex
	recs []gk_vibekit_u16_logRec
}

func (c *gk_vibekit_u16_logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *gk_vibekit_u16_logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	c.recs = append(c.recs, gk_vibekit_u16_logRec{level: r.Level, msg: r.Message, attrs: attrs})
	c.mu.Unlock()
	return nil
}

func (c *gk_vibekit_u16_logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *gk_vibekit_u16_logCapture) WithGroup(string) slog.Handler      { return c }

func (c *gk_vibekit_u16_logCapture) has(msg string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.msg == msg {
			return true
		}
	}
	return false
}

func (c *gk_vibekit_u16_logCapture) find(msg string) (gk_vibekit_u16_logRec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.msg == msg {
			return r, true
		}
	}
	return gk_vibekit_u16_logRec{}, false
}

// gk_vibekit_u16_installCapture swaps the global slog default for a
// capturing handler (all levels) and restores it via t.Cleanup. Tests
// using it must NOT call t.Parallel (global default is shared).
func gk_vibekit_u16_installCapture(t *testing.T) *gk_vibekit_u16_logCapture {
	t.Helper()
	c := &gk_vibekit_u16_logCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(old) })
	return c
}

func gk_vibekit_u16_asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// gk_vibekit_u16_errRT is a RoundTripper that always errors, so push()
// can reach its post-crypto body-assembly code without any real network.
type gk_vibekit_u16_errRT struct{}

func (gk_vibekit_u16_errRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("gk_vibekit_u16: forced transport error")
}

const gk_vibekit_u16_subject = "mailto:u16@example.com"

// --- persist.go:52 — loadKeys VAPID-persist success must not warn -----------

// Kills persist.go:52:73 CONDITIONALS_NEGATION (`saveErr != nil`). On a
// writable temp dir the VAPID-keys write succeeds (saveErr == nil), so the
// original logs nothing. The mutant (`saveErr == nil`) would log the
// failure warning on the success path.
func Test_gk_vibekit_u16_LoadKeysPersistSuccessNoWarn(t *testing.T) {
	capLog := gk_vibekit_u16_installCapture(t)
	s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
	defer s.Close()

	if capLog.has("push: persist VAPID keys failed") {
		t.Errorf("loadKeys logged %q on a successful key write; want no warning",
			"push: persist VAPID keys failed")
	}
}

// --- persist.go:163 — writeSubsSnapshot success must not warn ---------------

// Kills persist.go:163:73 CONDITIONALS_NEGATION (`saveErr != nil`). A write
// to a writable dir succeeds, so the original logs nothing; the mutant
// (`saveErr == nil`) logs the failure warning on success.
func Test_gk_vibekit_u16_WriteSubsSnapshotSuccessNoWarn(t *testing.T) {
	dir := t.TempDir()
	s := &Service{dir: dir}
	capLog := gk_vibekit_u16_installCapture(t)

	s.writeSubsSnapshot([]api.PushSubscription{
		{Endpoint: "https://fcm.googleapis.com/fcm/send/u16"},
	})

	if capLog.has("push: persist subscriptions failed") {
		t.Errorf("writeSubsSnapshot logged %q on a successful write; want none",
			"push: persist subscriptions failed")
	}
	// Sanity: the file was actually written (proves we hit the success path).
	if _, err := os.Stat(s.subsPath()); err != nil {
		t.Fatalf("writeSubsSnapshot did not write %s: %v", s.subsPath(), err)
	}
}

// --- persist.go:86:46 & 86:63 — loadSubs drop logs the parsed host ----------

// Kills persist.go:86:46 (`err == nil`) and 86:63 (`u.Host != ""`),
// both CONDITIONALS_NEGATION. For a disallowed endpoint that parses to a
// non-empty host, the original logs host="evil.example.com". Either mutant
// makes the condition false, leaving host="unknown".
func Test_gk_vibekit_u16_LoadSubsDropDisallowedHostLogged(t *testing.T) {
	dir := t.TempDir()
	subs := []api.PushSubscription{
		{Endpoint: "https://evil.example.com/steal"},
	}
	data, err := json.Marshal(subs)
	if err != nil {
		t.Fatalf("marshal subs: %v", err)
	}
	if werr := os.WriteFile(s_subsFileForDir(t, dir), data, 0o600); werr != nil {
		t.Fatalf("write subs file: %v", werr)
	}

	s := &Service{dir: dir, subs: make(map[string]api.PushSubscription)}
	capLog := gk_vibekit_u16_installCapture(t)

	s.loadSubs()

	rec, ok := capLog.find("push: dropping subscription with disallowed endpoint")
	if !ok {
		t.Fatalf("loadSubs did not log the disallowed-endpoint drop")
	}
	if got := rec.attrs["host"]; got != "evil.example.com" {
		t.Errorf("dropped-endpoint host = %v, want %q", got, "evil.example.com")
	}
}

// s_subsFileForDir returns the push-subs.json path for dir via a throwaway
// Service so the test does not duplicate the path-join logic under test.
func s_subsFileForDir(t *testing.T, dir string) string {
	t.Helper()
	return (&Service{dir: dir}).subsPath()
}

// --- service.go:165:44 — Subscribe logs the parsed host ---------------------

// Kills service.go:165:44 CONDITIONALS_NEGATION (`err == nil`). Subscribing
// a parseable endpoint logs host=<host>; the mutant (`err != nil`) leaves
// host="unknown".
func Test_gk_vibekit_u16_SubscribeHostLogged(t *testing.T) {
	s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
	defer s.Close()

	capLog := gk_vibekit_u16_installCapture(t)
	s.Subscribe(api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/u16-sub"})

	rec, ok := capLog.find("push: subscribed")
	if !ok {
		t.Fatalf("Subscribe did not log %q", "push: subscribed")
	}
	if got := rec.attrs["host"]; got != "fcm.googleapis.com" {
		t.Errorf("subscribed host = %v, want %q", got, "fcm.googleapis.com")
	}
}

// --- service.go:117:17 — HTTP client timeout is 10 * time.Second ------------

// Kills service.go:117:17 ARITHMETIC_BASE (`10 * time.Second`). The mutant
// (`10 / time.Second`) is integer division → 0 → no timeout.
func Test_gk_vibekit_u16_ClientTimeoutTenSeconds(t *testing.T) {
	s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
	defer s.Close()

	if s.client.Timeout != 10*time.Second {
		t.Errorf("client.Timeout = %v, want %v", s.client.Timeout, 10*time.Second)
	}
}

// --- persist.go:98:15 — saveSubsAsync context guard -------------------------

// Kills persist.go:98:15 CONDITIONALS_NEGATION (`ctx.Err() != nil`). With no
// write loop draining saveCh, a queued request leaves len(saveCh)==1.
// Cancelled guard ctx: original returns early (0 queued); mutant queues (1).
// Active guard ctx: original queues (1); mutant returns early (0).
func Test_gk_vibekit_u16_SaveSubsAsyncCtxGuard(t *testing.T) {
	newSvc := func() *Service {
		return &Service{
			subs:   map[string]api.PushSubscription{},
			saveCh: make(chan saveRequest, 1),
			ctx:    context.Background(), // service ctx active so the send path is taken
		}
	}

	t.Run("cancelled_guard_skips_save", func(t *testing.T) {
		s := newSvc()
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.saveSubsAsync(cctx)
		if n := len(s.saveCh); n != 0 {
			t.Errorf("saveSubsAsync(cancelled) queued %d requests, want 0", n)
		}
	})

	t.Run("active_guard_queues_save", func(t *testing.T) {
		s := newSvc()
		s.saveSubsAsync(context.Background())
		if n := len(s.saveCh); n != 1 {
			t.Errorf("saveSubsAsync(active) queued %d requests, want 1", n)
		}
	})
}

// --- persist.go:120:15 — saveSubs context guard -----------------------------

// Kills persist.go:120:15 CONDITIONALS_NEGATION (`ctx.Err() != nil`).
// saveSubs is synchronous (blocks on the write loop). Cancelled guard:
// original returns early → no file written; mutant proceeds → file written.
// Active guard: original writes; mutant returns early → no file.
func Test_gk_vibekit_u16_SaveSubsCtxGuard(t *testing.T) {
	const ep = "https://fcm.googleapis.com/fcm/send/u16-savesubs"

	t.Run("cancelled_guard_skips_write", func(t *testing.T) {
		s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
		defer s.Close()
		s.mu.Lock()
		s.subs[ep] = api.PushSubscription{Endpoint: ep}
		s.mu.Unlock()
		_ = os.Remove(s.subsPath()) // ensure absent before the call

		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.saveSubs(cctx)

		if _, err := os.Stat(s.subsPath()); !os.IsNotExist(err) {
			t.Errorf("saveSubs(cancelled) wrote %s (stat err=%v), want no write",
				s.subsPath(), err)
		}
	})

	t.Run("active_guard_writes", func(t *testing.T) {
		s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
		defer s.Close()
		s.mu.Lock()
		s.subs[ep] = api.PushSubscription{Endpoint: ep}
		s.mu.Unlock()
		_ = os.Remove(s.subsPath())

		s.saveSubs(context.Background())

		if _, err := os.Stat(s.subsPath()); err != nil {
			t.Errorf("saveSubs(active) did not write %s: %v", s.subsPath(), err)
		}
	})
}

// --- send.go:41 — oversize truncation WARN ----------------------------------

// Kills send.go:41:25 ARITHMETIC_BASE (`len(title)+len(body)`),
// 41:44 CONDITIONALS_BOUNDARY (`total > pushBodyCap`), and
// 41:44 CONDITIONALS_NEGATION. The "payload too large, truncating" Warn (and
// its "bytes" total) fires iff len(title)+len(body) > pushBodyCap.
func Test_gk_vibekit_u16_SendOversizeTruncationWarn(t *testing.T) {
	const warnMsg = "push: payload too large, truncating"

	t.Run("small_does_not_warn", func(t *testing.T) {
		// total=4: original 4>3000 false (no warn). NEGATION 4<=3000 true (warn).
		s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
		defer s.Close()
		capLog := gk_vibekit_u16_installCapture(t)
		s.Send(context.Background(), "aa", "bb", api.PushKindAgentFinished)
		if capLog.has(warnMsg) {
			t.Errorf("Send warned %q for a 4-byte payload; want no warn", warnMsg)
		}
	})

	t.Run("oversize_warns_with_total_bytes", func(t *testing.T) {
		// title=10, body=4000, total=4010: original warns bytes=4010.
		// ARITHMETIC (10-4000=-3990) and NEGATION (4010<=3000) both suppress it.
		s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
		defer s.Close()
		capLog := gk_vibekit_u16_installCapture(t)
		s.Send(context.Background(), strings.Repeat("a", 10), strings.Repeat("b", 4000),
			api.PushKindAgentFinished)
		rec, ok := capLog.find(warnMsg)
		if !ok {
			t.Fatalf("Send did not warn %q for a 4010-byte payload", warnMsg)
		}
		if got, ok := gk_vibekit_u16_asInt64(rec.attrs["bytes"]); !ok || got != 4010 {
			t.Errorf("truncation warn bytes = %v, want 4010", rec.attrs["bytes"])
		}
	})

	t.Run("boundary_equal_does_not_warn", func(t *testing.T) {
		// total=3000 exactly: original 3000>3000 false (no warn).
		// BOUNDARY 3000>=3000 true (warn). NEGATION 3000<=3000 true (warn).
		s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
		defer s.Close()
		capLog := gk_vibekit_u16_installCapture(t)
		s.Send(context.Background(), strings.Repeat("a", 1000), strings.Repeat("b", 2000),
			api.PushKindAgentFinished)
		if capLog.has(warnMsg) {
			t.Errorf("Send warned %q at exactly pushBodyCap total; want no warn", warnMsg)
		}
	})
}

// --- send.go:153:18 — push payload-size guard boundary ----------------------

// Kills send.go:153:18 CONDITIONALS_BOUNDARY (`len(payload) > pushBodyCap`).
// At exactly pushBodyCap bytes the original passes the guard and fails later
// (decode p256dh); the mutant (`>=`) rejects with "payload too large".
func Test_gk_vibekit_u16_PushPayloadSizeBoundary(t *testing.T) {
	s := &Service{ctx: context.Background()}
	sub := api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/u16-size"}
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

// --- send.go:200 — RFC 8291 body capacity arithmetic ------------------------

// Kills send.go:200:32 and 200:49 ARITHMETIC_BASE. Those `+` operators flip
// to `-`, driving the make([]byte, 0, cap) capacity negative for the chosen
// payload sizes (panic). The original computes a positive cap (no panic) and
// then fails at the forced transport error.
//
// 200:28 and 200:30 are documented equivalent (their cap stays positive for
// every payload, so no observable difference exists).
func Test_gk_vibekit_u16_PushBodyCapacityNegativePanic(t *testing.T) {
	s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
	defer s.Close()
	s.client = &http.Client{Transport: gk_vibekit_u16_errRT{}}
	sub := pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/u16-cap")

	// payload<26 → 200:32 mutant cap = len-26 < 0 → panic.
	gk_vibekit_u16_pushExpectNoPanic(t, s, sub, make([]byte, 10), "small")
	// payload>68 → 200:49 mutant cap = 68-len < 0 → panic.
	gk_vibekit_u16_pushExpectNoPanic(t, s, sub, make([]byte, 100), "large")
}

func gk_vibekit_u16_pushExpectNoPanic(t *testing.T, s *Service, sub api.PushSubscription, payload []byte, label string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("push(%s, len=%d) panicked (mutated capacity went negative): %v",
				label, len(payload), r)
		}
	}()
	_, err := s.push(context.Background(), sub, payload)
	if err == nil {
		t.Errorf("push(%s) err = nil, want forced transport error", label)
		return
	}
	// Reaching the forced transport error proves push() traversed past the
	// line-200 make([]byte,...) — so the mutated-capacity panic is on the
	// exercised path.
	if !strings.Contains(err.Error(), "forced transport error") {
		t.Errorf("push(%s) err = %v, want forced transport error (post-line-200)", label, err)
	}
}

// --- send.go:82 / 90 / 243 — per-result + fan-out + drain logging -----------

// Kills:
//   - send.go:82:19 CONDITIONALS_BOUNDARY/NEGATION (`code >= 400`): the
//     "unexpected status" Warn fires iff code>=400 (and not 410/404).
//   - send.go:90:26 CONDITIONALS_NEGATION (`err != nil` after g.Wait): g.Wait
//     is always nil, so the original never logs "fan-out wait"; the mutant does.
//   - send.go:243:92 CONDITIONALS_NEGATION (`copyErr != nil` drain): a clean
//     drain leaves copyErr nil, so the original never logs "drain response
//     body"; the mutant does.
func Test_gk_vibekit_u16_SendResultLogging(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantUnexpec bool
	}{
		{"400_logs_unexpected", http.StatusBadRequest, true}, // kills 82 BOUNDARY + NEGATION
		{"500_logs_unexpected", http.StatusInternalServerError, true},
		{"201_no_unexpected", http.StatusCreated, false}, // kills 82 NEGATION (other branch)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
			defer s.Close()
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			capLog := gk_vibekit_u16_installCapture(t)
			s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)

			if got := capLog.has("push: unexpected status"); got != tc.wantUnexpec {
				t.Errorf("status %d: logged unexpected-status = %v, want %v",
					tc.status, got, tc.wantUnexpec)
			}
			// g.Wait always returns nil → never log fan-out wait (kills 90).
			if capLog.has("push: fan-out wait") {
				t.Errorf("status %d: logged %q though g.Wait() returns nil",
					tc.status, "push: fan-out wait")
			}
			// A clean response body drains without error (kills 243).
			if capLog.has("push: drain response body") {
				t.Errorf("status %d: logged %q though the drain succeeded",
					tc.status, "push: drain response body")
			}
		})
	}
}

// --- send.go:217:17 — ctx merge when the service ctx is cancelled -----------

// Kills send.go:217:17 CONDITIONALS_NEGATION (`s.ctx.Err() != nil`). With the
// service ctx cancelled, the original merges it into the request ctx, so the
// request to a blocking server is cancelled immediately (context.Canceled).
// The mutant (`== nil`) skips the merge, using only the caller ctx, so the
// request blocks until the caller's deadline (context.DeadlineExceeded).
func Test_gk_vibekit_u16_PushMergesCtxWhenServiceCtxCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the request ctx is cancelled
	}))
	defer srv.Close()

	s := New(context.Background(), t.TempDir(), gk_vibekit_u16_subject)
	defer s.Close()
	s.client = srv.Client()
	sub := pushSubscriptionWithValidKeys(t, srv.URL)

	s.cancel() // cancel the service ctx → s.ctx.Err() != nil

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
