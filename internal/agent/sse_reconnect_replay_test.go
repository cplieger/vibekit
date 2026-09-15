package agent

// The reported reconnect sequence, at the seam rather than through a browser: a reader
// whose window closes mid-turn and re-opens on a URL naming no chat sends the withhold
// sentinel, so the connect replay carries a BARE busy signal — and the transcript GET is
// then the only channel left that can hand back the reply already on screen.
//
// Both halves are asserted in one test on purpose: the withhold is what makes the GET
// load-bearing, so asserting the GET alone would pass just as well on a build where the
// connect happened to carry the snapshot after all.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

const replayGapChat vibekit.ChatID = "c-replay-gap"

const (
	replayGapReasoning = "weighing the two shapes before answering"
	replayGapText      = "here is the first half of the reply"
	replayGapPrompt    = "do the thing"
)

// transcriptPage is the single-chat GET's response as this test READS it, spelled by
// hand rather than taken from the production struct: the field names ARE the contract
// the client decodes, so a test importing the server's own type would assert it against
// itself and pass through a rename.
type transcriptPage struct {
	LiveTurn *struct {
		Message   vibekit.Message `json:"message"`
		ChunkSeq  int64           `json:"chunk_seq"`
		Truncated bool            `json:"truncated"`
	} `json:"live_turn"`
	Messages []json.RawMessage `json:"messages"`
	TurnOpen bool              `json:"turn_open"`
}

// newReplayGapRuntime wires the runtime to a REAL chat store plus its real routes,
// because the defect IS a disagreement between two surfaces — what the connect replay
// withholds and what the transcript GET carries — so both have to be the shipped ones.
// The tab store is wired for newBudgetRuntime's reason: an unwired one makes every chat
// look open for a different reason.
func newReplayGapRuntime(t *testing.T) (*Runtime, *chat.Store, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	ts, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore(%q): %v", dir, err)
	}
	var rt *Runtime
	cs, err := chat.NewStore(t.TempDir(),
		chat.WithTurnOpen(func(id vibekit.ChatID) vibekit.TurnOpenState { return rt.TurnOpenState(id) }),
		chat.WithLiveTurn(func(id vibekit.ChatID) (vibekit.LiveTurn, bool) { return rt.LiveTurn(id) }),
	)
	if err != nil {
		t.Fatalf("chat.NewStore: %v", err)
	}
	rt = New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs,
		WithTabs(ts), WithConfigDir(dir))
	rt.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, rt) })
	mux := http.NewServeMux()
	cs.RegisterRoutes(mux)
	return rt, cs, mux
}

// openReplayGapTurn opens the turn the reader watched and fills it the way the wire
// does: reasoning AND text, through the buffer's own append methods, so the snapshot
// carries both the flat fields and the blocks the renderer reads.
func openReplayGapTurn(t *testing.T, rt *Runtime) {
	t.Helper()
	openSmallTurn(t, rt, replayGapChat, replayGapText)
	buf := rt.liveTurnBuffer(replayGapChat)
	if buf == nil {
		t.Fatalf("no live turn buffer for %q, so there is nothing to withhold or serve", replayGapChat)
	}
	buf.AppendThinkingDelta(replayGapReasoning, "")
	openBudgetChatTab(t, rt, replayGapChat)
}

// seedReplayGapPrompt writes what the chat FILE holds mid-turn: the user's prompt and
// nothing else. Seeding an assistant row would make the GET carry the reply through
// `messages` and every assertion below would pass for the wrong reason.
func seedReplayGapPrompt(t *testing.T, cs *chat.Store) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), replayGapChat, func(c *vibekit.Chat, _ bool) bool {
		c.Name = string(replayGapChat)
		c.Messages = []vibekit.Message{
			{ID: "u1", Role: vibekit.RoleUser, Content: replayGapPrompt, Ts: 1},
		}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", replayGapChat, err)
	}
}

// getTranscript drives the real route and decodes the newest page. The store's own rules
// about the field — the older-page withhold, an unwired reader, the byte charge — are
// internal/chat's and are tested there; this file's subject is whether the reader reaches
// the wire at all.
func getTranscript(t *testing.T, mux *http.ServeMux, id vibekit.ChatID) transcriptPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/%s = %d, want 200; body = %s", id, rec.Code, rec.Body.String())
	}
	var page transcriptPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode transcript page: %v; body = %s", err, rec.Body.String())
	}
	return page
}

