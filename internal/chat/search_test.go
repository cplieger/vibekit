package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func msg(id string, role vibekit.Role, content string) vibekit.Message {
	return vibekit.Message{ID: id, Role: role, Content: content, Ts: 100}
}

// transcript: turn 1 = u1/a1, turn 2 = u2/a2.
func transcript() []vibekit.Message {
	return []vibekit.Message{
		msg("u1", vibekit.RoleUser, "how does the retry work"),
		msg("a1", vibekit.RoleAssistant, "The retry uses exponential backoff."),
		msg("u2", vibekit.RoleUser, "now fix the composer"),
		msg("a2", vibekit.RoleAssistant, "Done, the composer grows upward."),
	}
}

func TestSearch_FindsTextAndNamesItsTurn(t *testing.T) {
	hits := Search(transcript(), "composer", false).Matches
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
	msgs := []vibekit.Message{
		msg("u1", vibekit.RoleUser, "now fix the composer"),
		msg("a1", vibekit.RoleAssistant, "Done, the Composer grows upward."),
	}
	filtered := []vibekit.Message{{
		ID:           "u1",
		Role:         vibekit.RoleUser,
		Content:      "look at the Composer",
		Ts:           100,
		ChangedFiles: map[string]*vibekit.FileChange{"static-src/Composer.ts": {}},
		ToolCalls:    []vibekit.ToolCall{{ID: "t1", Title: "ReadFile", Kind: vibekit.ToolKindRead}},
	}}

	cases := []struct {
		name          string
		msgs          []vibekit.Message
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
			if got := len(Search(tc.msgs, tc.query, tc.caseSensitive).Matches); got != tc.want {
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
	msgs := []vibekit.Message{msg("u1", vibekit.RoleUser, "héllo wörld Needle")}
	for _, cs := range []bool{false, true} {
		hits := Search(msgs, "Needle", cs).Matches
		if len(hits) != 1 {
			t.Fatalf("case=%v: got %d hits, want 1", cs, len(hits))
		}
		if want := len([]rune("héllo wörld ")); hits[0].Offset != want {
			t.Errorf("case=%v: offset = %d, want %d", cs, hits[0].Offset, want)
		}
	}
}

func TestSearch_ReportsEveryOccurrenceInOneMessage(t *testing.T) {
	msgs := []vibekit.Message{msg("u1", vibekit.RoleUser, "retry retry retry")}
	hits := Search(msgs, "retry", false).Matches
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
	msgs := []vibekit.Message{msg("u1", vibekit.RoleUser, "héllo wörld needle")}
	hits := Search(msgs, "needle", false).Matches
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if want := len([]rune("héllo wörld ")); hits[0].Offset != want {
		t.Errorf("offset = %d, want %d (rune index, not byte)", hits[0].Offset, want)
	}
}

func TestSearch_EmptyQueryFindsNothing(t *testing.T) {
	for _, q := range []string{"", "   "} {
		if got := Search(transcript(), q, false).Matches; len(got) != 0 {
			t.Errorf("query %q returned %d hits", q, len(got))
		}
	}
}

// An empty result must marshal as [] rather than null, so the client has one
// empty case instead of two.
func TestSearch_NeverReturnsNil(t *testing.T) {
	if Search(transcript(), "", false).Matches == nil {
		t.Error("empty query returned a nil slice")
	}
	if Search(nil, "anything", false).Matches == nil {
		t.Error("empty transcript returned a nil slice")
	}
}

func TestSearch_SearchesReasoningAndToolOutput(t *testing.T) {
	msgs := []vibekit.Message{
		{
			ID: "a1", Role: vibekit.RoleAssistant, Ts: 1,
			Reasoning: "considering a mutex here",
			ToolCalls: []vibekit.ToolCall{{ID: "t1", Title: "shell", Output: "permission denied"}},
		},
	}
	// "which turn printed that error" is asked more often than "which turn
	// mentioned it", so tool output is searchable.
	if len(Search(msgs, "permission denied", false).Matches) == 0 {
		t.Error("tool output is not searchable")
	}
	if len(Search(msgs, "mutex", false).Matches) == 0 {
		t.Error("the thinking trace is not searchable")
	}
}

func TestSearch_ScopedFilters(t *testing.T) {
	msgs := []vibekit.Message{
		msg("u1", vibekit.RoleUser, "look at auth"),
		{
			ID: "a1", Role: vibekit.RoleAssistant, Ts: 2, Content: "reading it",
			ToolCalls: []vibekit.ToolCall{
				{ID: "t1", Title: "readFile", Kind: "read", Locations: []vibekit.ToolLocation{{Path: "internal/auth/token.go"}}},
			},
		},
		msg("u2", vibekit.RoleUser, "and the composer"),
		{
			ID: "a2", Role: vibekit.RoleAssistant, Ts: 4, Content: "editing it",
			ChangedFiles: map[string]*vibekit.FileChange{"static-src/composer.ts": {LinesAdded: 3}},
		},
	}

	t.Run("role", func(t *testing.T) {
		for _, h := range Search(msgs, "role:user", false).Matches {
			if h.Role != vibekit.RoleUser {
				t.Errorf("role:user returned a %s message", h.Role)
			}
		}
		if len(Search(msgs, "role:user", false).Matches) != 2 {
			t.Errorf("role:user matched %d, want 2", len(Search(msgs, "role:user", false).Matches))
		}
	})

	t.Run("turn", func(t *testing.T) {
		hits := Search(msgs, "turn:2", false).Matches
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
		if got := Search(msgs, "file:token.go", false).Matches; len(got) != 1 || got[0].MessageID != "a1" {
			t.Errorf("file:token.go = %+v, want the reading turn", got)
		}
		if got := Search(msgs, "file:composer.ts", false).Matches; len(got) != 1 || got[0].MessageID != "a2" {
			t.Errorf("file:composer.ts = %+v, want the writing turn", got)
		}
	})

	t.Run("tool matches title or kind", func(t *testing.T) {
		if len(Search(msgs, "tool:readFile", false).Matches) != 1 {
			t.Error("tool:readFile did not match by title")
		}
		if len(Search(msgs, "tool:read", false).Matches) != 1 {
			t.Error("tool:read did not match by kind")
		}
	})

	t.Run("filters combine", func(t *testing.T) {
		if got := Search(msgs, "role:user turn:1", false).Matches; len(got) != 1 || got[0].MessageID != "u1" {
			t.Errorf("combined filters = %+v", got)
		}
		// A filter that excludes everything returns nothing rather than ignoring
		// itself.
		if got := Search(msgs, "role:user turn:99", false).Matches; len(got) != 0 {
			t.Errorf("impossible combination returned %d hits", len(got))
		}
	})

	t.Run("filter plus free text", func(t *testing.T) {
		if got := Search(msgs, "role:user composer", false).Matches; len(got) != 1 || got[0].MessageID != "u2" {
			t.Errorf("filter+text = %+v", got)
		}
	})
}

// A reader typing a URL means it literally, so an unknown prefix stays text.
func TestSearch_UnknownPrefixStaysFreeText(t *testing.T) {
	msgs := []vibekit.Message{msg("u1", vibekit.RoleUser, "see https://example.com for more")}
	if len(Search(msgs, "https://example.com", false).Matches) == 0 {
		t.Error("a colon-bearing term was parsed as a filter and lost")
	}
}

func TestSearch_NonNumericTurnStaysFreeText(t *testing.T) {
	msgs := []vibekit.Message{msg("u1", vibekit.RoleUser, "the turn:abc marker")}
	if len(Search(msgs, "turn:abc", false).Matches) == 0 {
		t.Error("an unparseable turn filter should fall back to text")
	}
}

func TestSearch_ExcerptCarriesContextAndCollapsesWhitespace(t *testing.T) {
	long := strings.Repeat("a ", 100) + "needle " + strings.Repeat("b ", 100)
	hits := Search([]vibekit.Message{msg("u1", vibekit.RoleUser, long)}, "needle", false).Matches
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

// The hit LIST is cut at maxSearchHits and the COUNT is not: a reader shown 200
// hits out of 340 is told 340, so the cap reads as a floor rather than a total.
// Truncated stays false because every message was read; the cut is stated by
// Matched exceeding the list, never by that flag.
func TestSearch_MatchedCountsPastTheHitCap(t *testing.T) {
	cases := []struct {
		name        string
		occurrences int
		wantHits    int
	}{
		{name: "under the cap", occurrences: maxSearchHits - 1, wantHits: maxSearchHits - 1},
		{name: "exactly at the cap", occurrences: maxSearchHits, wantHits: maxSearchHits},
		{name: "one past the cap", occurrences: maxSearchHits + 1, wantHits: maxSearchHits},
		{name: "far past the cap", occurrences: 3 * maxSearchHits, wantHits: maxSearchHits},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []vibekit.Message{
				msg("u1", vibekit.RoleUser, strings.Repeat("hit ", tc.occurrences)),
				msg("a1", vibekit.RoleAssistant, "nothing here"),
			}
			res := Search(msgs, "hit", false)
			if len(res.Matches) != tc.wantHits {
				t.Errorf("%d occurrences: got %d hits, want %d", tc.occurrences, len(res.Matches), tc.wantHits)
			}
			if res.Matched != tc.occurrences {
				t.Errorf("%d occurrences: Matched = %d, want %d", tc.occurrences, res.Matched, tc.occurrences)
			}
			if res.Scanned != len(msgs) {
				t.Errorf("%d occurrences: Scanned = %d, want %d (every message is read)", tc.occurrences, res.Scanned, len(msgs))
			}
			if res.Truncated {
				t.Errorf("%d occurrences: Truncated = true, want false: the scan read everything and the cut is Matched > len(Matches)", tc.occurrences)
			}
		})
	}
}

// searchChat's second result is the byte volume of exactly the segments the scan
// read: every span a message exposes, prose or not, and nothing from a message a
// filter excluded. Cross-chat ranking divides its occurrence count by this, so the
// two must cover one span set or a chat is normalised by text it was never
// searched for, or searched in text it is not normalised by.
func TestSearchChat_CharsAreTheSpansTheScanRead(t *testing.T) {
	msgs := []vibekit.Message{
		msg("u1", vibekit.RoleUser, "abc"),
		{
			ID: "a1", Role: vibekit.RoleAssistant,
			Blocks: []vibekit.Block{
				{Type: vibekit.BlockText, Text: "defg"},
				{Type: vibekit.BlockThinking, Thinking: "hijkl"},
				{Type: vibekit.BlockToolUse, ToolCallID: "t1"},
			},
			ToolCalls: []vibekit.ToolCall{{ID: "t1", Title: "run", Output: "mnopqrs"}},
		},
	}
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "every_span_of_every_message", query: "zzz", want: len("abc") + len("defg") + len("hijkl") + len("run") + len("mnopqrs")},
		{name: "a_message_a_filter_excludes_is_not_read", query: "zzz role:user", want: len("abc")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := searchChat(msgs, tc.query, false); got != tc.want {
				t.Errorf("searchChat(%q) chars = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

// The turn numbers a hit reports and the ones the rail draws come from the same
// projection, so they cannot disagree by construction.
func TestSearch_TurnNumbersMatchTheRailProjection(t *testing.T) {
	msgs := transcript()
	summaries := projectTurnSummaries(msgs, false)
	byOpener := make(map[string]int, len(summaries))
	for _, s := range summaries {
		byOpener[s.ID] = s.N
	}
	for _, h := range Search(msgs, "the", false).Matches {
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
	if _, err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []vibekit.Message{
			msg("u1", vibekit.RoleUser, "now fix the composer"),
			msg("a1", vibekit.RoleAssistant, "Done, the Composer grows upward."),
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
			var body SearchResult
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
			}
			if len(body.Matches) != tc.want {
				t.Errorf("%s: %d hits, want %d", tc.query, len(body.Matches), tc.want)
			}
		})
	}
}

// The handler writes the scan's own reply, so the tally reaches the wire: a cut
// list arrives beside the count that says it was cut. The envelope's spelling is
// pinned by testdata/search_hits.json, which the TypeScript decoder reads.
func TestHandleSearch_ReportsTheTally(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []vibekit.Message{
			msg("u1", vibekit.RoleUser, strings.Repeat("hit ", maxSearchHits+1)),
			msg("a1", vibekit.RoleAssistant, "one lonely miss"),
		}
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	cases := []struct {
		name        string
		query       string
		wantHits    int
		wantMatched int
	}{
		{name: "past the cap", query: "hit", wantHits: maxSearchHits, wantMatched: maxSearchHits + 1},
		{name: "under the cap", query: "lonely", wantHits: 1, wantMatched: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/search?q="+tc.query, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			var body SearchResult
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
			}
			if len(body.Matches) != tc.wantHits {
				t.Errorf("q=%s: %d hits, want %d", tc.query, len(body.Matches), tc.wantHits)
			}
			if body.Matched != tc.wantMatched {
				t.Errorf("q=%s: matched = %d, want %d", tc.query, body.Matched, tc.wantMatched)
			}
			if body.Scanned != 2 {
				t.Errorf("q=%s: scanned = %d, want 2", tc.query, body.Scanned)
			}
			if body.Truncated {
				t.Errorf("q=%s: truncated = true, want false", tc.query)
			}
		})
	}
}

