package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// fatMessage builds an assistant message whose tool call carries outputBytes of
// output — the shape that made a six-message chat answer 13,010,641 bytes.
func fatMessage(id string, outputBytes int) vibekit.Message {
	return vibekit.Message{
		ID:   id,
		Role: vibekit.RoleAssistant,
		Ts:   100,
		ToolCalls: []vibekit.ToolCall{{
			ID:     id + "-tc",
			Title:  "Execute",
			Kind:   vibekit.ToolKindExecute,
			Status: vibekit.ToolCompleted,
			Output: strings.Repeat("x", outputBytes),
		}},
	}
}

// blockyMessage builds an assistant message carrying blocks blocks and nothing
// else large — the shape a BYTE budget cannot see, since a text block of a few
// words costs the wire almost nothing and the client's paint one whole row.
func blockyMessage(id string, blocks int) vibekit.Message {
	bs := make([]vibekit.Block, blocks)
	for i := range bs {
		bs[i] = vibekit.Block{Type: vibekit.BlockText, Text: "x"}
	}
	return vibekit.Message{ID: id, Role: vibekit.RoleAssistant, Ts: 100, Blocks: bs}
}

// toolyMessage builds the v3 shape of a tool-heavy assistant turn: one tool call
// and its tool_use block per call, which is what `store.ts` holds. Its cost is
// equal in both residency units, so it is the shape that shows the CLIENT'S two
// budgets diverging — 320 blocks admits ~320 tool cards against a client that
// mounts 96.
func toolyMessage(id string, calls int) vibekit.Message {
	m := vibekit.Message{
		ID:        id,
		Role:      vibekit.RoleAssistant,
		Ts:        100,
		ToolCalls: make([]vibekit.ToolCall, calls),
		Blocks:    make([]vibekit.Block, calls),
	}
	for i := range calls {
		tcID := fmt.Sprintf("%s-tc%d", id, i)
		m.ToolCalls[i] = vibekit.ToolCall{
			ID: tcID, Title: "Execute", Kind: vibekit.ToolKindExecute,
			Status: vibekit.ToolCompleted, Output: "x",
		}
		m.Blocks[i] = vibekit.Block{Type: vibekit.BlockToolUse, ToolCallID: tcID}
	}
	return m
}

// wordyMessage builds an assistant message carrying contentBytes of prose. The
// shape a fixture sized against maxMaxBytes needs: previewMessage bounds a tool
// call's output to 8 KiB, and leaves `content` whole.
func wordyMessage(id string, contentBytes int) vibekit.Message {
	return vibekit.Message{
		ID:      id,
		Role:    vibekit.RoleAssistant,
		Ts:      100,
		Content: strings.Repeat("x", contentBytes),
	}
}

// nothingMessage builds the assistant message that reaches the transcript with
// nothing in it: carriesNothing is true of it, so it is in no turn at all.
func nothingMessage(id string) vibekit.Message {
	return vibekit.Message{ID: id, Role: vibekit.RoleAssistant, Ts: 100}
}

// windowPage is the part of a single-chat GET these tests read: the window plus
// the three fields describing its left edge.
type windowPage struct {
	Messages          []vibekit.Message `json:"messages"`
	HasMore           bool              `json:"has_more"`
	TurnOffset        int               `json:"turn_offset"`
	TurnSegmentClosed bool              `json:"turn_segment_closed"`
}

// servePage runs GET /api/chats/c1<query> against store s holding msgs.
func servePage(t *testing.T, s *Store, msgs []vibekit.Message, query string) (page windowPage, ids []string, bodyLen int) {
	t.Helper()
	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = msgs
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+query, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids = make([]string, len(page.Messages))
	for i, m := range page.Messages {
		ids[i] = m.ID
	}
	return page, ids, rec.Body.Len()
}

// serveOne runs GET /api/chats/c1<query> against a store holding msgs and
// returns the decoded window plus the raw body length.
func serveOne(t *testing.T, msgs []vibekit.Message, query string) (ids []string, hasMore bool, bodyLen int) {
	t.Helper()
	s, _ := newTestStore(t)
	page, ids, bodyLen := servePage(t, s, msgs, query)
	return ids, page.HasMore, bodyLen
}

func TestHandleOne_ByteBudgetCutsAtAMessageBoundary(t *testing.T) {
	// Four turns whose replies carry ~4 KiB each against a 10 KiB budget: two fit,
	// the third would overrun. The cut is at the boundary, so the answer is the two
	// newest turns, and has_more names the two the client does not have. turns=1 is
	// what leaves the byte ceiling as the thing that cuts.
	msgs := []vibekit.Message{
		user("ua", "a", 100), fatMessage("a", 4096),
		user("ub", "b", 100), fatMessage("b", 4096),
		user("uc", "c", 100), fatMessage("c", 4096),
		user("ud", "d", 100), fatMessage("d", 4096),
	}

	ids, hasMore, bodyLen := serveOne(t, msgs, "?max_bytes=10240&turns=1")

	if want := []string{"uc", "c", "ud", "d"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: two older messages were not served")
	}
	// The point of the budget: the response is bounded. Header and envelope ride
	// along, so the check is against the budget plus a generous allowance rather
	// than against the budget exactly.
	if bodyLen > 10240+4096 {
		t.Errorf("body = %d bytes, want it bounded near the 10240-byte budget", bodyLen)
	}
}

