package agent

// The cold-connect WIRE-BYTE gate. Every assertion here measures rec.Body after
// rt.handleSSE — the bytes that actually go out — never a count of events, because
// the defect being gated is a payload size and an event count cannot see it. Since
// the connect hook stopped carrying turn content (the live turn reaches the client
// through GET /api/chats/{id} and the live_turn digest subject), the gate is that
// no turn byte reaches the wire at connect however many busy chats there are, and
// that the two lists the handshake does carry stay within their caps.

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

// Fixture sizes, all deliberately OVER the state measured on the live instance
// (18 open tabs, 6 concurrent runs, one 33.5 MB chat file): a gate sized
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
//   - A PROMPT-sourced open turn per chat, so each chat is in the busy set.
//   - A buffer whose turn has STARTED and holds 3 MiB of Reasoning, 1 MiB of
//     Content and 20 tool calls of 100 KiB each, written through the buffer's own
//     Append* methods: if any connect path ever reads a turn's content again, these
//     are the bytes the flatness gate sees.
//   - One open chat TAB per chat, matching the live shape.
//   - 2 pending permission asks and 1 pending run ask, so the connect carries a
//     real pending set beside the busy list.
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
		if opened, _ := buf.StartTurn("m-" + string(id)); !opened {
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
			},
		))
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

// coldConnect drives one cold v3 connect and hands back the recorder, whose Body IS
// the measured wire bytes. The 150ms deadline bounds only the LIVE loop that
// follows: the connect replay is written synchronously from the OnConnect hook
// before it, so the measurement is deterministic rather than a race with the clock.
func coldConnect(t *testing.T, rt *Runtime, query string) *httptest.ResponseRecorder {
	t.Helper()
	return coldConnectAs(t, rt, false, query)
}

// coldConnectAs is coldConnect with the connect shape chosen: a legacy request sends
// no SSE-Wire header, which is how a v2 bundle presents.
func coldConnectAs(t *testing.T, rt *Runtime, legacy bool, query ...string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fixtureConnectDeadline)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events"+strings.Join(query, ""), nil).WithContext(ctx)
	if !legacy {
		req.Header.Set(wireHeader, "1")
	}
	rec := httptest.NewRecorder()
	rt.handleSSE(rec, req)
	return rec
}

func TestHandleSSE_ColdConnectIsFlatInTheNumberOfBusyChats(t *testing.T) {
	few := measureColdConnect(t, fixtureBusyChats)
	many := measureColdConnect(t, fixtureManyBusyChats)
	if delta := many - few; delta > fixtureFlatnessBudget {
		t.Errorf("cold connect grew by %d bytes from %d to %d busy chats, want at most %d: "+
			"the connect carries no turn content, so its size may move only by the busy-chat ids",
			delta, fixtureBusyChats, fixtureManyBusyChats, fixtureFlatnessBudget)
	}
}

// measureColdConnect returns the wire bytes of one cold v3 connect over n busy
// chats each holding a huge turn, and asserts no turn content reached the wire.
func measureColdConnect(t *testing.T, n int) int {
	t.Helper()
	rt := newBudgetRuntime(t)
	busyChatsWithHugeTurns(t, rt, n)
	body := coldConnect(t, rt, "").Body.String()
	if strings.Contains(body, strings.Repeat("r", 64)) || strings.Contains(body, strings.Repeat("c", 64)) {
		t.Fatalf("the connect carries turn content: %d bytes over %d busy chats", len(body), n)
	}
	if strings.Contains(body, string(vibekit.EventType("turn_state"))) {
		t.Fatalf("the connect carries a turn_state frame; that channel is gone")
	}
	return len(body)
}

// TestHandleSSE_ConnectedCarriesEveryBusyChatUpToTheCap pins the one per-chat
// cost that survives: 37 bytes of id per busy chat, and BusyStated true while the
// list fits.
func TestHandleSSE_ConnectedCarriesEveryBusyChatUpToTheCap(t *testing.T) {
	rt := newBudgetRuntime(t)
	ids := busyChatsWithHugeTurns(t, rt, fixtureBusyChats)
	p := connectPayload(t, rt, "")
	if !p.BusyStated {
		t.Fatal("BusyStated = false under the cap")
	}
	if len(p.BusyChats) != len(ids) {
		t.Errorf("busy_chats carries %d ids, want %d", len(p.BusyChats), len(ids))
	}
}
