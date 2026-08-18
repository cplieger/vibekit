package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func msg(id string, role api.Role, content string) api.Message {
	return api.Message{ID: id, Role: role, Content: content, Ts: 100}
}

// transcript: turn 1 = u1/a1, turn 2 = u2/a2.
func transcript() []api.Message {
	return []api.Message{
		msg("u1", api.RoleUser, "how does the retry work"),
		msg("a1", api.RoleAssistant, "The retry uses exponential backoff."),
		msg("u2", api.RoleUser, "now fix the composer"),
		msg("a2", api.RoleAssistant, "Done, the composer grows upward."),
	}
}

func TestSearch_FindsTextAndNamesItsTurn(t *testing.T) {
	hits := Search(transcript(), "composer", false)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (u2 and a2)", len(hits))
	}
	if hits[0].MessageID != "u2" || hits[0].Turn != 2 {
		t.Errorf("hit 0 = %+v, want u2 in turn 2", hits[0])
	}
	// The turn's OPENER, not the matched message — the fold state keys on it, and
	// a hit on an assistant message has to resolve back to its turn.
	if hits[1].MessageID != "a2" || hits[1].TurnMessageID != "u2" {
		t.Errorf("hit 1 = %+v, want a2 resolving to opener u2", hits[1])
	}
}

// The match-case flag governs the FREE TEXT only. The scoped filters stay
// case-insensitive whatever the reader asked for: `role:` is an enum, and a path
// filter that suddenly cared about case would be a behaviour change nobody
// requested by ticking a box labelled "match case".
func TestSearch_CaseSensitivity(t *testing.T) {
	msgs := []api.Message{
		msg("u1", api.RoleUser, "now fix the composer"),
		msg("a1", api.RoleAssistant, "Done, the Composer grows upward."),
	}
	filtered := []api.Message{{
		ID:           "u1",
		Role:         api.RoleUser,
		Content:      "look at the Composer",
		Ts:           100,
		ChangedFiles: map[string]*api.FileChange{"static-src/Composer.ts": {}},
		ToolCalls:    []api.ToolCall{{ID: "t1", Title: "ReadFile", Kind: api.ToolKindRead}},
	}}

	cases := []struct {
		name          string
		msgs          []api.Message
		query         string
		caseSensitive bool
		want          int
	}{
		// The behaviour every existing client gets, and the default on the wire.
		{name: "insensitive finds both spellings", msgs: msgs, query: "composer", want: 2},
		{name: "insensitive from an upper-case query", msgs: msgs, query: "COMPOSER", want: 2},
		{
			name:          "sensitive finds only the exact spelling",
			msgs:          msgs,
			query:         "composer",
			caseSensitive: true,
			want:          1,
		},
		{
			name:          "sensitive finds the capitalised spelling",
			msgs:          msgs,
			query:         "Composer",
			caseSensitive: true,
			want:          1,
		},
		{
			name:          "sensitive finds nothing when nothing matches exactly",
			msgs:          msgs,
			query:         "COMPOSER",
			caseSensitive: true,
			want:          0,
		},
		// The filters are unaffected in either mode.
		{
			name:          "a file filter stays case-insensitive under match-case",
			msgs:          filtered,
			query:         "file:composer.ts",
			caseSensitive: true,
			want:          1,
		},
		{
			name:          "a tool filter stays case-insensitive under match-case",
			msgs:          filtered,
			query:         "tool:readfile",
			caseSensitive: true,
			want:          1,
		},
		{
			name:          "a role filter stays case-insensitive under match-case",
			msgs:          filtered,
			query:         "role:USER",
			caseSensitive: true,
			want:          1,
		},
		// A filter plus free text: the filter is folded, the text is not.
		{
			name:          "the free text half still respects match-case",
			msgs:          filtered,
			query:         "role:user composer",
			caseSensitive: true,
			want:          0,
		},
		{
			name:          "the free text half matches at its own casing",
			msgs:          filtered,
			query:         "role:user Composer",
			caseSensitive: true,
			want:          1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(Search(tc.msgs, tc.query, tc.caseSensitive)); got != tc.want {
				t.Errorf("Search(%q, case=%v) = %d hits, want %d",
					tc.query, tc.caseSensitive, got, tc.want)
			}
		})
	}
}