// The chat's TITLE is not part of the in-chat search, and this is the layer that
// can say so: Search takes []vibekit.Message and never sees a name, so only the
// handler — which holds the whole record and passes `c.Messages` alone — can pin
// the decision.
//
// It is a DECISION rather than an omission. SearchAll already answers "which
// conversation", and a title hit names no position inside a transcript for the
// client to navigate to, so it would be counted-but-unreachable.
//
// Red check: pass the chat's Name to Search as a synthetic message and this
// finds a hit.
func TestHandleSearch_ChatNameIsNotSearched(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "The needle investigation"
		c.Messages = []vibekit.Message{
			msg("u1", vibekit.RoleUser, "what now"),
			msg("a1", vibekit.RoleAssistant, "nothing to report"),
		}
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/search?q=needle", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(body.Matches) != 0 || body.Matched != 0 {
		t.Errorf("the chat NAME matched %d times (matched=%d), want 0: %+v", len(body.Matches), body.Matched, body.Matches)
	}
}

// `turn:` takes an absolute turn ordinal, and turns are numbered from 1. Turn 0
// names no turn, so `turn:0` is not a filter — it is the text the user typed.
func TestSearch_TurnZeroIsNotAFilter(t *testing.T) {
	msgs := []vibekit.Message{
		msg("u1", vibekit.RoleUser, "see turn:0 for the trace"),
		msg("a1", vibekit.RoleAssistant, "acknowledged"),
	}
	hits := Search(msgs, "turn:0", false).Matches
	if len(hits) != 1 {
		t.Fatalf("Search(%q) returned %d hits, want 1: turn 0 is free text, not a turn filter", "turn:0", len(hits))
	}
	if hits[0].MessageID != "u1" {
		t.Errorf("Search(%q) hit = %+v, want the message containing that text", "turn:0", hits[0])
	}
}

