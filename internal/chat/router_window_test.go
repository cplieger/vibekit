package chat

import (
	"encoding/json"
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