func TestSearch_CaseSensitiveOffsetsStayRuneIndices(t *testing.T) {
	// The rune-offset arithmetic reads a prefix of the HAYSTACK, which is the
	// folded string in insensitive mode and the original in sensitive mode. Both
	// have to land on the same rune index for the client to highlight the right
	// occurrence.
	msgs := []api.Message{msg("u1", api.RoleUser, "héllo wörld Needle")}
	for _, cs := range []bool{false, true} {
		hits := Search(msgs, "Needle", cs)
		if len(hits) != 1 {
			t.Fatalf("case=%v: got %d hits, want 1", cs, len(hits))
		}
		if want := len([]rune("héllo wörld ")); hits[0].Offset != want {
			t.Errorf("case=%v: offset = %d, want %d", cs, hits[0].Offset, want)
		}
	}
}

func TestSearch_ReportsEveryOccurrenceInOneMessage(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "retry retry retry")}
	hits := Search(msgs, "retry", false)
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3", len(hits))
	}
	// Offsets are RUNE indices so the client highlights the right occurrence
	// rather than always the first.
	for i, want := range []int{0, 6, 12} {
		if hits[i].Offset != want {
			t.Errorf("hit %d offset = %d, want %d", i, hits[i].Offset, want)
		}
	}
}

func TestSearch_OffsetsAreRuneIndicesNotBytes(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "héllo wörld needle")}
	hits := Search(msgs, "needle", false)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if want := len([]rune("héllo wörld ")); hits[0].Offset != want {
		t.Errorf("offset = %d, want %d (rune index, not byte)", hits[0].Offset, want)
	}
}

func TestSearch_EmptyQueryFindsNothing(t *testing.T) {
	for _, q := range []string{"", "   "} {
		if got := Search(transcript(), q, false); len(got) != 0 {
			t.Errorf("query %q returned %d hits", q, len(got))
		}
	}
}

// An empty result must marshal as [] rather than null, so the client has one
// empty case instead of two.
func TestSearch_NeverReturnsNil(t *testing.T) {
	if Search(transcript(), "", false) == nil {
		t.Error("empty query returned a nil slice")
	}
	if Search(nil, "anything", false) == nil {
		t.Error("empty transcript returned a nil slice")
	}
}

func TestSearch_SearchesReasoningAndToolOutput(t *testing.T) {
	msgs := []api.Message{
		{
			ID: "a1", Role: api.RoleAssistant, Ts: 1,
			Reasoning: "considering a mutex here",
			ToolCalls: []api.ToolCall{{ID: "t1", Title: "shell", Output: "permission denied"}},
		},
	}
	// "which turn printed that error" is asked more often than "which turn
	// mentioned it", so tool output is searchable.
	if len(Search(msgs, "permission denied", false)) == 0 {
		t.Error("tool output is not searchable")
	}
	if len(Search(msgs, "mutex", false)) == 0 {
		t.Error("the thinking trace is not searchable")
	}
}

func TestSearch_ScopedFilters(t *testing.T) {
	msgs := []api.Message{
		msg("u1", api.RoleUser, "look at auth"),
		{
			ID: "a1", Role: api.RoleAssistant, Ts: 2, Content: "reading it",
			ToolCalls: []api.ToolCall{
				{ID: "t1", Title: "readFile", Kind: "read", Locations: []api.ToolLocation{{Path: "internal/auth/token.go"}}},
			},
		},
		msg("u2", api.RoleUser, "and the composer"),
		{
			ID: "a2", Role: api.RoleAssistant, Ts: 4, Content: "editing it",
			ChangedFiles: map[string]*api.FileChange{"static-src/composer.ts": {LinesAdded: 3}},
		},
	}

	t.Run("role", func(t *testing.T) {
		for _, h := range Search(msgs, "role:user", false) {
			if h.Role != api.RoleUser {
				t.Errorf("role:user returned a %s message", h.Role)
			}
		}
		if len(Search(msgs, "role:user", false)) != 2 {
			t.Errorf("role:user matched %d, want 2", len(Search(msgs, "role:user", false)))
		}
	})

	t.Run("turn", func(t *testing.T) {
		hits := Search(msgs, "turn:2", false)
		if len(hits) == 0 {
			t.Fatal("turn:2 matched nothing")
		}
		for _, h := range hits {
			if h.Turn != 2 {
				t.Errorf("turn:2 returned a hit in turn %d", h.Turn)
			}
		}
	})

	// A file only READ never appears in changed_files, and "the turn where you
	// looked at auth.go" is a real question — so locations count too.
	t.Run("file matches a read as well as a write", func(t *testing.T) {
		if got := Search(msgs, "file:token.go", false); len(got) != 1 || got[0].MessageID != "a1" {
			t.Errorf("file:token.go = %+v, want the reading turn", got)
		}
		if got := Search(msgs, "file:composer.ts", false); len(got) != 1 || got[0].MessageID != "a2" {
			t.Errorf("file:composer.ts = %+v, want the writing turn", got)
		}
	})

	t.Run("tool matches title or kind", func(t *testing.T) {
		if len(Search(msgs, "tool:readFile", false)) != 1 {
			t.Error("tool:readFile did not match by title")
		}
		if len(Search(msgs, "tool:read", false)) != 1 {
			t.Error("tool:read did not match by kind")
		}
	})

	t.Run("filters combine", func(t *testing.T) {
		if got := Search(msgs, "role:user turn:1", false); len(got) != 1 || got[0].MessageID != "u1" {
			t.Errorf("combined filters = %+v", got)
		}
		// A filter that excludes everything returns nothing rather than ignoring
		// itself.
		if got := Search(msgs, "role:user turn:99", false); len(got) != 0 {
			t.Errorf("impossible combination returned %d hits", len(got))
		}
	})

	t.Run("filter plus free text", func(t *testing.T) {
		if got := Search(msgs, "role:user composer", false); len(got) != 1 || got[0].MessageID != "u2" {
			t.Errorf("filter+text = %+v", got)
		}
	})
}

