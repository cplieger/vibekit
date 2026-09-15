package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// searchFixture is the envelope of testdata/search_hits.json: one real
// GET /api/chats/{id}/search reply per query, pinned across languages. The Go
// side PRODUCES it from a real scan (golden, regenerated behind UPDATE_GOLDEN=1);
// the TS side (chat-search.node.test.ts) DECODES it through the generated
// decodeSearchResult. A field the encoder renames or re-types fails the decode.
type searchFixture struct {
	Comment []string          `json:"_comment"`
	Queries []searchQueryCase `json:"queries"`
}

// searchQueryCase is one query's pinned reply.
type searchQueryCase struct {
	Name          string       `json:"name"`
	Query         string       `json:"query"`
	CaseSensitive bool         `json:"case_sensitive"`
	Result        SearchResult `json:"result"`
}

var searchFixtureComment = []string{
	"GET /api/chats/{id}/search replies, produced by a real scan over one message set.",
	"",
	"chat.SearchResult and chat.Hit are wiregen-registered, so the TypeScript types and",
	"decoders are generated from them; this file is what keeps the ENCODER honest against",
	"that decoder. TestSearchWireContract (Go) asserts the scan marshals to exactly these",
	"bytes, and chat-search.node.test.ts (TypeScript) decodes every reply through the",
	"generated decodeSearchResult and pins the field-level invariants (segment-relative",
	"RUNE offsets, the message-kind zero contract, the tally beside the hits).",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchWireContract",
	"then re-run the TS half: npx vitest --run chat-search.node.test.ts (from static-src/).",
}

