package agent

// The cold-connect WIRE-BYTE gate. Every assertion here measures rec.Body after
// rt.handleSSE — the bytes that actually go out — never a count of events, because
// the defect being gated is a payload size and an event count cannot see it. The
// existing sse_test.go asserts the replay's SHAPE and never its SIZE, which is how
// an 18 MB connect arrived without a single test noticing.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Fixture sizes, all deliberately OVER the state sse-gap.md measured on the live
// instance (18 open tabs, 6 concurrent runs, one 33.5 MB chat file): a gate sized
// at the observation would pass the moment the observation moved.
const (
	fixtureReasoningBytes  = 3 << 20
	fixtureContentBytes    = 1 << 20
	fixtureToolCalls       = 20
	fixtureToolOutputBytes = 100 << 10
	// fixtureDeltaChunks keeps the fill a STREAM rather than one delta, so the
	// per-block accumulation the wire performs is the accumulation measured.
	fixtureDeltaChunks = 8
	// fixturePendingPerms and fixturePendingRunAsks keep the non-snapshot
	// remainder non-zero, so the total assertion can still fail on a connect that
	// withholds every snapshot.
	fixturePendingPerms    = 2
	fixturePendingRunAsks  = 1
	fixtureBusyChats       = 6
	fixtureManyBusyChats   = 24
	fixtureFlatnessBudget  = 24 * 1024
	fixtureDeclaredBudget  = maxColdConnectBytes / 2
	fixtureSnapshotMarker  = `"message":`
	fixtureFrameSeparator  = "\n\n"
	fixtureConnectDeadline = 150 * time.Millisecond
)

// newBudgetRuntime builds the runtime the budget tests connect to, with the
// open-tab set WIRED: an unwired store makes every chat look open for a different
// reason, so a fixture that skipped it could not tell a tab filter that works from
// one that was never consulted.
func newBudgetRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	st, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore(%q): %v", dir, err)
	}
	cs := newFakeChatStore()
	br := newFakeBridge()
	rt := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs,
		WithTabs(st), WithConfigDir(dir))
	cs.Bus = rt
	rt.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, rt) })
	return rt
}

// busyChatsWithHugeTurns opens n busy chats and fills each one's turn buffer. WHAT
// the fixture must hold is what decides whether each assertion can fail at all:
//
//   - A PROMPT-sourced open turn per chat. replayTurnState skips a PRIME's turn
//     outright, so a prime-sourced fixture emits nothing and all four assertions
//     pass over an empty connect.
//   - A buffer whose turn has STARTED. Buffer.Snapshot keys on the message id, so
//     an unstarted buffer reports no snapshot and the connect carries only the bare
//     busy signal — kilobytes, under every budget here.
//   - 3 MiB of Reasoning and 1 MiB of Content per turn, written through the
//     buffer's own Append* methods so the flat-field/Blocks duplication is REAL:
//     a snapshot carries each of those bytes TWICE, and that duplication is what
//     makes one turn_state frame megabytes rather than kilobytes. Hand-built Blocks
//     would halve the frame and let the frame-budget assertion pass on a payload
//     the wire never produces.
//   - 20 tool calls carrying 100 KiB of Output each, so a cap on the text streams
//     alone still leaves a frame over the frame budget.
//   - One open chat TAB per chat, so a later open-tab filter has something to KEEP
//     rather than everything to drop.
//   - 2 pending permission asks and 1 pending run ask, so the remainder that no
//     snapshot cap can reduce is on the wire and measured.
func busyChatsWithHugeTurns(tb testing.TB, rt *Runtime, n int) []vibekit.ChatID {
	tb.Helper()
	ids := make([]vibekit.ChatID, 0, n)
	for i := range n {
		id := vibekit.ChatID(fmt.Sprintf("c-budget-%02d", i))
		rt.bridge.mgr.orInsert(id)
		if epoch := rt.coord.StartTurn(tb.Context(), id, vibekit.TurnSourcePrompt); epoch == 0 {
			tb.Fatalf("StartTurn(%q) refused, so the chat is not busy and the connect replays nothing", id)
		}
		buf := rt.liveTurnBuffer(id)
		if buf == nil {
			tb.Fatalf("no live turn buffer for %q, so there is nothing to fill", id)
		}
		if !buf.StartTurn("m-" + string(id)) {
			tb.Fatalf("turn for %q was already started, so the fixture is not the one filling it", id)
		}
		fillTurnBuffer(buf, string(id))
		openBudgetChatTab(tb, rt, id)
		ids = append(ids, id)
	}
	seedPendingDecisions(tb, rt, ids)
	return ids
}