// A reader typing a URL means it literally, so an unknown prefix stays text.
func TestSearch_UnknownPrefixStaysFreeText(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "see https://example.com for more")}
	if len(Search(msgs, "https://example.com", false)) == 0 {
		t.Error("a colon-bearing term was parsed as a filter and lost")
	}
}

func TestSearch_NonNumericTurnStaysFreeText(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "the turn:abc marker")}
	if len(Search(msgs, "turn:abc", false)) == 0 {
		t.Error("an unparseable turn filter should fall back to text")
	}
}

func TestSearch_ExcerptCarriesContextAndCollapsesWhitespace(t *testing.T) {
	long := strings.Repeat("a ", 100) + "needle " + strings.Repeat("b ", 100)
	hits := Search([]api.Message{msg("u1", api.RoleUser, long)}, "needle", false)
	if len(hits) != 1 {
		t.Fatalf("got %d hits", len(hits))
	}
	ex := hits[0].Excerpt
	if !strings.Contains(ex, "needle") {
		t.Errorf("excerpt %q does not contain the match", ex)
	}
	if !strings.HasPrefix(ex, "\u2026") || !strings.HasSuffix(ex, "\u2026") {
		t.Errorf("excerpt %q is not marked as cut on both sides", ex)
	}
	if strings.Contains(ex, "  ") {
		t.Errorf("excerpt %q has an uncollapsed whitespace run", ex)
	}
}

func TestSearch_CapsTheResultCount(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, strings.Repeat("hit ", maxSearchHits+50))}
	if got := len(Search(msgs, "hit", false)); got != maxSearchHits {
		t.Errorf("got %d hits, want the cap of %d", got, maxSearchHits)
	}
}

// The turn numbers a hit reports and the ones the rail draws come from the same
// projection, so they cannot disagree by construction.
func TestSearch_TurnNumbersMatchTheRailProjection(t *testing.T) {
	msgs := transcript()
	summaries := api.ProjectTurnSummaries(msgs, false)
	byOpener := make(map[string]int, len(summaries))
	for _, s := range summaries {
		byOpener[s.ID] = s.N
	}
	for _, h := range Search(msgs, "the", false) {
		if byOpener[h.TurnMessageID] != h.Turn {
			t.Errorf("hit %+v disagrees with the projection (%d)", h, byOpener[h.TurnMessageID])
		}
	}
}

// TestHandleSearch_CaseParam pins the HTTP half of the match-case toggle. Both
// halves of the in-chat search have to agree on the flag — the client highlights
// in the DOM while this enumerates session-wide — so it rides the request rather
// than being a default either side could get wrong.
func TestHandleSearch_CaseParam(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []api.Message{
			msg("u1", api.RoleUser, "now fix the composer"),
			msg("a1", api.RoleAssistant, "Done, the Composer grows upward."),
		}
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent is insensitive", query: "?q=composer", want: 2},
		{name: "case=1 is sensitive", query: "?q=composer&case=1", want: 1},
		{name: "case=0 is insensitive", query: "?q=composer&case=0", want: 2},
		{name: "an unrecognised value is insensitive", query: "?q=composer&case=yes", want: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/search"+tc.query, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Hits []SearchHit `json:"hits"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
			}
			if len(body.Hits) != tc.want {
				t.Errorf("%s: %d hits, want %d", tc.query, len(body.Hits), tc.want)
			}
		})
	}
}