// A message whose thinking trace dwarfs its prose is searchable like any other.
// The two are concatenated before the scan, so the buffer sized for them must
// account for both.
func TestSearch_MessageWithMoreThinkingThanProse(t *testing.T) {
	m := msg("a1", vibekit.RoleAssistant, "ok")
	m.Reasoning = strings.Repeat("thinking ", 200) + "needle"

	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	if len(hits) != 1 {
		t.Fatalf("Search over a message with a %d-byte reasoning trace and a %d-byte body returned %d hits, want 1",
			len(m.Reasoning), len(m.Content), len(hits))
	}
}

// An excerpt marks a cut with an ellipsis and carries a fixed radius of context
// around the match. A mark on an uncut side claims text was dropped when none
// was, and a short radius silently loses context the reader needs.
func TestSearch_ExcerptMarksOnlyTheSidesItActuallyCut(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "match_fills_the_whole_text",
			content: "needle tail",
			want:    "needle tail",
		},
		{
			name:    "text_continues_past_the_radius",
			content: "needle" + strings.Repeat(" x", 100),
			want:    "needle" + strings.Repeat(" x", 30) + "\u2026",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits := Search([]vibekit.Message{msg("u1", vibekit.RoleUser, tc.content)}, "needle", false).Matches
			if len(hits) != 1 {
				t.Fatalf("Search(%q) returned %d hits, want 1", tc.content, len(hits))
			}
			if got := hits[0].Excerpt; got != tc.want {
				t.Errorf("Search(%q) excerpt = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// blockMsg builds a block-bearing assistant message whose legacy Content and
// Reasoning fields mirror the block texts — the shape the buffer persists,
// since one Append*Delta call fills the block array AND the legacy builders.
func blockMsg(id string, blocks []vibekit.Block, tools ...vibekit.ToolCall) vibekit.Message {
	var content, reasoning strings.Builder
	for _, b := range blocks {
		content.WriteString(b.Text)
		reasoning.WriteString(b.Thinking)
	}
	return vibekit.Message{
		ID:        id,
		Role:      vibekit.RoleAssistant,
		Ts:        100,
		Content:   content.String(),
		Reasoning: reasoning.String(),
		Blocks:    blocks,
		ToolCalls: tools,
	}
}

// wantHit is the segment-addressing half of an expected hit; assertBlockHits
// checks it field by field so a failure names the exact coordinate that broke.
type wantHit struct {
	blockIndex *int
	kind       SegmentKind
	subtask    string
	offset     int
	segmentLen int
}

func assertBlockHits(t *testing.T, hits []Hit, want []wantHit) {
	t.Helper()
	if len(hits) != len(want) {
		t.Fatalf("got %d hits, want %d: %+v", len(hits), len(want), hits)
	}
	for i, w := range want {
		h := hits[i]
		if h.SegmentKind != w.kind {
			t.Errorf("hit %d SegmentKind = %q, want %q", i, h.SegmentKind, w.kind)
		}
		if h.AgentSubtaskID != w.subtask {
			t.Errorf("hit %d AgentSubtaskID = %q, want %q", i, h.AgentSubtaskID, w.subtask)
		}
		if h.Offset != w.offset {
			t.Errorf("hit %d Offset = %d, want %d", i, h.Offset, w.offset)
		}
		if h.SegmentLen != w.segmentLen {
			t.Errorf("hit %d SegmentLen = %d, want %d", i, h.SegmentLen, w.segmentLen)
		}
		switch {
		case w.blockIndex == nil && h.BlockIndex != nil:
			t.Errorf("hit %d BlockIndex = %d, want nil", i, *h.BlockIndex)
		case w.blockIndex != nil && h.BlockIndex == nil:
			t.Errorf("hit %d BlockIndex = nil, want %d", i, *w.blockIndex)
		case w.blockIndex != nil && *h.BlockIndex != *w.blockIndex:
			t.Errorf("hit %d BlockIndex = %d, want %d", i, *h.BlockIndex, *w.blockIndex)
		}
	}
}

// The same text in a parent block and a delegate block is two different places:
// each hit names its own block and subtask, and both offsets are relative to
// their OWN segment, so the two identical prefixes yield identical offsets.
func TestSearch_DistinguishesParentAndDelegateBlocks(t *testing.T) {
	m := blockMsg("a1", []vibekit.Block{
		{Type: vibekit.BlockText, Text: "the needle in the parent"},
		{Type: vibekit.BlockText, Text: "the needle in the delegate", AgentSubtaskID: "sub-1"},
	})
	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentContent, blockIndex: new(0), subtask: "", offset: 4, segmentLen: 24},
		{kind: SegmentContent, blockIndex: new(1), subtask: "sub-1", offset: 4, segmentLen: 26},
	})
}