// searchContractMessages is the message set the fixture's replies are computed
// from: a legacy blockless message (two occurrences, the second behind a
// multibyte word so a byte offset could not impersonate a rune offset, plus the
// attachment that exercises the legacy path's message-level tail), a
// block-bearing assistant message covering reasoning / content / tool title /
// tool disclosed / tool diff / tool denial / tool input / tool output / a
// delegate's content / a plan / a turn failure reason, and a tool-only assistant
// message so the filter-only query yields a message-kind hit with no prose
// behind it.
//
// Every declared segment kind must OCCUR here: this file's own loop and
// TestSearch_SegmentKindsAreExhaustive both fail on a kind with no hit, so a
// kind added to segmentKinds without a fixture occurrence is red on the Go side
// and unrepresented in the fixture the TS side decodes.
func searchContractMessages() []vibekit.Message {
	return []vibekit.Message{
		{
			ID: "u1", Role: vibekit.RoleUser,
			Content: "Where does the retry backoff live? The naïve loop calls retry twice.",
			// The attachment is on the USER message deliberately: it is the one
			// message here with no block array, so it exercises legacySegments'
			// own message-level tail rather than messageSegments'. Its PATH also
			// carries the needle and contributes no hit, which is premise 3
			// holding in the golden rather than only in a unit test.
			Attachments: []vibekit.Attachment{{Path: "docs/retry/backoff.md", Name: "retry-notes.md"}},
		},
		{
			ID: "a1", Role: vibekit.RoleAssistant,
			Blocks: []vibekit.Block{
				{Type: vibekit.BlockThinking, Thinking: "The retry semantics differ per client."},
				{Type: vibekit.BlockText, Text: "The **retry** helper lives in fetch.go; wrap the call in retry(ctx)."},
				{Type: vibekit.BlockToolUse, ToolCallID: "t1"},
				{Type: vibekit.BlockText, Text: "The delegate traced the retry path end to end.", AgentSubtaskID: "sub-1"},
				{Type: vibekit.BlockToolUse, ToolCallID: "t3"},
				{Type: vibekit.BlockToolUse, ToolCallID: "t4"},
				{Type: vibekit.BlockToolUse, ToolCallID: "t5"},
			},
			// The two MESSAGE-level tail kinds of the block-bearing shape. Both
			// render OUTSIDE this message's row — the plan card is in the row, the
			// notice is card-level — which is what the client's turn-level arm is
			// for; neither carries a block index.
			Plan:              []vibekit.PlanEntry{{Content: "Trace the retry path", Status: vibekit.PlanCompleted}},
			TurnFailureReason: "the retry budget ran out",
			ToolCalls: []vibekit.ToolCall{
				{
					ID: "t1", Title: "Read retry.go", Kind: vibekit.ToolKind("read"),
					Status: vibekit.ToolStatus("completed"), Output: "func retry(ctx context.Context) error",
				},
				{
					// The diff-bearing call, and the one carrying the fixture's ONE
					// tool_input occurrence. THE CONSTRAINT ON THAT INPUT: its
					// `retry`-bearing leaf is 28 bytes, under inputLeafDedupeMin (40),
					// so the containment skip keeps it even though it occurs verbatim
					// in this new_text. A leaf at or above that length and present in
					// the diff is DROPPED, which would leave the fixture with no
					// tool_input hit and fail the golden's kind loop and
					// TestSearch_SegmentKindsAreExhaustive at once.
					//
					// old_text carries `retry` too, and deliberately: the golden then
					// shows the new_text-only decision holding rather than merely
					// asserting it elsewhere.
					ID: "t3", Title: "Replace in File", Kind: vibekit.ToolKind("edit"),
					Status: vibekit.ToolStatus("completed"),
					Input:  json.RawMessage(`{"path":"fetch.go","newStr":"return retry(ctx, fetchOnce)"}`),
					Diffs: []vibekit.ToolDiff{{
						Path:    "fetch.go",
						OldText: "func fetch(ctx context.Context) error { return retryOnce(ctx) }",
						NewText: "func fetch(ctx context.Context) error {\n\treturn retry(ctx, fetchOnce)\n}",
					}},
				},
				{
					// The disclosed claim REPLACES this card's title in the display,
					// so the display name is the only text a reader can see here. URI
					// carries the needle too and contributes nothing, which is the
					// DisplayName-only decision holding in the golden.
					ID: "t4", Title: "Disclose Context", Kind: vibekit.ToolKind("other"),
					Status: vibekit.ToolStatus("completed"),
					Disclosed: &vibekit.ToolDisclosed{
						Type:        "skill",
						DisplayName: "retry-budget",
						URI:         "file:///workspace/.kiro/skills/retry/SKILL.md",
					},
				},
				{
					// The denial's RESOURCE is the one reader-facing string; Capability
					// and the rule's patterns carry the needle and contribute nothing.
					ID: "t5", Title: "Run Command", Kind: vibekit.ToolKind("execute"),
					Status: vibekit.ToolStatus("failed"),
					Denial: &vibekit.ToolDenial{
						Capability: "shell_retry",
						Resource:   "rm -rf /config/retry",
						Scope:      "user",
						Source:     "permissions.yaml",
						Rule: &vibekit.ToolDenialRule{
							Capability: "shell", Effect: "deny", Match: []string{"rm -rf /config/retry*"},
						},
					},
				},
			},
		},
		{ID: "u2", Role: vibekit.RoleUser, Content: "Anything left?"},
		{
			ID: "a2", Role: vibekit.RoleAssistant,
			Blocks: []vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t2"}},
			ToolCalls: []vibekit.ToolCall{{
				ID: "t2", Title: "List files", Kind: vibekit.ToolKind("read"),
				Status: vibekit.ToolStatus("completed"), Output: "a.go b.go",
			}},
		},
	}
}

// TestSearchWireContract pins the marshaled shape of the in-chat search reply to
// testdata/search_hits.json — the cross-language fixture chat-search.node.test.ts
// reads (the turn_outcomes.json pattern).
func TestSearchWireContract(t *testing.T) {
	msgs := searchContractMessages()
	fx := searchFixture{
		Comment: searchFixtureComment,
		Queries: []searchQueryCase{
			{Name: "free text hits every segment kind", Query: "retry"},
			{Name: "filter only lists matching messages", Query: "role:assistant"},
		},
	}
	kinds := make(map[SegmentKind]int)
	for i := range fx.Queries {
		q := &fx.Queries[i]
		q.Result = Search(msgs, q.Query, q.CaseSensitive)
		if len(q.Result.Matches) == 0 {
			t.Fatalf("Search(%q) found nothing; an empty fixture would pin nothing", q.Query)
		}
		for _, h := range q.Result.Matches {
			kinds[h.SegmentKind]++
		}
	}
	// segmentKinds rather than a literal of its own: two enumerations of one
	// vocabulary drift, and the drift is silent in exactly the direction that
	// matters — a kind declared with no producer would pass a loop that never
	// learned about it.
	for _, want := range segmentKinds {
		if kinds[want] == 0 {
			t.Errorf("fixture carries no %q hit; the TS side cannot pin a kind that never occurs", want)
		}
	}

	pinGolden(t, "testdata/search_hits.json", fx, "TestSearchWireContract", "chat-search.node.test.ts")
}

