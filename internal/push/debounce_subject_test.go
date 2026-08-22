package push

// The debounce window belongs to a KIND AND A SUBJECT, asserted at the level where
// the defect lived: the integrated Send path, not the poller's fake notifier.
//
// The poller's own subject test could not see this. Its notifier records every call
// and never runs the real preflight gate, so the refactor that gave each pull
// request a distinct subject looked complete while push.Service still keyed the
// window on kind alone — dropping the second PR of a poll inside five seconds, after
// the poller had already advanced `seen` for it, so nothing ever retried it.
//
// "Reaches delivery" is asserted by counting the HTTP requests the service actually
// issues. The transport is swapped for a counter rather than pointed at an
// httptest server because the real one is ssrf.SafeTransport, which refuses a
// loopback address by design — so an httptest endpoint would measure the SSRF guard
// instead of the debounce.

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// countingTransport accepts every delivery and counts it.
type countingTransport struct {
	mu sync.Mutex
	n  int
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusCreated,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}, nil
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// newCountingService returns a service with one subscriber whose deliveries are
// counted, so a Send that passes preflight is observable as an HTTP attempt.
func newCountingService(t *testing.T) (*Service, *countingTransport) {
	t.Helper()
	s := New(t.Context(), t.TempDir(), "mailto:test@example.com")
	t.Cleanup(s.Close)
	rt := &countingTransport{}
	s.client = &http.Client{Transport: rt, Timeout: 5 * time.Second}
	s.Subscribe(pushSubscriptionWithValidKeys(t, "https://fcm.googleapis.com/fcm/send/debounce"))
	return s, rt
}

// TestSend_TwoSubjectsInOneWindowBothDeliver is the finding. Two pull requests
// settling in one poll are two notifications; the second must not be swallowed by
// the first's window, because the poller will never offer it again.
func TestSend_TwoSubjectsInOneWindowBothDeliver(t *testing.T) {
	s, rt := newCountingService(t)

	first := vibekit.PRSubject("github:github.com", "cplieger/vibekit", 1)
	second := vibekit.PRSubject("github:github.com", "cplieger/vibekit", 2)

	s.Send(t.Context(), DefaultTitle, "#1 checks passed", vibekit.PushKindPRStatus, first)
	s.Send(t.Context(), DefaultTitle, "#2 checks failed", vibekit.PushKindPRStatus, second)

	if got := rt.count(); got != 2 {
		t.Errorf("deliveries = %d, want 2: a second pull request settling inside the "+
			"five-second window was dropped, and the poller has already advanced past it", got)
	}
}

// TestSend_RepeatsOfOneSubjectStillCoalesce is the half worth keeping. Per-subject
// keying must not turn the debounce off — a subject re-sending inside its own window
// is exactly what it exists to suppress.
func TestSend_RepeatsOfOneSubjectStillCoalesce(t *testing.T) {
	s, rt := newCountingService(t)
	subject := vibekit.PRSubject("github:github.com", "cplieger/vibekit", 7)

	for range 3 {
		s.Send(t.Context(), DefaultTitle, "#7 checks passed", vibekit.PushKindPRStatus, subject)
	}

	if got := rt.count(); got != 1 {
		t.Errorf("deliveries = %d, want 1: repeats of one subject must coalesce", got)
	}
}

// TestSend_SubjectWindowsAreKindScoped keeps the two axes independent: the same chat
// can be the subject of a finished note and an ask, and neither may silence the
// other.
func TestSend_SubjectWindowsAreKindScoped(t *testing.T) {
	s, rt := newCountingService(t)
	chat := vibekit.ChatSubject("c-abc")

	s.Send(t.Context(), DefaultTitle, "finished", vibekit.PushKindAgentFinished, chat)
	s.Send(t.Context(), DefaultTitle, "may I", vibekit.PushKindPermission, chat)

	if got := rt.count(); got != 2 {
		t.Errorf("deliveries = %d, want 2: one kind's window suppressed another's on the same chat", got)
	}
}

// TestDebounceKey_GlobalSubjectIsExplicit pins the workspace-global slot as a stated
// member of the key space rather than a zero value nothing names, and pins that it
// cannot be collided with by a real subject.
func TestDebounceKey_GlobalSubjectIsExplicit(t *testing.T) {
	global := debounceKey(vibekit.PushKindAgentFinished, vibekit.PushSubject{})
	if global.subject != pushSubjectGlobal {
		t.Errorf("empty subject keyed as %q, want the named global slot %q",
			global.subject, pushSubjectGlobal)
	}
	chat := debounceKey(vibekit.PushKindAgentFinished, vibekit.ChatSubject("c-abc"))
	pull := debounceKey(vibekit.PushKindPRStatus, vibekit.PRSubject("github:github.com", "a/b", 1))
	for _, k := range []pushDebounceKey{chat, pull} {
		if k.subject == pushSubjectGlobal {
			t.Errorf("a real subject (%+v) landed in the workspace-global slot", k)
		}
	}
	if chat == global {
		t.Error("a chat subject shares the global window")
	}
}

// TestPruneDebounce_DropsOnlyExpiredEntries keeps the map bounded without changing a
// decision: an entry inside its window still suppresses, an expired one is gone.
func TestPruneDebounce_DropsOnlyExpiredEntries(t *testing.T) {
	s := New(t.Context(), t.TempDir(), "mailto:test@example.com")
	defer s.Close()

	live := debounceKey(vibekit.PushKindPRStatus, vibekit.PRSubject("github:github.com", "a/b", 999))
	s.mu.Lock()
	for i := range debounceHighWater + 8 {
		s.lastPush[debounceKey(vibekit.PushKindPRStatus,
			vibekit.PRSubject("github:github.com", "a/b", i))] = time.Now().Add(-2 * pushDebounce)
	}
	s.lastPush[live] = time.Now()
	before := len(s.lastPush)
	s.pruneDebounceLocked()
	after := len(s.lastPush)
	_, liveKept := s.lastPush[live]
	s.mu.Unlock()

	if after >= before {
		t.Errorf("prune left %d of %d entries; expired windows are not being reclaimed", after, before)
	}
	if !liveKept {
		t.Error("prune dropped an entry still inside its window, so its next send would not be suppressed")
	}
}

// TestPruneDebounce_EngagesAtTheHighWaterMark states where the prune starts
// working.
//
// The map holds one entry per distinct chat and pull request a long-lived process
// has notified about, and the high-water mark is the whole bound on it: a prune
// that only engages one entry PAST the mark makes the number the constant names
// something the map is always allowed to exceed, which is not a bound anyone can
// reason about in a process that runs for weeks.
func TestPruneDebounce_EngagesAtTheHighWaterMark(t *testing.T) {
	s := New(t.Context(), t.TempDir(), "mailto:test@example.com")
	defer s.Close()

	s.mu.Lock()
	for i := range debounceHighWater {
		s.lastPush[debounceKey(vibekit.PushKindPRStatus,
			vibekit.PRSubject("github:github.com", "a/b", i))] = time.Now().Add(-2 * pushDebounce)
	}
	seeded := len(s.lastPush)
	s.pruneDebounceLocked()
	after := len(s.lastPush)
	s.mu.Unlock()

	if seeded != debounceHighWater {
		t.Fatalf("seeded %d entries, want %d — the fixture is not at the mark", seeded, debounceHighWater)
	}
	if after != 0 {
		t.Errorf("prune left %d of %d expired entries at the high-water mark, want 0", after, debounceHighWater)
	}
}