// Two occurrences INSIDE one block are two hits with distinct segment-relative
// offsets — not offsets into any concatenation of the message.
func TestSearch_TwoHitsInOneBlockGetSegmentRelativeOffsets(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{
			{Type: vibekit.BlockText, Text: "intro paragraph"},
			{Type: vibekit.BlockToolUse, ToolCallID: "t1"},
			{Type: vibekit.BlockText, Text: "needle then a needle"},
		},
		vibekit.ToolCall{ID: "t1", Title: "shell"},
	)
	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentContent, blockIndex: new(2), offset: 0, segmentLen: 20},
		{kind: SegmentContent, blockIndex: new(2), offset: 14, segmentLen: 20},
	})
}

// A tool block exposes its title and its output as SEPARATE segments SHARING
// the block index: the kind disambiguates them, and an output hit's offset is
// relative to the output segment, not the title.
func TestSearch_ToolTitleAndOutputAreSeparateSegmentsSharingTheBlock(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{
			{Type: vibekit.BlockText, Text: "running the search now"},
			{Type: vibekit.BlockToolUse, ToolCallID: "t1", AgentSubtaskID: "sub-9"},
		},
		vibekit.ToolCall{ID: "t1", Title: "grep needle", Output: "found a needle here"},
	)
	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentToolTitle, blockIndex: new(1), subtask: "sub-9", offset: 5, segmentLen: 11},
		{kind: SegmentToolOutput, blockIndex: new(1), subtask: "sub-9", offset: 8, segmentLen: 19},
	})
}

// A diff-bearing call's new_text is a segment of its own, sharing the tool
// block's index and carrying a RUNE offset like every other segment. The diff's
// PATH is deliberately not searched — it is already reachable through the `file:`
// filter and through the title — so a needle in the path yields nothing.
//
// Red check: drop the diff arm from toolSegments and this finds nothing.
func TestSearch_DiffNewTextIsSearched(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1", AgentSubtaskID: "sub-2"}},
		vibekit.ToolCall{ID: "t1", Title: "Replace in File", Diffs: []vibekit.ToolDiff{{
			Path: "needle.go", NewText: "åß needle",
		}}},
	)
	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	// "åß " is 3 runes (5 bytes); the whole segment is 9 runes (11 bytes).
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentToolDiff, blockIndex: new(0), subtask: "sub-2", offset: 3, segmentLen: 9},
	})
}

// The new_text-only decision, pinned so it cannot silently become both: 97.4% of
// old_text's lines are also in new_text, so searching both would mint a second
// hit for one rendered line. The stated loss is exactly this — a line the edit
// REMOVED is not findable through the diff.
//
// Red check: add an old_text arm to toolSegments and this finds a hit.
func TestSearch_DiffOldTextIsNotSearched(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{ID: "t1", Title: "Replace in File", Diffs: []vibekit.ToolDiff{{
			OldText: "the needle used to live here",
			NewText: "and now it does not",
		}}},
	)
	if hits := Search([]vibekit.Message{m}, "needle", false).Matches; len(hits) != 0 {
		t.Errorf("Search found %d hits in a diff's old_text, want 0: %+v", len(hits), hits)
	}
}