// searchAllFixture is the envelope of testdata/search_all.json: one real
// GET /api/chats/search reply, decoded by actions/chat-search.node.test.ts
// through the generated decodeSearchAllResult.
type searchAllFixture struct {
	Comment []string        `json:"_comment"`
	Query   string          `json:"query"`
	Result  SearchAllResult `json:"result"`
}

var searchAllFixtureComment = []string{
	"A GET /api/chats/search reply, produced by a real SearchAll over a seeded store.",
	"",
	"chat.SearchAllResult and chat.Match are wiregen-registered; TestSearchAllWireContract",
	"(Go) asserts the store's reply marshals to exactly these bytes, and",
	"actions/chat-search.node.test.ts (TypeScript) decodes it through the generated",
	"decodeSearchAllResult. A title-only match carries no `best`.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchAllWireContract",
	"then re-run the TS half: npx vitest --run actions/chat-search.node.test.ts (from static-src/).",
}

// TestSearchAllWireContract pins the marshaled shape of the cross-chat search
// reply. Three chats, each a different row shape: a title-and-body match, a
// body-only match with several hits (the multibyte word before the second one
// keeps the rune offset honest), and a title-only match with no best hit. Chat
// files are written directly so UpdatedAt and the mtime order are fixed.
func TestSearchAllWireContract(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	chats := []*vibekit.Chat{
		{
			ID: "chat-001", Name: "Redis migration", UpdatedAt: 1000,
			Messages: []vibekit.Message{
				{ID: "m1", Role: vibekit.RoleUser, Content: "we moved the cache to redis today"},
				{ID: "m2", Role: vibekit.RoleAssistant, Content: "Redis is up; the naïve redis client was replaced."},
			},
		},
		{
			ID: "chat-002", Name: "Grocery list", UpdatedAt: 2000,
			Messages: []vibekit.Message{
				{ID: "m1", Role: vibekit.RoleUser, Content: "nothing relevant here at all"},
			},
		},
		{
			ID: "chat-003", Name: "Why redis over memcached", UpdatedAt: 3000,
			Messages: []vibekit.Message{
				{ID: "m1", Role: vibekit.RoleUser, Content: "compare the two caches for us"},
			},
		},
	}
	for i, c := range chats {
		seedChatFile(t, s, c, base.Add(time.Duration(i)*time.Minute))
	}

	fx := searchAllFixture{Comment: searchAllFixtureComment, Query: "redis"}
	fx.Result = s.SearchAll(t.Context(), fx.Query)
	if len(fx.Result.Matches) != 2 {
		t.Fatalf("SearchAll(%q) matched %d chats, want 2: %+v", fx.Query, len(fx.Result.Matches), fx.Result.Matches)
	}
	var withBest, titleOnly int
	for _, m := range fx.Result.Matches {
		if m.Best != nil {
			withBest++
		} else {
			titleOnly++
		}
	}
	if withBest == 0 || titleOnly == 0 {
		t.Fatalf("fixture needs one match with a best hit and one without, got %d and %d", withBest, titleOnly)
	}

	pinGolden(t, "testdata/search_all.json", fx, "TestSearchAllWireContract", "actions/chat-search.node.test.ts")
}

// pinGolden marshals v, rewrites path behind UPDATE_GOLDEN=1, and compares the
// bytes. The failure names the regeneration command and the TypeScript consumer
// to re-run, because a cross-language fixture is one atomic change.
func pinGolden(t *testing.T, path string, v any, regen, consumer string) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/chat/ -run %s): %v", path, regen, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/chat/ -run %s, "+
			"then re-run the TS half: npx vitest --run %s (from static-src/).",
			path, want, got, regen, consumer)
	}
}
