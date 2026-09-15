package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/sse/ssetest"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// dataFrames parses a recorded stream into its data-bearing frames, the hello
// included and the bare retry line excluded, which is what the cut counts.
func dataFrames(t *testing.T, body string) []ssetest.Frame {
	t.Helper()
	frames, err := ssetest.ReadFrames(strings.NewReader(body), 0)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	var out []ssetest.Frame
	for _, f := range frames {
		if f.Event != "retry" {
			out = append(out, f)
		}
	}
	return out
}

func TestSSEProbe_CountsConnectsByWireAndServedClients(t *testing.T) {
	h, _, _ := newTestHub()
	if n := h.SSEClientCount(); n != 0 {
		t.Errorf("SSEClientCount before any connect = %d, want 0", n)
	}
	coldConnectAs(t, h, true)
	coldConnectAs(t, h, false)
	coldConnectAs(t, h, false)
	legacy, v3 := h.SSEConnects()
	if legacy != 1 || v3 != 2 {
		t.Errorf("SSEConnects = (%d, %d), want (1, 2)", legacy, v3)
	}
}

// The armed cut lands between two frames: frame n is on the wire, the write that
// would carry frame n+1 fails, and Serve returns before the peer's deadline.
func TestCloseNextSSEAfter_CutsTheNextConnectionAfterNFrames(t *testing.T) {
	h, _, _ := newTestHub()
	h.CloseNextSSEAfter(2) // the hello and the connected envelope

	started := time.Now()
	body := coldConnectAs(t, h, false).Body.String()
	if elapsed := time.Since(started); elapsed >= fixtureConnectDeadline {
		t.Errorf("the cut connection ran %v, the whole fixture deadline; the stream was never cut", elapsed)
	}
	frames := dataFrames(t, body)
	if len(frames) != 2 {
		t.Fatalf("frames on the cut stream = %d, want exactly 2: %q", len(frames), body)
	}
	if !strings.Contains(frames[1].Data, string(vibekit.EventConnected)) {
		t.Errorf("second frame = %q, want the connected envelope", frames[1].Data)
	}
	if strings.Contains(body, string(vibekit.EventPendingSnapshot)) {
		t.Errorf("the cut stream carries pending_snapshot; the cut must land before frame 3")
	}

	// Single-shot: the next connection streams the whole hook.
	next := dataFrames(t, coldConnectAs(t, h, false).Body.String())
	if len(next) < 4 {
		t.Errorf("the connection after the cut carried %d frames, want the full hook (hello, connected, pending_snapshot, status_snapshot)", len(next))
	}
}

// hookFrames counts the connect hook's data frames on a recorded stream: every
// data frame that is not a keepalive.
func hookFrames(t *testing.T, body string) int {
	t.Helper()
	n := 0
	for _, f := range dataFrames(t, body) {
		if f.Event != keepaliveEventName {
			n++
		}
	}
	return n
}

// A keepalive on an armed connection spends none of the budget: armed with exactly
// the hook's frame count, the connection keeps beating until its deadline instead
// of being cut on the first beat. Serial: it writes the package var the hub reads
// at construction.
func TestCloseNextSSEAfter_KeepaliveSpendsNoBudget(t *testing.T) {
	prev := keepaliveInterval
	keepaliveInterval = 10 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = prev })
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	budget := hookFrames(t, coldConnectAs(t, h, false).Body.String())

	h.CloseNextSSEAfter(budget)
	body := coldConnectAs(t, h, false).Body.String()
	if got := hookFrames(t, body); got != budget {
		t.Errorf("armed connection carried %d hook frames, want %d (the budget)", got, budget)
	}
	if beats := strings.Count(body, string(keepaliveFrameStart)); beats == 0 {
		t.Errorf("armed connection carried no keepalive after spending its %d-frame budget; a beat was counted as a frame and cut the stream: %q", budget, body)
	}
}

func TestCloseNextSSEAfter_ZeroDisarms(t *testing.T) {
	h, _, _ := newTestHub()
	h.CloseNextSSEAfter(3)
	h.CloseNextSSEAfter(0)
	frames := dataFrames(t, coldConnectAs(t, h, false).Body.String())
	if len(frames) < 4 {
		t.Errorf("a disarmed connection carried %d frames, want the full hook", len(frames))
	}
}