// fillTurnBuffer writes one turn's worth of content through the buffer's own append
// methods. Reasoning and Content arrive as separate streams because they land in
// separate builders AND separate blocks, which is the shape a snapshot doubles.
func fillTurnBuffer(buf *buffer.Buffer, chatID string) {
	reasoning := strings.Repeat("r", fixtureReasoningBytes/fixtureDeltaChunks)
	content := strings.Repeat("c", fixtureContentBytes/fixtureDeltaChunks)
	for range fixtureDeltaChunks {
		buf.AppendThinkingDelta(reasoning, "")
	}
	for range fixtureDeltaChunks {
		buf.AppendTextDelta(content, "")
	}
	output := strings.Repeat("o", fixtureToolOutputBytes)
	for i := range fixtureToolCalls {
		toolID := fmt.Sprintf("%s-tool-%02d", chatID, i)
		buf.AppendToolCall(&vibekit.ToolCall{
			ID:     toolID,
			Title:  "budget fixture",
			Kind:   vibekit.ToolKindExecute,
			Status: vibekit.ToolCompleted,
			Output: output,
		})
		buf.AppendToolUseBlock(toolID, "")
	}
}

// openBudgetChatTab puts the chat in the server-owned open-tab set, which is the
// half of the fixture a tab filter reads.
func openBudgetChatTab(tb testing.TB, rt *Runtime, id vibekit.ChatID) {
	tb.Helper()
	if _, _, _, err := rt.tabs.Open(tb.Context(), vibekit.OpenTab{
		Kind: vibekit.TabKindChat,
		Ref:  string(id),
	}); err != nil {
		tb.Fatalf("open chat tab for %q: %v", id, err)
	}
}

// seedPendingDecisions adds the unanswered asks a real reconnect replays beside the
// snapshots, so the measured total includes the part no snapshot cap can shrink.
func seedPendingDecisions(tb testing.TB, rt *Runtime, ids []vibekit.ChatID) {
	tb.Helper()
	if len(ids) == 0 {
		tb.Fatal("no chats in the fixture, so there is nothing to attach a pending ask to")
	}
	for i := range fixturePendingPerms {
		requestID := int64(i + 1)
		rt.bus.pendingPerms.Add(requestID, vibekit.NewEvent(
			vibekit.EventPermissionNeeded, ids[i%len(ids)], vibekit.PermissionNeededPayload{
				RequestID:  requestID,
				ToolCallID: fmt.Sprintf("perm-%d", requestID),
				Title:      "Run a command",
				Kind:       vibekit.ToolKindExecute,
				Options: []vibekit.PermissionOption{
					{OptionID: "allow", Name: "Allow", Kind: "allow_once"},
					{OptionID: "reject", Name: "Reject", Kind: "reject_once"},
				},
			}))
	}
	for i := range fixturePendingRunAsks {
		if !rt.runs.asks.Add(&runAsk{chatID: ids[0], payload: vibekit.RunInputNeededPayload{
			WorkflowID: "wf-budget",
			AskID:      fmt.Sprintf("ask-%d", i+1),
			Question:   "Which branch should the step target?",
		}}) {
			tb.Fatalf("pending run ask %d was refused, so the fixture is short a frame", i+1)
		}
	}
}

// coldConnect drives one cold connect and hands back the recorder, whose Body IS
// the measured wire bytes. The 150ms deadline bounds only the LIVE loop that
// follows: the connect replay is written synchronously from the OnConnect hook
// before it, so the measurement is deterministic rather than a race with the clock.
func coldConnect(t *testing.T, rt *Runtime, query string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fixtureConnectDeadline)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events"+query, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	rt.handleSSE(rec, req)
	return rec
}

