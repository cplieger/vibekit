package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// noSubscriberLine is reportNoSubscribers' message, anchored whole with its closing
// quote so a reworded line fails here rather than matching a prefix.
const noSubscriberLine = `"msg":"no push subscribers; notifications are being dropped until a browser subscribes"`

// newDropHub wires a runtime whose push service starts with no subscribers, which
// is the only input the drop path reads.
func newDropHub(t *testing.T) (*Runtime, *recordingPush) {
	t.Helper()
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	fp.noSubs.Store(true)
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	return h, fp
}

// A permission ask reaches NotifyPush once per tool call, so a line per drop would
// bury the rest of the log: an episode of the no-subscriber condition is worth
// exactly one line, and without any line a dead push pipeline and a workspace
// nobody subscribed from are indistinguishable.
func TestNotifyPush_ReportsANoSubscriberDropOncePerEpisode(t *testing.T) {
	h, fp := newDropHub(t)
	logs := captureLogs(t)
	ctx := t.Context()

	for range 3 {
		h.coord.NotifyPush(ctx, "Permission needed", vibekit.PushKindPermission, "c1")
	}

	got := logs.String()
	if n := strings.Count(got, noSubscriberLine); n != 1 {
		t.Errorf("3 drops logged the no-subscriber line %d times, want 1; the latch is what keeps a"+
			" per-tool-call permission ask from flooding the log. Captured: %s", n, got)
	}
	if !strings.Contains(got, `"chat_id":"c1"`) {
		t.Errorf("the no-subscriber line carries no chat_id; captured: %s", got)
	}
	if !strings.Contains(got, `"kind":"`+string(vibekit.PushKindPermission)+`"`) {
		t.Errorf("the no-subscriber line carries no kind; captured: %s", got)
	}
	select {
	case body := <-fp.sends:
		t.Errorf("push sent %q with no subscribers, want the notification dropped", body)
	default:
	}
}

// The latch is per EPISODE, not per process: a subscriber arriving re-arms it, so a
// later unsubscribe is reported again rather than silently.
func TestNotifyPush_NoSubscriberLatchReArmsWhenASubscriberAppears(t *testing.T) {
	h, fp := newDropHub(t)
	logs := captureLogs(t)
	ctx := t.Context()

	h.coord.NotifyPush(ctx, "Permission needed", vibekit.PushKindPermission, "c1")
	if n := strings.Count(logs.String(), noSubscriberLine); n != 1 {
		t.Fatalf("first episode logged %d lines, want 1; captured: %s", n, logs.String())
	}

	fp.noSubs.Store(false)
	h.coord.NotifyPush(ctx, "Agent finished", vibekit.PushKindAgentFinished, "c1")
	select {
	case <-fp.sends:
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent while a subscriber was present, so nothing re-armed the latch")
	}

	fp.noSubs.Store(true)
	h.coord.NotifyPush(ctx, "Permission needed", vibekit.PushKindPermission, "c1")

	got := logs.String()
	if n := strings.Count(got, noSubscriberLine); n != 2 {
		t.Errorf("after a subscriber came and went, the no-subscriber line was logged %d times in total,"+
			" want 2; the second episode is silent unless a subscriber re-arms the latch. Captured: %s", n, got)
	}
}