// A file DELETE has an empty new_text, so it contributes no segment at all —
// the other half of the stated loss, and the reason the arm is guarded rather
// than emitting an empty span nothing can ever match.
func TestSearch_DiffWithEmptyNewTextContributesNoSegment(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{ID: "t1", Title: "Delete needle.go", Diffs: []vibekit.ToolDiff{{
			OldText: "the needle used to live here", NewText: "",
		}}},
	)
	// The title is the only span, so the one hit is a title hit.
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolTitle, blockIndex: new(0), offset: 7, segmentLen: 16},
	})
}

// Only Diffs[0] is searched, because nothing renders or fetches a second diff:
// a hit past the first would be a counted match with no destination.
//
// Red check: loop toolSegments over every diff and this finds two hits.
func TestSearch_OnlyTheFirstDiffIsSearched(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{ID: "t1", Title: "Replace in File", Diffs: []vibekit.ToolDiff{
			{Path: "a.go", NewText: "first needle"},
			{Path: "b.go", NewText: "second needle"},
		}},
	)
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolDiff, blockIndex: new(0), offset: 6, segmentLen: 12},
	})
}

// inputCall builds a tool call whose only searchable span is its input, so a
// hit's kind and offset can be read without a title or an output competing.
func inputCall(input string) vibekit.ToolCall {
	return vibekit.ToolCall{ID: "t1", Title: "Write File", Input: json.RawMessage(input)}
}

// Only the string LEAF VALUES of an input are searched, and each clause of that
// is a separate case: a needle in a KEY is not text a reader searches for, a
// number or a bool is not either, and every string at any depth is covered so a
// nested array element is reachable.
//
// Red checks, one case each: neuter takeLeafSlot's key branch and the KEY case
// finds a hit; append every token rather than only strings and the bool and
// number cases do; push an object frame for `[` as well and the array case loses
// its second element to the key/value alternation.
func TestSearch_ToolInputSearchesStringLeavesOnly(t *testing.T) {
	tests := []struct {
		name  string
		query string
		input string
		want  []wantHit
	}{{
		name:  "a needle in a key yields nothing",
		input: `{"needle":"a value"}`,
	}, {
		name:  "a needle in a string value is a tool_input hit",
		input: `{"cmd":"grep needle here"}`,
		want:  []wantHit{{kind: SegmentToolInput, blockIndex: new(0), offset: 5, segmentLen: 16}},
	}, {
		// A bool is not text a reader searches for, so the query is the bool's own
		// spelling: `true` would match every call carrying a flag.
		name:  "a bool leaf is not searched",
		query: "true",
		input: `{"recurse":true}`,
	}, {
		name:  "a number leaf is not searched",
		query: "42",
		input: `{"limit":42}`,
	}, {
		// The leaves are "outer", "deep needle" and "tail": one segment of
		// "outer\ndeep needle\ntail", so the hit sits 11 runes in.
		name:  "a string nested in an array of objects is covered",
		input: `{"a":"outer","edits":[{"newStr":"deep needle"}],"z":"tail"}`,
		want:  []wantHit{{kind: SegmentToolInput, blockIndex: new(0), offset: 11, segmentLen: 22}},
	}, {
		// An array ELEMENT is a value, never a key, so a walk that alternates
		// key/value inside an array would drop every second path.
		name:  "every element of a string array is covered",
		input: `{"paths":["a.go","needle.go"]}`,
		want:  []wantHit{{kind: SegmentToolInput, blockIndex: new(0), offset: 5, segmentLen: 14}},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			if query == "" {
				query = "needle"
			}
			m := blockMsg("a1",
				[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
				inputCall(tc.input),
			)
			hits := Search([]vibekit.Message{m}, query, false).Matches
			if tc.want == nil {
				if len(hits) != 0 {
					t.Errorf("Search(%q) found %d hits, want 0: %+v", query, len(hits), hits)
				}
				return
			}
			assertBlockHits(t, hits, tc.want)
		})
	}
}

// Leaves are joined in DOCUMENT order, which is the order the card prints them,
// so two occurrences come back at the offsets that order gives them — the
// property that keeps the golden stable and the client's cursor still between two
// requests for one query. A map round trip would randomise it.
//
// The two leaves are deliberately different lengths, so the expected offsets
// (0, 24) belong to this order alone: swapped, the same two leaves yield 11 and
// 18. The keys are reverse-alphabetical for the same reason — a sorted walk
// cannot produce document order by accident here.
//
// Red check: sort the leaves before joining them and this fails. A map round
// trip is the shape actually refused, and it would fail this only SOMETIMES,
// which is the whole objection to it.
func TestSearch_ToolInputLeafOrderIsDocumentOrder(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		inputCall(`{"z":"needle first","a":"and then a needle"}`),
	)
	// "needle first\nand then a needle" is one 30-rune segment.
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolInput, blockIndex: new(0), offset: 0, segmentLen: 30},
		{kind: SegmentToolInput, blockIndex: new(0), offset: 24, segmentLen: 30},
	})
}

// An input hit's offset is a RUNE index into the joined leaf text, like every
// other segment's, even when an earlier leaf carries multi-byte text.
func TestSearch_ToolInputOffsetIsARuneIndex(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		inputCall(`{"a":"åß","b":"ü needle"}`),
	)
	// "åß\nü " is 5 runes (8 bytes); the whole segment is 11 runes (14 bytes).
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolInput, blockIndex: new(0), offset: 5, segmentLen: 11},
	})
}

// An edit call sends its payload twice — once as the input's newStr, once as the
// diff the card renders — and that is ONE rendered write, so it is one hit, on
// the kind whose element actually holds the text. The skip is what makes it so.
//
// Red check: drop the containment skip and this finds two hits.
func TestSearch_InputLeafDoesNotDoubleCountItsDiff(t *testing.T) {
	const payload = "func fetch(ctx context.Context) error { return needle(ctx) }"
	if len(payload) < inputLeafDedupeMin {
		t.Fatalf("payload is %d bytes, under inputLeafDedupeMin (%d): the skip would not apply", len(payload), inputLeafDedupeMin)
	}
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{
			ID: "t1", Title: "Replace in File",
			Input: json.RawMessage(`{"path":"fetch.go","newStr":` + strconv.Quote(payload) + `}`),
			Diffs: []vibekit.ToolDiff{{Path: "fetch.go", NewText: payload}},
		},
	)
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolDiff, blockIndex: new(0), offset: 47, segmentLen: 60},
	})
}