func TestHandleOne_ByteBudgetLetsOneOversizeMessageThroughWhole(t *testing.T) {
	// A single message bigger than the whole budget. It must be served: the
	// envelope is the reconcile unit, so there is no honest half-message, and a
	// budget that could answer nothing would make the newest message of a big
	// chat unreachable.
	msgs := []vibekit.Message{
		user("ua", "a", 100), fatMessage("a", 64),
		user("ub", "big", 100), fatMessage("big", 200_000),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?max_bytes=1024&turns=1")

	if want := []string{"ub", "big"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v: the newest message goes through whole, and the floor "+
			"keeps the prompt that opened it", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: the older turn was not served")
	}
}

func TestHandleOne_HasMoreIsHonestAgainstTheBytes(t *testing.T) {
	// The defect the budget replaces: has_more used to describe only the message
	// COUNT, so a response that dropped nothing by count and everything by size
	// still said false. Same three turns, two budgets, two honest answers.
	msgs := []vibekit.Message{
		user("ua", "a", 100), fatMessage("a", 4096),
		user("ub", "b", 100), fatMessage("b", 4096),
		user("uc", "c", 100), fatMessage("c", 4096),
	}

	all, allMore, _ := serveOne(t, msgs, "?max_bytes=1048576")
	if len(all) != 6 {
		t.Errorf("a 1 MiB budget served %d of 6 messages, want all of them", len(all))
	}
	if allMore {
		t.Error("has_more = true with every message served, want false")
	}

	_, someMore, _ := serveOne(t, msgs, "?max_bytes=10240&turns=1")
	if !someMore {
		t.Error("has_more = false with a 10 KiB budget over ~12 KiB of messages, want true")
	}
}

func TestHandleOne_ByteBudgetComposesWithLimitAndBeforeID(t *testing.T) {
	// Both budgets apply, and the cursor still bounds the top of the window.
	msgs := make([]vibekit.Message, 6)
	for i := range msgs {
		msgs[i] = fatMessage(string(rune('a'+i)), 64)
	}

	ids, hasMore, _ := serveOne(t, msgs, "?before_id=f&limit=2&max_bytes=1048576")

	if want := []string{"d", "e"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v — limit still caps a window the bytes would allow", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true")
	}
}

func TestHandleOne_EmptyWindowIsAnArrayNotNull(t *testing.T) {
	// The generated decoder rejects `null` for an array, so an empty window has
	// to marshal as []. Same guard the make+copy this replaced provided.
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{fatMessage("a", 8)}
		return true
	})
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1?before_id=a", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if !strings.Contains(rec.Body.String(), `"messages":[]`) {
		t.Errorf("body does not carry `\"messages\":[]`: %s", rec.Body.String())
	}
}

// The item: the page the server cuts and the window the client can hold are the
// same unit. `block-window.ts` bounds residency in BLOCKS, so a page bounded only
// in bytes holds a chat-dependent number of them and the surplus is fetched,
// decoded and then stubbed on arrival.
//
// The byte budget is deliberately generous here, so a byte-only cut cannot
// produce this answer and the assertion is about the block budget alone.
func TestHandleOne_BlockBudgetCutsAtAMessageBoundary(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), blockyMessage("a", 40),
		user("ub", "b", 100), blockyMessage("b", 40),
		user("uc", "c", 100), blockyMessage("c", 40),
		user("ud", "d", 100), blockyMessage("d", 40),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?blocks=100&max_bytes=8388608&turns=1")

	if want := []string{"uc", "c", "ud", "d"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v: 82 blocks fit a 100-block budget and 122 do not",
			ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: two older messages were not served")
	}
}

// The same rule the byte budget follows: the newest message goes through whole
// however many blocks it carries, or the newest message of a block-heavy chat
// would be unreachable. One measured assistant message carries 580 blocks.
func TestHandleOne_BlockBudgetLetsOneOversizeMessageThroughWhole(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), blockyMessage("a", 2),
		user("ub", "big", 100), blockyMessage("big", 600),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?blocks=100&turns=1")

	if want := []string{"ub", "big"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v: the newest message goes through whole", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: the older turn was not served")
	}
}

// has_more answers against whichever budget cut the page, so a client that reads
// it can still reach everything the block budget held back.
func TestHandleOne_HasMoreIsHonestAgainstTheBlocks(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), blockyMessage("a", 40),
		user("ub", "b", 100), blockyMessage("b", 40),
		user("uc", "c", 100), blockyMessage("c", 40),
	}

	all, allMore, _ := serveOne(t, msgs, "?blocks=8192&max_bytes=8388608")
	if len(all) != 6 {
		t.Errorf("a 8192-block budget served %d of 6 messages, want all of them", len(all))
	}
	if allMore {
		t.Error("has_more = true with every message served, want false")
	}

	_, someMore, _ := serveOne(t, msgs, "?blocks=100&max_bytes=8388608&turns=1")
	if !someMore {
		t.Error("has_more = false with a 100-block budget over 123 blocks, want true")
	}
}