func TestHandleSSE_ColdConnectStaysUnderBudget(t *testing.T) {
	rt := newBudgetRuntime(t)
	busyChatsWithHugeTurns(t, rt, fixtureBusyChats)

	got := coldConnect(t, rt, "").Body.Len()

	if got >= maxColdConnectBytes {
		t.Errorf("cold connect over %d busy chats wrote %d bytes, want < %d (maxColdConnectBytes)",
			fixtureBusyChats, got, maxColdConnectBytes)
	}
}

// TestHandleSSE_NoSingleFrameExceedsTheFrameBudget is the assertion the total
// cannot make: WebKit buffers a whole SSE frame before dispatch, so one 3 MB frame
// is a peak-memory cost in the network and parse layers however small the total is.
func TestHandleSSE_NoSingleFrameExceedsTheFrameBudget(t *testing.T) {
	rt := newBudgetRuntime(t)
	busyChatsWithHugeTurns(t, rt, fixtureBusyChats)

	frames := strings.Split(coldConnect(t, rt, "").Body.String(), fixtureFrameSeparator)

	over, largest, largestIdx := 0, 0, -1
	for i, frame := range frames {
		if len(frame) > largest {
			largest, largestIdx = len(frame), i
		}
		if len(frame) >= maxConnectFrameBytes {
			over++
		}
	}
	if over > 0 {
		t.Errorf("%d of %d connect frames are >= %d bytes (maxConnectFrameBytes); largest is frame %d at %d bytes",
			over, len(frames), maxConnectFrameBytes, largestIdx, largest)
	}
}

// TestHandleSSE_ColdConnectIsFlatInTheNumberOfBusyChats is what makes this a gate
// on the CODE rather than on the fixture's sizes, and it is the one a per-snapshot
// cap alone cannot satisfy: N capped snapshots still grow with N.
func TestHandleSSE_ColdConnectIsFlatInTheNumberOfBusyChats(t *testing.T) {
	small := measureColdConnect(t, fixtureBusyChats)
	large := measureColdConnect(t, fixtureManyBusyChats)

	if delta := large - small; delta >= fixtureFlatnessBudget {
		t.Errorf("cold connect grew %d bytes going from %d to %d busy chats (%d -> %d bytes), want < %d: the payload is O(busy chats)",
			delta, fixtureBusyChats, fixtureManyBusyChats, small, large, fixtureFlatnessBudget)
	}
}

// measureColdConnect builds a FRESH runtime per measurement, so the two points
// differ only in the number of busy chats.
func measureColdConnect(t *testing.T, n int) int {
	t.Helper()
	rt := newBudgetRuntime(t)
	busyChatsWithHugeTurns(t, rt, n)
	return coldConnect(t, rt, "").Body.Len()
}

// TestHandleSSE_ColdConnectDeclaringOneChatCarriesOneSnapshot fails today on the
// parameter being ignored, which is correct: nothing reads ?snapshot= yet.
func TestHandleSSE_ColdConnectDeclaringOneChatCarriesOneSnapshot(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, fixtureBusyChats)

	body := coldConnect(t, rt, "?snapshot="+string(ids[0])).Body.String()

	withSnapshot := 0
	for _, frame := range strings.Split(body, fixtureFrameSeparator) {
		if strings.Contains(frame, fixtureSnapshotMarker) {
			withSnapshot++
		}
	}
	if withSnapshot != 1 {
		t.Errorf("declaring 1 of %d busy chats, %d frames carry a snapshot, want exactly 1",
			fixtureBusyChats, withSnapshot)
	}
	if got := len(body); got >= fixtureDeclaredBudget {
		t.Errorf("declaring 1 of %d busy chats wrote %d bytes, want < %d (maxColdConnectBytes/2)",
			fixtureBusyChats, got, fixtureDeclaredBudget)
	}
}