// A malformed input is a NORMAL value: non-JSON bytes and the literal `null`
// boundInput writes for an input it could neither parse nor shorten both yield no
// segment, with no error to the caller — a search must not fail because one tool
// call's input is odd.
func TestSearch_MalformedInputYieldsNoHit(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "the literal null boundInput writes", input: `null`},
		{name: "truncated object", input: `{"cmd":"needle"`},
		{name: "unterminated object", input: `{`},
		{name: "bare word", input: `needle`},
		{name: "trailing garbage past a valid object", input: `{"cmd":"needle"} needle`},
		{name: "absent", input: ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := blockMsg("a1",
				[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
				inputCall(tc.input),
			)
			if hits := Search([]vibekit.Message{m}, "needle", false).Matches; len(hits) != 0 {
				t.Errorf("Search found %d hits in input %q, want 0: %+v", len(hits), tc.input, hits)
			}
		})
	}
}

// An agent-authored plan renders as a card in the message's own row, so each
// entry's content is a searchable span of its own — one segment per entry, with
// no block index, because a plan is a property of the MESSAGE.
//
// This test is the ONLY measurement of plan coverage that will ever exist: the
// corpus carries 0 plan entries fleet-wide, so no reading of a real chat can say
// whether the producer works.
//
// Red check: drop the plan loop from messageTailSegments and this finds nothing.
func TestSearch_PlanIsSearched(t *testing.T) {
	m := blockMsg("a1", []vibekit.Block{{Type: vibekit.BlockText, Text: "starting now"}})
	m.Plan = []vibekit.PlanEntry{
		{Content: "Read the needle", Status: vibekit.PlanCompleted},
		{Content: "Fix the needle", Status: vibekit.PlanPending},
	}
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentPlan, offset: 9, segmentLen: 15},
		{kind: SegmentPlan, offset: 8, segmentLen: 14},
	})
}

// A denial's RESOURCE is the one reader-facing string in the policy verdict — the
// command or path that was refused — so it is the one field searched.
//
// The other measurement the corpus cannot supply: 0 denials fleet-wide.
//
// Red check: drop the denial arm from toolSegments and this finds nothing.
func TestSearch_DenialResourceIsSearched(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{ID: "t1", Title: "Run Command", Denial: &vibekit.ToolDenial{
			Capability: "shell",
			Resource:   "rm -rf needle",
			Scope:      "user",
			Source:     "permissions.yaml",
		}},
	)
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolDenial, blockIndex: new(0), offset: 7, segmentLen: 13},
	})
}

// A turn that ended badly persists WHY, and the card renders it as a notice, so
// the reason is a searchable span of the message that carries it. A real corpus
// population, unlike plan and denial: 240 messages.
//
// Red check: drop the turn-failure arm from messageTailSegments and this finds
// nothing.
func TestSearch_TurnFailureReasonIsSearched(t *testing.T) {
	m := blockMsg("a1", []vibekit.Block{{Type: vibekit.BlockText, Text: "partial answer"}})
	m.TurnFailureReason = "the needle budget ran out"
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentTurnFailure, offset: 4, segmentLen: 25},
	})
}

// An attachment is searched by the NAME the pill renders and NOT by the path,
// which lives in a `title` attribute the client's DOM walker cannot mark. Both
// directions, because either half alone would pass while the other broke.
//
// Red check: search Path instead of Name and the second case finds a hit.
func TestSearch_AttachmentNameIsSearchedAndPathIsNot(t *testing.T) {
	m := msg("u1", vibekit.RoleUser, "have a look")
	m.Attachments = []vibekit.Attachment{
		{Path: "docs/haystack/notes.md", Name: "needle-notes.md"},
	}
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentAttachment, offset: 0, segmentLen: 15},
	})
	if hits := Search([]vibekit.Message{m}, "haystack", false).Matches; len(hits) != 0 {
		t.Errorf("the attachment PATH matched %d times, want 0: %+v", len(hits), hits)
	}
}

// A `disclose_context` call's display name is what the card SHOWS — disclosedClaim
// replaces the title with it — so a reader who can see a skill name must be able
// to find it. DisplayName only: URI is not rendered and Type is a class name.
//
// Red check: drop the disclosed arm from toolSegments and this finds nothing.
func TestSearch_DisclosedDisplayNameIsSearched(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{ID: "t1", Title: "Disclose Context", Disclosed: &vibekit.ToolDisclosed{
			Type:        "skill",
			DisplayName: "needle-review",
			URI:         "file:///workspace/.kiro/skills/haystack/SKILL.md",
		}},
	)
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolDisclosed, blockIndex: new(0), offset: 0, segmentLen: 13},
	})
	// The URI is not rendered anywhere, so a hit in it would be unreachable.
	if hits := Search([]vibekit.Message{m}, "haystack", false).Matches; len(hits) != 0 {
		t.Errorf("the disclosed URI matched %d times, want 0: %+v", len(hits), hits)
	}
}