// TestReconnectMidTurn_TheTranscriptGETCarriesTheInFlightTurn is the reported defect.
// Before the fix the connect withholds the snapshot AND the GET carries no carrier for
// it, so the reloaded view showed the prompt over an empty body.
func TestReconnectMidTurn_TheTranscriptGETCarriesTheInFlightTurn(t *testing.T) {
	rt, cs, mux := newReplayGapRuntime(t)
	openReplayGapTurn(t, rt)
	seedReplayGapPrompt(t, cs)

	// The connect the reloaded window makes carries no turn content at all, which is
	// what makes the GET the only channel for the in-flight reply. The retired frame
	// type is checked by name so its return would fail here rather than pass by luck.
	body := coldConnect(t, rt, "").Body.String()
	if strings.Contains(body, string(vibekit.EventType("turn_state"))) || strings.Contains(body, replayGapText) {
		t.Fatalf("the connect carries turn content; the GET assertions below would pass without it: %q", body)
	}
	if !connectPayload(t, rt, "").BusyStated || !busySetOf(connectPayload(t, rt, ""))[replayGapChat] {
		t.Fatal("the fixture's chat is not busy at connect, so nothing below is measuring the reconnect this test is about")
	}

	page := getTranscript(t, mux, replayGapChat)

	if !page.TurnOpen {
		t.Errorf("turn_open = false while a turn is in flight, so the fixture's turn is not open")
	}
	if page.LiveTurn == nil {
		t.Fatalf("the transcript GET carries no live_turn while a turn is in flight: a client "+
			"that declared no chat at connect has no other channel, so the reply already on "+
			"screen is unreachable. Body carried %d messages and turn_open=%v",
			len(page.Messages), page.TurnOpen)
	}
	if got := page.LiveTurn.Message.Content; !strings.Contains(got, replayGapText) {
		t.Errorf("live_turn.message.content = %q, want it to contain %q", got, replayGapText)
	}
	if got := page.LiveTurn.Message.Reasoning; !strings.Contains(got, replayGapReasoning) {
		t.Errorf("live_turn.message.reasoning = %q, want it to contain %q", got, replayGapReasoning)
	}
	if got := page.LiveTurn.Message.ID; got == "" {
		t.Errorf("live_turn.message.id is empty: the client keys its dedup and its own live-turn " +
			"marker on that id, so an unnamed message is one it cannot merge")
	}
	// The blocks are what the renderer draws from; the flat fields alone paint nothing.
	if len(page.LiveTurn.Message.Blocks) == 0 {
		t.Errorf("live_turn.message.blocks is empty, want the reasoning and text blocks: the " +
			"renderer is block-only, so flat content alone renders an empty turn body")
	}
	// `messages` still means "what the file holds", so every window computation keeps its
	// meaning and the live turn cannot be counted twice.
	if len(page.Messages) != 1 {
		t.Errorf("messages carries %d rows, want 1 (the persisted prompt): the live turn must "+
			"ride its own field rather than being spliced into the window", len(page.Messages))
	}
}

// openTurnFrom opens a turn of the given source on chatID and returns its buffer, so a
// test can choose what the turn then produces — including nothing.
func openTurnFrom(t *testing.T, rt *Runtime, id vibekit.ChatID, src vibekit.TurnOpenSource) *buffer.Buffer {
	t.Helper()
	rt.bridge.mgr.orInsert(id)
	if epoch := rt.coord.StartTurn(t.Context(), id, src); epoch == 0 {
		t.Fatalf("StartTurn(%q, source %v) refused, so there is no open turn to read", id, src)
	}
	buf := rt.liveTurnBuffer(id)
	if buf == nil {
		t.Fatalf("no live turn buffer for %q", id)
	}
	if opened, _ := buf.StartTurn("m-" + string(id)); !opened {
		t.Fatalf("turn for %q was already started, so the fixture is not the one filling it", id)
	}
	return buf
}

// TestLiveTurn_WithholdsATurnThatHasProducedNothing is the boundary between "a turn is
// running" and "there is a reply to hand back". `turn_open` already carries the first, and
// a carrier naming an EMPTY message is worse than none: the client adopts that id as its
// unpersisted live turn and mounts a blank assistant row under the prompt.
func TestLiveTurn_WithholdsATurnThatHasProducedNothing(t *testing.T) {
	rt, _, _ := newReplayGapRuntime(t)
	const quiet vibekit.ChatID = "c-quiet"
	openTurnFrom(t, rt, quiet, vibekit.TurnSourcePrompt)

	if !rt.TurnOpenState(quiet).Open {
		t.Fatalf("the fixture's turn is not open, so nothing below measures the empty case")
	}
	if live, ok := rt.LiveTurn(quiet); ok {
		t.Errorf("LiveTurn served a turn that has produced nothing (message id %q), want it "+
			"withheld until there is content to describe", live.Message.ID)
	}
}

// TestLiveTurn_WithholdsAnIdleChat is the other direction, and it is what keeps the
// snapshot from being served after the turn has been taken: `SnapshotCapped` reads a
// buffer the finalize has drained, so an idle chat must answer no.
func TestLiveTurn_WithholdsAnIdleChat(t *testing.T) {
	rt, _, _ := newReplayGapRuntime(t)

	if _, ok := rt.LiveTurn("c-never-had-a-turn"); ok {
		t.Errorf("LiveTurn served a chat with no open turn, want it withheld")
	}
}

// TestRuntimeLiveTurn_CarriesTheBase is the GET channel's COPY, which is the one fact
// bridge_coord's LiveTurn can get wrong on its own — and it needs its own test because
// this channel's base is almost always 0 in production, so nothing else would exercise
// the field's journey onto vibekit.LiveTurn at all.
func TestRuntimeLiveTurn_CarriesTheBase(t *testing.T) {
	rt, _, _ := newReplayGapRuntime(t)
	const cut vibekit.ChatID = "c-get-cut"
	buf := openTurnFrom(t, rt, cut, vibekit.TurnSourcePrompt)
	// The caps test's own fixture, shared rather than re-sized here: it is built to exceed
	// liveTurnGETCaps.BlockTextBytes, the one dimension that cuts this channel's block
	// array at a size worth building.
	fillCuttingTurn(t, buf)

	snap, ok := buf.SnapshotCapped(liveTurnGETCaps)
	if !ok {
		t.Fatal("the fixture's own snapshot reported no content")
	}
	if snap.BlockBase <= 0 {
		t.Fatalf("the fixture's snapshot reports BlockBase = %d, want > 0: this channel's caps cut "+
			"nothing here, so the comparison below would hold with the field carried nowhere",
			snap.BlockBase)
	}

	live, ok := rt.LiveTurn(cut)
	if !ok {
		t.Fatal("LiveTurn withheld a turn with content, so there is no payload to check")
	}
	if live.BlockBase != snap.BlockBase {
		t.Errorf("LiveTurn.BlockBase = %d, want %d: the read is right and the COPY onto the payload "+
			"is what drops it, which is the one thing this channel can get wrong on its own",
			live.BlockBase, snap.BlockBase)
	}
}
