package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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

// serveOne runs GET /api/chats/c1<query> against a store holding msgs and
// returns the decoded window plus the raw body length.
func serveOne(t *testing.T, msgs []vibekit.Message, query string) (ids []string, hasMore bool, bodyLen int) {
	t.Helper()
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = msgs
		return true
	})
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+query, nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Messages []vibekit.Message `json:"messages"`
		HasMore  bool              `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids = make([]string, len(got.Messages))
	for i, m := range got.Messages {
		ids[i] = m.ID
	}
	return ids, got.HasMore, rec.Body.Len()
}

func TestHandleOne_ByteBudgetCutsAtAMessageBoundary(t *testing.T) {
	// Four messages of ~4 KiB each against a 10 KiB budget: two fit, the third
	// would overrun. The cut is at the boundary, so the answer is the two
	// newest, and has_more names the two the client does not have.
	msgs := []vibekit.Message{
		fatMessage("a", 4096), fatMessage("b", 4096),
		fatMessage("c", 4096), fatMessage("d", 4096),
	}

	ids, hasMore, bodyLen := serveOne(t, msgs, "?max_bytes=10240")

	if want := []string{"c", "d"}; !slices.Equal(ids, want) {
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
	msgs := []vibekit.Message{fatMessage("a", 64), fatMessage("big", 200_000)}

	ids, hasMore, _ := serveOne(t, msgs, "?max_bytes=1024")

	if want := []string{"big"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v — the newest message goes through whole", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: the older message was not served")
	}
}

func TestHandleOne_HasMoreIsHonestAgainstTheBytes(t *testing.T) {
	// The defect the budget replaces: has_more used to describe only the message
	// COUNT, so a response that dropped nothing by count and everything by size
	// still said false. Same six messages, two budgets, two honest answers.
	msgs := make([]vibekit.Message, 6)
	for i := range msgs {
		msgs[i] = fatMessage(string(rune('a'+i)), 4096)
	}

	all, allMore, _ := serveOne(t, msgs, "?max_bytes=1048576")
	if len(all) != 6 {
		t.Errorf("a 1 MiB budget served %d of 6 messages, want all of them", len(all))
	}
	if allMore {
		t.Error("has_more = true with every message served, want false")
	}

	_, someMore, _ := serveOne(t, msgs, "?max_bytes=10240")
	if !someMore {
		t.Error("has_more = false with a 10 KiB budget over ~24 KiB of messages, want true")
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
		blockyMessage("a", 40), blockyMessage("b", 40),
		blockyMessage("c", 40), blockyMessage("d", 40),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?blocks=100&max_bytes=8388608")

	if want := []string{"c", "d"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v — 80 blocks fit a 100-block budget and 120 do not", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: two older messages were not served")
	}
}

// The same rule the byte budget follows: the newest message goes through whole
// however many blocks it carries, or the newest message of a block-heavy chat
// would be unreachable. One measured assistant message carries 580 blocks.
func TestHandleOne_BlockBudgetLetsOneOversizeMessageThroughWhole(t *testing.T) {
	msgs := []vibekit.Message{blockyMessage("a", 2), blockyMessage("big", 600)}

	ids, hasMore, _ := serveOne(t, msgs, "?blocks=100")

	if want := []string{"big"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v — the newest message goes through whole", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: the older message was not served")
	}
}

// has_more answers against whichever budget cut the page, so a client that reads
// it can still reach everything the block budget held back.
func TestHandleOne_HasMoreIsHonestAgainstTheBlocks(t *testing.T) {
	msgs := make([]vibekit.Message, 6)
	for i := range msgs {
		msgs[i] = blockyMessage(string(rune('a'+i)), 40)
	}

	all, allMore, _ := serveOne(t, msgs, "?blocks=8192&max_bytes=8388608")
	if len(all) != 6 {
		t.Errorf("a 8192-block budget served %d of 6 messages, want all of them", len(all))
	}
	if allMore {
		t.Error("has_more = true with every message served, want false")
	}

	_, someMore, _ := serveOne(t, msgs, "?blocks=100&max_bytes=8388608")
	if !someMore {
		t.Error("has_more = false with a 100-block budget over 240 blocks, want true")
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
		toolyMessage("a", 40), toolyMessage("b", 40),
		toolyMessage("c", 40), toolyMessage("d", 40),
	}

	ids, hasMore, _ := serveOne(t, msgs, "?tool_calls=100&blocks=8192&max_bytes=8388608")

	if want := []string{"c", "d"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v — 80 tool calls fit a 100-call budget and 120 do not",
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
	msgs := []vibekit.Message{toolyMessage("a", 2), toolyMessage("big", 400)}

	ids, hasMore, _ := serveOne(t, msgs, "?tool_calls=96")

	if want := []string{"big"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v — the newest message goes through whole", ids, want)
	}
	if !hasMore {
		t.Error("has_more = false, want true: the older message was not served")
	}
}

// A caller that names a block budget and no tool-call budget gets the answer the
// block budget alone gives, which is what the tool-call DEFAULT is chosen for: it
// is the block default, and every tool call the client synthesizes a block for
// costs a block too, so the default cannot cut a page the blocks admitted.
func TestHandleOne_TheDefaultToolCallBudgetCutsNothingTheBlocksAllow(t *testing.T) {
	msgs := make([]vibekit.Message, 6)
	for i := range msgs {
		msgs[i] = toolyMessage(string(rune('a'+i)), 40)
	}

	ids, hasMore, _ := serveOne(t, msgs, "?blocks=8192&max_bytes=8388608")

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