// The fields fix 4 deliberately leaves unsearched, each for a stated reason, and
// each asserted where a needle in it would otherwise be indistinguishable from a
// gap. Chat.Name is NOT here — Search takes []vibekit.Message and cannot see a
// name — and has its own test at the layer that can (TestHandleSearch_ChatNameIsNotSearched).
func TestSearch_UnsearchedFieldsStayUnsearched(t *testing.T) {
	tests := []struct {
		name string
		why  string
		mut  func(m *vibekit.Message)
	}{
		{
			name: "ToolDiff.OldText",
			why:  "a line the edit REMOVED has no rendered surface in the card's mini-diff",
			mut: func(m *vibekit.Message) {
				m.ToolCalls[0].Diffs = []vibekit.ToolDiff{{Path: "f.go", OldText: "gone needle", NewText: "kept"}}
			},
		},
		{
			name: "the second diff",
			why:  "nothing renders or fetches Diffs[1], so a hit there is counted-but-unreachable",
			mut: func(m *vibekit.Message) {
				m.ToolCalls[0].Diffs = []vibekit.ToolDiff{
					{Path: "a.go", NewText: "first"},
					{Path: "b.go", NewText: "second needle"},
				}
			},
		},
		{
			name: "Attachment.Path",
			why:  "the path lives in a title ATTRIBUTE the DOM walker cannot mark",
			mut: func(m *vibekit.Message) {
				m.Attachments = []vibekit.Attachment{{Path: "needle/notes.md", Name: "notes.md"}}
			},
		},
		{
			name: "Denial.Capability",
			why:  "a closed vocabulary, not reader-facing text",
			mut: func(m *vibekit.Message) {
				m.ToolCalls[0].Denial = &vibekit.ToolDenial{Capability: "needle", Resource: "rm -rf /"}
			},
		},
		{
			name: "the denial rule's match patterns",
			why:  "policy text, reachable and editable through Settings -> Permissions",
			mut: func(m *vibekit.Message) {
				m.ToolCalls[0].Denial = &vibekit.ToolDenial{
					Resource: "rm -rf /",
					Rule: &vibekit.ToolDenialRule{
						Capability: "shell", Effect: "deny",
						Match: []string{"needle*"}, Exclude: []string{"needle-safe"},
					},
				}
			},
		},
		{
			name: "Disclosed.URI",
			why:  "the card renders the display name, never the uri",
			mut: func(m *vibekit.Message) {
				m.ToolCalls[0].Disclosed = &vibekit.ToolDisclosed{
					Type: "skill", DisplayName: "review", URI: "file:///needle/SKILL.md",
				}
			},
		},
		{
			name: "Message.CodeReferences",
			why:  "attributions are TURN-scoped; KAS drops the span that would locate one",
			mut: func(m *vibekit.Message) {
				m.CodeReferences = []vibekit.CodeReference{{LicenseName: "needle", Repository: "needle/repo"}}
			},
		},
		{
			name: "ToolCall.Locations[].Path",
			why:  "already reachable through the `file:` filter and through the title",
			mut: func(m *vibekit.Message) {
				m.ToolCalls[0].Locations = []vibekit.ToolLocation{{Path: "needle.go", Line: 12}}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := blockMsg("a1",
				[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
				vibekit.ToolCall{ID: "t1", Title: "Replace in File"},
			)
			tc.mut(&m)
			if hits := Search([]vibekit.Message{m}, "needle", false).Matches; len(hits) != 0 {
				t.Errorf("%s is searched (%d hits) but %s: %+v", tc.name, len(hits), tc.why, hits)
			}
		})
	}
}

// A tool call's segments come out in the order the CARD renders them, so
// stepping a card walks it the way a reader reads it — and bestHit's
// list-position tie-break is pinned to a stated order rather than to an accident
// of declaration.
//
// Red check: move any one arm of toolSegments past its neighbour — the diff below
// the output, the input above the diff, the denial below the input, the disclosed
// below the diff — and the expected order goes red.
func TestSearch_SegmentOrderFollowsTheRenderedCard(t *testing.T) {
	m := blockMsg("a1",
		[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
		vibekit.ToolCall{
			ID: "t1", Title: "grep needle", Output: "found a needle here",
			Disclosed: &vibekit.ToolDisclosed{Type: "skill", DisplayName: "the needle skill"},
			Denial:    &vibekit.ToolDenial{Capability: "shell", Resource: "a needle to deny"},
			Input:     json.RawMessage(`{"cmd":"a needle in the input"}`),
			Diffs:     []vibekit.ToolDiff{{Path: "fetch.go", NewText: "a needle in the diff"}},
		},
	)
	assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, []wantHit{
		{kind: SegmentToolTitle, blockIndex: new(0), offset: 5, segmentLen: 11},
		{kind: SegmentToolDisclosed, blockIndex: new(0), offset: 4, segmentLen: 16},
		{kind: SegmentToolDiff, blockIndex: new(0), offset: 2, segmentLen: 20},
		{kind: SegmentToolDenial, blockIndex: new(0), offset: 2, segmentLen: 16},
		{kind: SegmentToolInput, blockIndex: new(0), offset: 2, segmentLen: 21},
		{kind: SegmentToolOutput, blockIndex: new(0), offset: 8, segmentLen: 19},
	})
}

// A MESSAGE-level span comes after every block of the message that carries it,
// and the three arrive in one stated order: attachments, plan entries, the turn
// failure reason. Both message shapes end with the same tail, so a pre-blocks
// message is covered by the same producer.
//
// Red check: reorder the three loops in messageTailSegments, or drop the call
// from either messageSegments or legacySegments, and one of the two cases fails.
func TestSearch_MessageLevelSegmentsComeAfterTheBlocks(t *testing.T) {
	tail := func(m *vibekit.Message) {
		m.Attachments = []vibekit.Attachment{{Path: "a.md", Name: "needle-a"}}
		m.Plan = []vibekit.PlanEntry{{Content: "needle-plan", Status: vibekit.PlanPending}}
		m.TurnFailureReason = "needle-reason"
	}
	want := []wantHit{
		{kind: SegmentContent, blockIndex: new(0), offset: 0, segmentLen: 13},
		{kind: SegmentAttachment, offset: 0, segmentLen: 8},
		{kind: SegmentPlan, offset: 0, segmentLen: 11},
		{kind: SegmentTurnFailure, offset: 0, segmentLen: 13},
	}

	t.Run("block-bearing", func(t *testing.T) {
		m := blockMsg("a1", []vibekit.Block{{Type: vibekit.BlockText, Text: "needle-block!"}})
		tail(&m)
		assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, want)
	})

	t.Run("legacy blockless", func(t *testing.T) {
		m := vibekit.Message{ID: "a1", Role: vibekit.RoleAssistant, Ts: 1, Content: "needle-block!"}
		tail(&m)
		// The legacy content segment carries no block index; everything else matches.
		legacy := append([]wantHit(nil), want...)
		legacy[0].blockIndex = nil
		assertBlockHits(t, Search([]vibekit.Message{m}, "needle", false).Matches, legacy)
	})
}