// The client's residency budget is a PAIR, so the page has to be measured in both
// halves. `planResidency` stops on RESIDENT_BLOCKS *or* RESIDENT_TOOL_CALLS,
// whichever runs out first, and 320 blocks admits on the order of 320 tool cards
// against a client that mounts 96 — so a tool-heavy transcript cut on blocks alone
// still overshoots by ~3x and the surplus is fetched, decoded and stubbed.
//
// The block and byte budgets are deliberately generous here, so neither could
// produce this answer and the assertion is about the tool-call budget alone.
func TestHandleOne_ToolCallBudgetCutsAtAMessageBoundary(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), toolyMessage("a", 40),
		user("ub", "b", 100), toolyMessage("b", 40),
		user("uc", "c", 100), toolyMessage("c", 40),
		user("ud", "d", 100), toolyMessage("d", 40),
	}

	ids, hasMore, _ := serveOne(t, msgs,
		"?tool_calls=100&blocks=8192&max_bytes=8388608&turns=1")

	if want := []string{"uc", "c", "ud", "d"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v: 80 tool calls fit a 100-call budget and 120 do not",
			ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: two older messages were not served")
	}
}

// The same rule the other two budgets follow: the newest message goes through
// whole however many tool calls it carries. One measured assistant message carries
// 353 of them, so a budget that could answer nothing would make the newest message
// of a tool-heavy chat unreachable.
func TestHandleOne_ToolCallBudgetLetsOneOversizeMessageThroughWhole(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), toolyMessage("a", 2),
		user("ub", "big", 100), toolyMessage("big", 400),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?tool_calls=96&turns=1")

	if want := []string{"ub", "big"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v: the newest message goes through whole", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: the older turn was not served")
	}
}

// A caller that names a block budget and no tool-call budget gets the answer the
// block budget alone gives, which is what the tool-call DEFAULT is chosen for: it
// is the block default, and every tool call the client synthesizes a block for
// costs a block too, so the default cannot cut a page the blocks admitted.
func TestHandleOne_TheDefaultToolCallBudgetCutsNothingTheBlocksAllow(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), toolyMessage("a", 80),
		user("ub", "b", 100), toolyMessage("b", 80),
		user("uc", "c", 100), toolyMessage("c", 80),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?blocks=8192&max_bytes=8388608&turns=1")

	if len(ids) != 6 {
		t.Errorf("served %d of 6 messages (240 tool calls), want all of them: %v", len(ids), ids)
	}
	if hasMore {
		t.Error("has_more = true with every message served, want false")
	}
}

// costOfMessage mirrors `block-window.ts turnCost`, and it has to: a budget
// measured one way on the server and another on the client cuts a page the client
// still stubs. The legacy row is the one that matters for blocks — a message
// persisted before the blocks field carries none, and the client SYNTHESIZES them
// from the content, the reasoning and one per tool call before it measures — and
// the synthesis is gated on the ASSISTANT role, which every other role misses.
func TestCostOfMessage_MirrorsTheClientsAccounting(t *testing.T) {
	tests := map[string]struct {
		msg  vibekit.Message
		want messageCost
	}{
		"a message's own blocks are what it costs": {
			msg:  blockyMessage("a", 7),
			want: messageCost{Blocks: 7},
		},
		"an empty message still costs one row": {
			msg:  vibekit.Message{ID: "a", Role: vibekit.RoleAssistant},
			want: messageCost{Blocks: 1},
		},
		"a legacy message costs its synthesized blocks, not one": {
			msg: vibekit.Message{
				ID: "a", Role: vibekit.RoleAssistant,
				Content:   "hello",
				Reasoning: "thinking",
				ToolCalls: []vibekit.ToolCall{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}},
			},
			want: messageCost{Blocks: 5, ToolCalls: 3},
		},
		"a legacy tool-only message costs one per tool call": {
			msg: vibekit.Message{
				ID: "a", Role: vibekit.RoleAssistant,
				ToolCalls: []vibekit.ToolCall{{ID: "t1"}, {ID: "t2"}},
			},
			want: messageCost{Blocks: 2, ToolCalls: 2},
		},
		"blocks present win over the synthesis": {
			msg: vibekit.Message{
				ID: "a", Role: vibekit.RoleAssistant,
				Content:   "hello",
				Blocks:    []vibekit.Block{{Type: vibekit.BlockText, Text: "hello"}},
				ToolCalls: []vibekit.ToolCall{{ID: "t1"}, {ID: "t2"}},
			},
			want: messageCost{Blocks: 1, ToolCalls: 2},
		},
		"a blockless NON-assistant message costs one row whatever it carries": {
			// normalizeMessage returns any non-assistant message untouched, so the
			// client leaves its blocks array empty and turnCost charges max(1, 0).
			// Synthesizing here would price it at 4 and cut a page the client holds.
			msg: vibekit.Message{
				ID: "a", Role: vibekit.RoleUser,
				Content:   "hello",
				Reasoning: "thinking",
				ToolCalls: []vibekit.ToolCall{{ID: "t1"}, {ID: "t2"}},
			},
			want: messageCost{Blocks: 1, ToolCalls: 2},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := costOfMessage(&tc.msg); got != tc.want {
				t.Errorf("costOfMessage(%+v) = %+v, want %+v", tc.msg, got, tc.want)
			}
		})
	}
}

