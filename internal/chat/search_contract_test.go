package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// searchHitsFixture is the envelope of testdata/search_hits.json: the wire
// shape of a search reply, pinned across languages. The Go side PRODUCES it
// from a real Search run (golden, regenerated behind UPDATE_GOLDEN=1); the TS
// side (chat-search.node.test.ts) DECODES it through the hand-maintained
// SearchHit mirror. A field renamed, added, or re-typed on either side fails
// the other side's test — SearchHit is deliberately not wiregen-registered, so
// this fixture is the only mechanism keeping the two spellings in agreement.
type searchHitsFixture struct {
	Comment []string          `json:"_comment"`
	Queries []searchHitsQuery `json:"queries"`
}

// searchHitsQuery is one query's pinned reply.
type searchHitsQuery struct {
	Name          string      `json:"name"`
	Query         string      `json:"query"`
	CaseSensitive bool        `json:"case_sensitive"`
	Hits          []SearchHit `json:"hits"`
}

var searchHitsComment = []string{
	"The SearchHit wire shape, as a real Search() reply both languages must agree on.",
	"",
	"chat.SearchHit is deliberately NOT wiregen-registered (the generated namespace's",
	"SearchHit name is taken by the tools type), so the TypeScript mirror in",
	"static-src/chat-search.ts is hand-maintained. This file is what keeps the two",
	"spellings honest: TestSearchHitWireContract (Go) asserts the real search output",
	"marshals to exactly these bytes, and chat-search.node.test.ts (TypeScript)",
	"decodes every hit through the mirror type and pins the field-level invariants",
	"(segment kinds, segment-relative RUNE offsets, message-kind zero contract).",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchHitWireContract",
	"then re-run the TS half: npx vitest --run chat-search.node.test.ts (from static-src/).",
}

// searchContractMessages is the message set the fixture's replies are computed
// from: a legacy blockless message (two occurrences, the second behind a
// multibyte word so a byte offset could not impersonate a rune offset), a
// block-bearing assistant message covering reasoning / content / tool title /
// tool output / a delegate's content, and a tool-only assistant message so the
// filter-only query yields a message-kind hit with no prose behind it.
func searchContractMessages() []vibekit.Message {
	return []vibekit.Message{
		{ID: "u1", Role: vibekit.RoleUser, Content: "Where does the retry backoff live? The naïve loop calls retry twice."},
		{
			ID: "a1", Role: vibekit.RoleAssistant,
			Blocks: []vibekit.Block{
				{Type: vibekit.BlockThinking, Thinking: "The retry semantics differ per client."},
				{Type: vibekit.BlockText, Text: "The **retry** helper lives in fetch.go; wrap the call in retry(ctx)."},
				{Type: vibekit.BlockToolUse, ToolCallID: "t1"},
				{Type: vibekit.BlockText, Text: "The delegate traced the retry path end to end.", AgentSubtaskID: "sub-1"},
			},
			ToolCalls: []vibekit.ToolCall{{
				ID: "t1", Title: "Read retry.go", Kind: vibekit.ToolKind("read"),
				Status: vibekit.ToolStatus("completed"), Output: "func retry(ctx context.Context) error",
			}},
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

// TestSearchHitWireContract pins the marshaled shape of real Search output to
// testdata/search_hits.json — the cross-language fixture chat-search.node.test.ts
// reads (the turn_outcomes.json pattern).
func TestSearchHitWireContract(t *testing.T) {
	msgs := searchContractMessages()
	fx := searchHitsFixture{
		Comment: searchHitsComment,
		Queries: []searchHitsQuery{
			{Name: "free text hits every segment kind", Query: "retry"},
			{Name: "filter only lists matching messages", Query: "role:assistant"},
		},
	}
	kinds := make(map[SegmentKind]int)
	for i := range fx.Queries {
		q := &fx.Queries[i]
		q.Hits = Search(msgs, q.Query, q.CaseSensitive)
		if len(q.Hits) == 0 {
			t.Fatalf("Search(%q) found nothing; an empty fixture would pin nothing", q.Query)
		}
		for _, h := range q.Hits {
			kinds[h.SegmentKind]++
		}
	}
	for _, want := range []SegmentKind{SegmentContent, SegmentReasoning, SegmentToolTitle, SegmentToolOutput, SegmentMessage} {
		if kinds[want] == 0 {
			t.Errorf("fixture carries no %q hit; the TS side cannot pin a kind that never occurs", want)
		}
	}

	got, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	const path = "testdata/search_hits.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchHitWireContract): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Search output drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestSearchHitWireContract, "+
			"then re-run the TS half: npx vitest --run chat-search.node.test.ts (from static-src/).",
			path, want, got)
	}
}