// Every kind segmentKinds declares has a producer that can actually reach it.
// Read off the SAME slice the golden's kind loop reads, so a kind added without
// a producer fails here instead of passing both quietly.
//
// TWO queries, not one: segmentKinds includes SegmentMessage, which only a
// FILTER-ONLY query produces (appendMessageHits' q.text == "" branch), so a
// single free-text run can never satisfy this and its failure would read as a
// missing producer rather than a missing query.
//
// Red check: declare a const in segmentKinds with no producer and this fails.
func TestSearch_SegmentKindsAreExhaustive(t *testing.T) {
	msgs := searchContractMessages()
	seen := make(map[SegmentKind]int)
	for _, q := range []string{"retry", "role:assistant"} {
		hits := Search(msgs, q, false).Matches
		if len(hits) == 0 {
			t.Fatalf("Search(%q) found nothing; it can vouch for no kind at all", q)
		}
		for _, h := range hits {
			seen[h.SegmentKind]++
		}
	}
	for _, kind := range segmentKinds {
		if seen[kind] == 0 {
			t.Errorf("segment kind %q has no producer: a declared kind the client can never be handed", kind)
		}
	}
}

// A message persisted before blocks existed has no block array to address:
// its prose and thinking fall back to ONE content segment over the legacy
// content/reasoning concatenation (the existing searchable shape), and its
// tool calls keep their own segments — all without a block index.
func TestSearch_LegacyBlocklessMessageFallsBackToOneContentSegment(t *testing.T) {
	m := vibekit.Message{
		ID: "a1", Role: vibekit.RoleAssistant, Ts: 1,
		Content:   "prose needle",
		Reasoning: "thinking needle",
		ToolCalls: []vibekit.ToolCall{{ID: "t1", Title: "shell", Output: "output needle"}},
	}
	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	// "prose needle\nthinking needle" is one 28-rune segment.
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentContent, offset: 6, segmentLen: 28},
		{kind: SegmentContent, offset: 22, segmentLen: 28},
		{kind: SegmentToolOutput, offset: 7, segmentLen: 13},
	})
}

// Segment offsets and lengths count RUNES, not bytes, and are relative to the
// matched segment even when earlier segments hold multi-byte text.
func TestSearch_SegmentOffsetsAreRuneIndices(t *testing.T) {
	m := blockMsg("a1", []vibekit.Block{
		{Type: vibekit.BlockText, Text: "héllo wörld"},
		{Type: vibekit.BlockThinking, Thinking: "åß needle"},
	})
	hits := Search([]vibekit.Message{m}, "needle", false).Matches
	// "åß " is 3 runes (5 bytes); the whole segment is 9 runes (11 bytes).
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentReasoning, blockIndex: new(1), offset: 3, segmentLen: 9},
	})
}

// A filter-only query keeps its one-synthetic-hit-per-message contract, carried
// as segment_kind "message": offset 0, zero segment length, no block index —
// the hit locates the message, not a span inside it. A tool-only assistant
// message with empty prose is still listed.
func TestSearch_FilterOnlyHitsAreMessageKind(t *testing.T) {
	msgs := []vibekit.Message{
		blockMsg("a1", []vibekit.Block{{Type: vibekit.BlockText, Text: "prose here"}}),
		blockMsg("a2",
			[]vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
			vibekit.ToolCall{ID: "t1", Title: "shell", Output: "ran fine"},
		),
	}
	hits := Search(msgs, "role:assistant", false).Matches
	assertBlockHits(t, hits, []wantHit{
		{kind: SegmentMessage, offset: 0, segmentLen: 0},
		{kind: SegmentMessage, offset: 0, segmentLen: 0},
	})
	if hits[0].MessageID != "a1" || hits[1].MessageID != "a2" {
		t.Errorf("hits name %q and %q, want a1 and a2", hits[0].MessageID, hits[1].MessageID)
	}
	// The tool-only message still gets a usable excerpt.
	if hits[1].Excerpt == "" {
		t.Error("tool-only message got an empty excerpt")
	}
}

// block_index and agent_subtask_id are OPTIONAL on the generated type, and the
// encoder's half of that is omitting them when unset rather than writing null:
// the bytes the golden pins carry no null, so a message-kind hit must stay that
// way.
func TestHit_WireShape(t *testing.T) {
	full, err := json.Marshal(Hit{
		MessageID:      "m1",
		SegmentKind:    SegmentToolOutput,
		AgentSubtaskID: "sub-1",
		BlockIndex:     new(3),
		Offset:         8,
		SegmentLen:     19,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"segment_kind":"tool_output"`, `"agent_subtask_id":"sub-1"`, `"block_index":3`, `"segment_len":19`, `"offset":8`} {
		if !strings.Contains(string(full), key) {
			t.Errorf("marshalled hit %s lacks %s", full, key)
		}
	}
	minimal, err := json.Marshal(Hit{MessageID: "m1", SegmentKind: SegmentMessage})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"block_index", "agent_subtask_id"} {
		if strings.Contains(string(minimal), key) {
			t.Errorf("marshalled filter-only hit %s carries %s, want it omitted", minimal, key)
		}
	}
}