// A paged client cannot number its turns: its own projection starts at the window,
// so turn 1 there is only turn 1 of the session when nothing was cut. The window
// response therefore carries the segmentation state at its LEFT EDGE, and this
// pins the handler's half of that — `turnWindowBase`'s own rule is the shared
// fixture's (testdata/turn_windows.json, TestTurnWindowBaseContract).
//
// The block budget is what cuts each window here, so the three cases are three
// genuinely different left edges rather than three spellings of one.
func TestHandleOne_ServesTheWindowBase(t *testing.T) {
	settled := func(id string, blocks int) vibekit.Message {
		m := blockyMessage(id, blocks)
		m.TurnOutcome = vibekit.TurnOutcomeCompleted
		return m
	}
	// Three turns, each a prompt plus a settled 40-block reply.
	msgs := []vibekit.Message{
		user("u1", "a", 100), settled("a1", 40),
		user("u2", "b", 200), settled("a2", 40),
		user("u3", "c", 300), blockyMessage("a3", 40),
	}

	tests := []struct {
		name         string
		query        string
		wantIDs      []string
		wantMore     bool
		wantOffset   int
		wantClosed   bool
		whyTheEdgeIs string
	}{
		{
			name:         "the whole conversation fits, so the window IS the session",
			query:        "?blocks=8192&max_bytes=8388608",
			wantIDs:      []string{"u1", "a1", "u2", "a2", "u3", "a3"},
			wantMore:     false,
			wantOffset:   0,
			wantClosed:   false,
			whyTheEdgeIs: "nothing precedes turn 1 and no segment closed before it",
		},
		{
			name:         "cut on a PROMPT, so the window's first turn opens inside it",
			query:        "?blocks=100&max_bytes=8388608&turns=1",
			wantIDs:      []string{"u2", "a2", "u3", "a3"},
			wantMore:     true,
			wantOffset:   1,
			wantClosed:   true,
			whyTheEdgeIs: "turn 1 precedes the window and its reply settled, so the segment had closed",
		},
		{
			name: "a ceiling tighter than one turn still cuts on a PROMPT",
			// 40 blocks admits a3 and nothing beside it, and the floor is what keeps
			// its own prompt: the window opens on u3 rather than inside turn 3.
			query:        "?blocks=40&max_bytes=8388608&turns=1",
			wantIDs:      []string{"u3", "a3"},
			wantMore:     true,
			wantOffset:   2,
			wantClosed:   true,
			whyTheEdgeIs: "u3 opens turn 3, so two turns precede it and turn 2's reply settled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "A"
				c.Messages = msgs
				return true
			})
			req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Messages          []vibekit.Message `json:"messages"`
				HasMore           bool              `json:"has_more"`
				TurnOffset        int               `json:"turn_offset"`
				TurnSegmentClosed bool              `json:"turn_segment_closed"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			ids := make([]string, len(got.Messages))
			for i, m := range got.Messages {
				ids[i] = m.ID
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Fatalf("window = %v, want %v — the budget cut somewhere else, so the "+
					"left edge this case is about is not the one served", ids, tc.wantIDs)
			}
			if got.HasMore != tc.wantMore {
				t.Errorf("has_more = %v, want %v", got.HasMore, tc.wantMore)
			}
			if got.TurnOffset != tc.wantOffset {
				t.Errorf("turn_offset = %d, want %d (%s)", got.TurnOffset, tc.wantOffset, tc.whyTheEdgeIs)
			}
			if got.TurnSegmentClosed != tc.wantClosed {
				t.Errorf("turn_segment_closed = %v, want %v (%s)",
					got.TurnSegmentClosed, tc.wantClosed, tc.whyTheEdgeIs)
			}
		})
	}
}

// The property no other case in this file asserts: a window's LEFT EDGE is a turn
// boundary. Every ceiling below is tight enough that a message-granular cut would
// land inside the newest turn, and the floor is what keeps the edge out of it.
func TestHandleOne_WindowDeliversWholeTurns(t *testing.T) {
	msgs := []vibekit.Message{
		user("ua", "a", 100), toolyMessage("a", 40),
		user("ub", "b", 100), toolyMessage("b", 40),
		user("uc", "c", 100), toolyMessage("c", 40),
		user("ud", "d", 100), toolyMessage("d", 40),
	}
	prompts := []string{"ua", "ub", "uc", "ud"}

	tests := []struct {
		name     string
		query    string
		wantIDs  []string
		wantMore bool
	}{
		{
			name:     "the default floor carries three whole turns past every ceiling",
			query:    "?max_bytes=1024",
			wantIDs:  []string{"ub", "b", "uc", "c", "ud", "d"},
			wantMore: true,
		},
		{
			name:     "a floor of one carries the newest turn whole",
			query:    "?max_bytes=1024&turns=1",
			wantIDs:  []string{"ud", "d"},
			wantMore: true,
		},
		{
			name:     "the block ceiling cuts on a prompt",
			query:    "?blocks=1&max_bytes=8388608&turns=1",
			wantIDs:  []string{"ud", "d"},
			wantMore: true,
		},
		{
			// The shape the ceilings above cannot reach, because each of them breaches
			// exactly at a prompt: here the floor is already met when the breach lands
			// and `start` is an assistant row, so only the boundary half of the gate
			// refuses. 81 blocks admits d(40) + ud(1) + c(40) and breaches on uc(1), so
			// a cut that asked about the count alone would open the window on c.
			name:     "a breach one message past a prompt walks back to that turn's prompt",
			query:    "?blocks=81&max_bytes=8388608&turns=1",
			wantIDs:  []string{"uc", "c", "ud", "d"},
			wantMore: true,
		},
		{
			name:     "the tool-call ceiling cuts on a prompt",
			query:    "?tool_calls=1&blocks=8192&max_bytes=8388608&turns=1",
			wantIDs:  []string{"ud", "d"},
			wantMore: true,
		},
		{
			name:     "a floor past the turns the chat holds serves every one of them",
			query:    "?max_bytes=1024&turns=50",
			wantIDs:  []string{"ua", "a", "ub", "b", "uc", "c", "ud", "d"},
			wantMore: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids, hasMore, _ := serveOne(t, msgs, tc.query)

			if !slices.Equal(ids, tc.wantIDs) {
				t.Fatalf("serveOne(%q) = %v, want %v", tc.query, ids, tc.wantIDs)
			}
			if !slices.Contains(prompts, ids[0]) {
				t.Errorf("serveOne(%q) opens on %q, want a prompt: a window opening mid-turn "+
					"gives the reader a first card with no request band", tc.query, ids[0])
			}
			if hasMore != tc.wantMore {
				t.Errorf("serveOne(%q) has_more = %v, want %v", tc.query, hasMore, tc.wantMore)
			}
		})
	}
}

// The population a prompt-counting floor left unbounded: a transcript no PROMPT
// opens. Each reply here follows a settled one, so each opens a headerless turn,
// and the ceilings have to bound the page as they do for a prompted chat.
func TestHandleOne_APromptlessTranscriptIsCutByTheBytes(t *testing.T) {
	settled := func(id string) vibekit.Message {
		m := fatMessage(id, 4096)
		m.TurnOutcome = vibekit.TurnOutcomeCompleted
		return m
	}
	msgs := []vibekit.Message{settled("a"), settled("b"), settled("c"), settled("d")}

	ids, hasMore, bodyLen := serveOne(t, msgs, "?max_bytes=10240&turns=1")

	if want := []string{"c", "d"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v: a chat holding no prompt is still bounded by max_bytes",
			ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: two older messages were not served")
	}
	if bodyLen > 10240+4096 {
		t.Errorf("body = %d bytes, want it bounded near the 10240-byte budget", bodyLen)
	}
}

// The floor is a floor on what the transcript can OFFER. A chat holding fewer turns
// than the floor asks for keeps every one of them, and the ceilings still cut once
// the turns it does hold are served.
func TestHandleOne_TheFloorIsBoundedByTheTurnsTheChatOffers(t *testing.T) {
	twoTurns := []vibekit.Message{
		user("ua", "a", 100), fatMessage("a", 4096),
		user("ub", "b", 100), fatMessage("b", 4096),
	}
	// Two messages that render nothing precede the first turn, so the floor this chat
	// can meet is reached while older messages are still unserved.
	leading := append([]vibekit.Message{nothingMessage("n0"), nothingMessage("n1")}, twoTurns...)
	rendersNothing := []vibekit.Message{
		nothingMessage("n0"), nothingMessage("n1"),
		nothingMessage("n2"), nothingMessage("n3"),
	}

	tests := []struct {
		name     string
		msgs     []vibekit.Message
		query    string
		wantIDs  []string
		wantMore bool
	}{
		{
			name:     "a chat holding fewer turns than the floor is served whole",
			msgs:     twoTurns,
			query:    "?max_bytes=1024",
			wantIDs:  []string{"ua", "a", "ub", "b"},
			wantMore: false,
		},
		{
			name:     "the floor a short chat can meet is its own turn count, so a ceiling still cuts",
			msgs:     leading,
			query:    "?max_bytes=1024",
			wantIDs:  []string{"ua", "a", "ub", "b"},
			wantMore: true,
		},
		{
			name:     "a transcript that opens no turn at all is bounded by its ceilings",
			msgs:     rendersNothing,
			query:    "?blocks=1&max_bytes=8388608",
			wantIDs:  []string{"n3"},
			wantMore: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids, hasMore, _ := serveOne(t, tc.msgs, tc.query)

			if !slices.Equal(ids, tc.wantIDs) {
				t.Fatalf("serveOne(%q) = %v, want %v", tc.query, ids, tc.wantIDs)
			}
			if hasMore != tc.wantMore {
				t.Errorf("serveOne(%q) has_more = %v, want %v", tc.query, hasMore, tc.wantMore)
			}
		})
	}
}

// The RUNAWAY stop the floor may not override. The floor outranks every caller ceiling
// including ?max_bytes=, so maxWholeTurnBytes is the one bound left that keeps an
// unconditional whole-turn guarantee from being an unbounded response. It may cut
// mid-turn, and a bounded response outranks a perfect left edge.
//
// Derived from maxWholeTurnBytes rather than maxMaxBytes: the latter is the CALLER's
// ceiling, which the guarantee now beats, so deriving from it would assert the contract
// this change reverses.
func TestHandleOne_TheHardByteStopBoundsEveryFloor(t *testing.T) {
	// Two of these fit under the stop and three do not, whatever maxWholeTurnBytes is.
	fat := maxWholeTurnBytes/3 + 1<<20

	t.Run("the floor cannot carry a page past the stop", func(t *testing.T) {
		msgs := []vibekit.Message{
			user("ua", "a", 100), wordyMessage("a", fat),
			user("ub", "b", 100), wordyMessage("b", fat),
			user("uc", "c", 100), wordyMessage("c", fat),
		}

		// No query, so max_bytes sits at its default and the default floor of three
		// turns is what would carry this page past the stop.
		page, ids, bodyLen := servePage(t, newCappedTestStore(t, 0), msgs, "")

		if want := []string{"ub", "b", "uc", "c"}; !slices.Equal(ids, want) {
			t.Fatalf("ids = %v, want %v: the stop cut the third turn the floor asked for",
				ids, want)
		}
		if !page.HasMore {
			t.Error("has_more = false, want true: the oldest turn was not served")
		}
		if bodyLen > maxWholeTurnBytes {
			t.Errorf("body = %d bytes, want at most maxWholeTurnBytes = %d", bodyLen, maxWholeTurnBytes)
		}
	})

	t.Run("one oversize newest message still goes through whole", func(t *testing.T) {
		big := maxWholeTurnBytes + 1<<20
		msgs := []vibekit.Message{
			user("ua", "a", 100), wordyMessage("a", 64),
			user("ub", "b", 100), wordyMessage("big", big),
		}

		page, ids, bodyLen := servePage(t, newCappedTestStore(t, 0), msgs, "")

		if want := []string{"big"}; !slices.Equal(ids, want) {
			t.Fatalf("ids = %v, want %v: the stop bounds what accumulates past the newest "+
				"message, never the newest message itself", ids, want)
		}
		if bodyLen < big {
			t.Errorf("body = %d bytes, want at least the %d-byte message served whole",
				bodyLen, big)
		}
		if !page.HasMore {
			t.Error("has_more = false, want true: the older turn was not served")
		}
		// The mid-turn cut the stop is allowed to make, read where a client sees it:
		// the offset names turn 2 while the window holds only its reply.
		if page.TurnOffset != 1 {
			t.Errorf("turn_offset = %d, want 1: the stop cut inside turn 2, so the "+
				"ordinal names a turn the window holds partially", page.TurnOffset)
		}
	})
}

// The window's left edge and the turn_offset the same response publishes are a
// boundary in one unit, so a turn the AGENT opened is served whole like any other
// and the ordinal addresses a turn the window holds whole.
func TestHandleOne_AHeaderlessTurnIsServedWhole(t *testing.T) {
	settled := func(m vibekit.Message) vibekit.Message {
		m.TurnOutcome = vibekit.TurnOutcomeCompleted
		return m
	}
	// Turn 1 settles, so h1 opens a headerless turn 2 that h2 and h3 continue: the
	// byte ceiling below breaches inside it.
	msgs := []vibekit.Message{
		user("u1", "a", 100), settled(fatMessage("a1", 4096)),
		fatMessage("h1", 4096), fatMessage("h2", 4096), settled(fatMessage("h3", 4096)),
	}

	s, _ := newTestStore(t)
	page, ids, _ := servePage(t, s, msgs, "?max_bytes=10240&turns=1")

	if want := []string{"h1", "h2", "h3"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v: the window opens on the turn's own first message", ids, want)
	}
	if !page.HasMore {
		t.Error("has_more = false, want true: turn 1 was not served")
	}
	if page.TurnOffset != 1 {
		t.Errorf("turn_offset = %d, want 1: one turn precedes h1, and the window holds "+
			"the turn h1 opens whole", page.TurnOffset)
	}
	if !page.TurnSegmentClosed {
		t.Error("turn_segment_closed = false, want true: turn 1's reply settled")
	}
}

// A row that renders nothing belongs to no turn, so a window opening on one is
// resolved from the first row that DOES render. A ceiling may still cut there
// rather than walking back into the turn before it.
func TestHandleOne_ALeftEdgeOnARowThatRendersNothingResolvesToItsTurn(t *testing.T) {
	msgs := []vibekit.Message{
		user("u1", "a", 100), fatMessage("a1", 4096),
		nothingMessage("n"),
		user("u2", "b", 100), fatMessage("a2", 4096),
	}

	s, _ := newTestStore(t)
	page, ids, _ := servePage(t, s, msgs, "?max_bytes=5120&turns=1")

	if want := []string{"n", "u2", "a2"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v: the cut is admissible where the row that renders "+
			"opens a turn", ids, want)
	}
	if !page.HasMore {
		t.Error("has_more = false, want true: turn 1 was not served")
	}
	if page.TurnOffset != 1 {
		t.Errorf("turn_offset = %d, want 1: the window's first turn is the one u2 opens",
			page.TurnOffset)
	}
}

// The contract behind turn_offset: wherever the walk may cut, the ordinal published
// for that edge counts exactly the turns PRECEDING it, so the window holds its first
// turn whole. Two traversals of one boundary rule, checked against each other.
func TestTurnOpeners_AgreeWithTheOrdinalAtEveryAdmissibleCut(t *testing.T) {
	settled := func(m vibekit.Message) vibekit.Message {
		m.TurnOutcome = vibekit.TurnOutcomeCompleted
		return m
	}
	event := func(id string, ts int64) vibekit.Message {
		return vibekit.Message{
			ID: id, Role: vibekit.RoleEvent, EventKind: vibekit.EventModelSwitched, Ts: ts,
		}
	}
	fixtures := map[string][]vibekit.Message{
		"prompted turns": {
			user("u1", "a", 100), assistant("a1", 200),
			user("u2", "b", 300), assistant("a2", 400),
		},
		"a headerless turn continued twice": {
			user("u1", "a", 100), settled(assistant("a1", 200)),
			assistant("h1", 300), assistant("h2", 400), settled(assistant("h3", 500)),
		},
		"an agent-initiated transcript with no prompt at all": {
			settled(assistant("a1", 100)), settled(assistant("a2", 200)),
			settled(assistant("a3", 300)),
		},
		"rows that render nothing lead and interleave": {
			nothingMessage("n0"), nothingMessage("n1"),
			user("u1", "a", 100), assistant("a1", 200), nothingMessage("n2"),
			user("u2", "b", 300), assistant("a2", 400),
		},
		"a leading reply that never settles": {
			assistant("h1", 100), assistant("h2", 200),
			user("u1", "a", 300), assistant("a1", 400),
		},
		// An event row opens nothing and settles nothing, so the segment the reply
		// before it closed is still closed when h1 arrives and opens a turn.
		"an event row between a settled reply and an agent-initiated one": {
			user("u1", "a", 100), settled(assistant("a1", 200)),
			event("e1", 300), assistant("h1", 400),
			user("u2", "b", 500), assistant("a2", 600),
		},
		"nothing renders at all": {
			nothingMessage("n0"), nothingMessage("n1"), nothingMessage("n2"),
		},
	}
	for name, msgs := range fixtures {
		t.Run(name, func(t *testing.T) {
			openers := findTurnOpeners(msgs)
			preceding := 0
			for start := range msgs {
				if openers.admitCutAt(msgs, start) {
					offset, _ := turnWindowBase(msgs, start)
					if offset != preceding {
						t.Errorf("a window cut at %d (%q) publishes turn_offset %d, but %d "+
							"turn(s) open before it", start, msgs[start].ID, offset, preceding)
					}
				}
				if openers.opens[start] {
					preceding++
				}
			}
		})
	}
}

func TestParseBlocksParam_HonoursTheInclusiveRange(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent", query: "", want: defaultMaxBlocks},
		{name: "smallest_accepted", query: "?blocks=1", want: 1},
		{name: "the client's own residency budget", query: "?blocks=320", want: 320},
		{name: "largest_accepted", query: "?blocks=8192", want: maxMaxBlocks},
		{name: "one_past_the_largest", query: "?blocks=8193", want: defaultMaxBlocks},
		{name: "zero", query: "?blocks=0", want: defaultMaxBlocks},
		{name: "negative", query: "?blocks=-1", want: defaultMaxBlocks},
		{name: "not_a_number", query: "?blocks=lots", want: defaultMaxBlocks},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			if got := parseBlocksParam(r); got != tc.want {
				t.Errorf("parseBlocksParam(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

func TestParseToolCallsParam_HonoursTheInclusiveRange(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent", query: "", want: defaultMaxBlocks},
		// Zero is IN range, unlike the block floor: a page of pure prose costs no
		// tool calls, so a client that mounts no tool cards can honestly ask for 0.
		{name: "zero", query: "?tool_calls=0", want: 0},
		{name: "the client's own residency budget", query: "?tool_calls=96", want: 96},
		{name: "largest_accepted", query: "?tool_calls=8192", want: maxMaxBlocks},
		{name: "one_past_the_largest", query: "?tool_calls=8193", want: defaultMaxBlocks},
		{name: "negative", query: "?tool_calls=-1", want: defaultMaxBlocks},
		{name: "not_a_number", query: "?tool_calls=lots", want: defaultMaxBlocks},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			if got := parseToolCallsParam(r); got != tc.want {
				t.Errorf("parseToolCallsParam(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

func TestParseTurnsParam_HonoursTheInclusiveRange(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent", query: "", want: defaultWindowTurns},
		// One is the floor rather than zero: a window that may end mid-turn opens on
		// a turn it can only continue, and no caller wants that.
		{name: "smallest_accepted", query: "?turns=1", want: 1},
		{name: "largest_accepted", query: "?turns=50", want: 50},
		{name: "one_past_the_largest", query: "?turns=51", want: defaultWindowTurns},
		{name: "zero", query: "?turns=0", want: defaultWindowTurns},
		{name: "negative", query: "?turns=-1", want: defaultWindowTurns},
		{name: "not_a_number", query: "?turns=lots", want: defaultWindowTurns},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			if got := parseTurnsParam(r); got != tc.want {
				t.Errorf("parseTurnsParam(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

func TestParseMaxBytesParam_HonoursTheInclusiveRange(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent", query: "", want: defaultMaxBytes},
		{name: "smallest_accepted", query: "?max_bytes=1024", want: 1024},
		{name: "largest_accepted", query: "?max_bytes=8388608", want: maxMaxBytes},
		{name: "one_below_the_smallest", query: "?max_bytes=1023", want: defaultMaxBytes},
		{name: "one_past_the_largest", query: "?max_bytes=8388609", want: defaultMaxBytes},
		{name: "zero", query: "?max_bytes=0", want: defaultMaxBytes},
		{name: "negative", query: "?max_bytes=-1", want: defaultMaxBytes},
		{name: "not_a_number", query: "?max_bytes=lots", want: defaultMaxBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			if got := parseMaxBytesParam(r); got != tc.want {
				t.Errorf("parseMaxBytesParam(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

// THE GUARANTEE, at the precedence it was built for: the newest turn is served WHOLE past
// every SIZE ceiling the caller can name, all three at once and all set to their floors.
// Before the guarantee only ?max_bytes= was overridden by the floor and the residency pair
// could still cut mid-turn, so a client asking for a narrow paint budget got a fragment of
// the reply it was about to render.
func TestHandleOne_TheNewestTurnIsServedWholePastEveryCallerCeiling(t *testing.T) {
	// One turn of five messages: a prompt, prose, a block-heavy row and two tool-heavy
	// ones, so each ceiling below has something of its own kind to refuse.
	msgs := []vibekit.Message{
		user("uold", "old", 100), fatMessage("old", 4096),
		user("unew", "new", 100),
		wordyMessage("n1", 8192),
		blockyMessage("n2", 64),
		toolyMessage("n3", 32),
		fatMessage("n4", 8192),
	}

	// Every size ceiling at or near its floor at once. turns=1 asks for the minimum the
	// floor can be, so nothing here is the floor being generous.
	page, ids, _ := serveOnePage(t, msgs, "?max_bytes=1024&blocks=1&tool_calls=0&turns=1")

	want := []string{"unew", "n1", "n2", "n3", "n4"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v: the newest turn is served whole past ?max_bytes=, ?blocks= "+
			"and ?tool_calls= alike, so a reader never sees a fragment of the reply they are "+
			"looking at", ids, want)
	}
	if !page.HasMore {
		t.Error("has_more = false, want true: the older turn was not served, and the client's " +
			"only route to it is has_more plus before_id")
	}
	if page.TurnOffset != 1 {
		t.Errorf("turn_offset = %d, want 1: one turn precedes the window, and it opens on a "+
			"boundary so the ordinal names a turn the window holds WHOLE", page.TurnOffset)
	}
}

// The case that NAMES the decision: the guarantee beats maxMaxBytes, the top of the
// ?max_bytes= range, because that is the CALLER's ceiling and the newest turn outranks it.
// Only maxWholeTurnBytes stops the floor, and this turn sits between the two.
func TestHandleOne_ANewestTurnOverTheCallerCeilingIsServedWhole(t *testing.T) {
	// Three messages of 3 MiB each: 9 MiB, over maxMaxBytes (8) and under
	// maxWholeTurnBytes (16), so the answer differs depending on which one stops the floor.
	const each = 3 << 20
	msgs := []vibekit.Message{
		user("uold", "old", 100), wordyMessage("old", 4096),
		user("unew", "new", 100),
		wordyMessage("n1", each), wordyMessage("n2", each), wordyMessage("n3", each),
	}

	// max_bytes at its own maximum, so the caller has asked for the largest window the
	// endpoint will serve and the turn still does not fit inside it.
	page, ids, bodyLen := servePage(t, newCappedTestStore(t, 0), msgs,
		"?max_bytes="+strconv.Itoa(maxMaxBytes)+"&turns=1")

	want := []string{"unew", "n1", "n2", "n3"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v: a turn over maxMaxBytes (%d) but under maxWholeTurnBytes "+
			"(%d) is served whole — the caller's ceiling is not the guarantee's ceiling",
			ids, want, maxMaxBytes, maxWholeTurnBytes)
	}
	if bodyLen <= maxMaxBytes {
		t.Errorf("body = %d bytes, want more than maxMaxBytes = %d: a body inside the caller's "+
			"ceiling means the turn was cut and this fixture proves nothing", bodyLen, maxMaxBytes)
	}
	if bodyLen > maxWholeTurnBytes {
		t.Errorf("body = %d bytes, want at most maxWholeTurnBytes = %d: the runaway stop still "+
			"bounds the response", bodyLen, maxWholeTurnBytes)
	}
	if !page.HasMore {
		t.Error("has_more = false, want true: the older turn was not served")
	}
}

// THE ONE RESIDUAL, pinned rather than left latent: ?limit= is the caller's statement about
// page LENGTH, and the turn floor does not outrank it. A production caller depends on that
// — store-load.ts's confirmChatExists sends `?limit=1` as the cheapest page the endpoint
// will serve, for a deep link to a chat the store holds no row for, and decodes only
// `chat`. Subordinating the message cap to the floor would hand that probe a whole turn.
func TestHandleOne_TheMessageCapStaysAHardCut(t *testing.T) {
	// The cut lands MID-TURN: the newest turn holds three messages and only its last is
	// served, so this is the floor being overridden rather than a turn that happened to fit.
	msgs := []vibekit.Message{
		user("uold", "old", 100), fatMessage("old", 4096),
		user("unew", "new", 100), fatMessage("n1", 4096), fatMessage("n2", 4096),
	}

	page, ids, _ := serveOnePage(t, msgs, "?limit=1")

	if want := []string{"n2"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v: ?limit= is a hard cut, so the header probe stays the "+
			"cheapest page the endpoint serves", ids, want)
	}
	if !page.HasMore {
		t.Error("has_more = false, want true: everything older than the one served message is " +
			"unserved, mid-turn included")
	}
}

// serveOnePage is serveOne's sibling for a case that reads the window's left-edge fields as
// well as its ids. serveOne discards the page, so a case needing turn_offset cannot use it.
func serveOnePage(t *testing.T, msgs []vibekit.Message, query string) (windowPage, []string, int) {
	t.Helper()
	s, _ := newTestStore(t)
	return servePage(t, s, msgs, query)
}
