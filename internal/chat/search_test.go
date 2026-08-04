package chat

import (
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

func TestSearchChat_FindsTextAndNamesItsTurn(t *testing.T) {
	hits := SearchChat(transcript(), "composer")
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

func TestSearchChat_IsCaseInsensitive(t *testing.T) {
	if len(SearchChat(transcript(), "COMPOSER")) == 0 {
		t.Error("an upper-case query found nothing")
	}
}

func TestSearchChat_ReportsEveryOccurrenceInOneMessage(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "retry retry retry")}
	hits := SearchChat(msgs, "retry")
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

func TestSearchChat_OffsetsAreRuneIndicesNotBytes(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "héllo wörld needle")}
	hits := SearchChat(msgs, "needle")
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if want := len([]rune("héllo wörld ")); hits[0].Offset != want {
		t.Errorf("offset = %d, want %d (rune index, not byte)", hits[0].Offset, want)
	}
}

func TestSearchChat_EmptyQueryFindsNothing(t *testing.T) {
	for _, q := range []string{"", "   "} {
		if got := SearchChat(transcript(), q); len(got) != 0 {
			t.Errorf("query %q returned %d hits", q, len(got))
		}
	}
}

// An empty result must marshal as [] rather than null, so the client has one
// empty case instead of two.
func TestSearchChat_NeverReturnsNil(t *testing.T) {
	if SearchChat(transcript(), "") == nil {
		t.Error("empty query returned a nil slice")
	}
	if SearchChat(nil, "anything") == nil {
		t.Error("empty transcript returned a nil slice")
	}
}

func TestSearchChat_SearchesReasoningAndToolOutput(t *testing.T) {
	msgs := []api.Message{
		{
			ID: "a1", Role: api.RoleAssistant, Ts: 1,
			Reasoning: "considering a mutex here",
			ToolCalls: []api.ToolCall{{ID: "t1", Title: "shell", Output: "permission denied"}},
		},
	}
	// "which turn printed that error" is asked more often than "which turn
	// mentioned it", so tool output is searchable.
	if len(SearchChat(msgs, "permission denied")) == 0 {
		t.Error("tool output is not searchable")
	}
	if len(SearchChat(msgs, "mutex")) == 0 {
		t.Error("the thinking trace is not searchable")
	}
}

func TestSearchChat_ScopedFilters(t *testing.T) {
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
		for _, h := range SearchChat(msgs, "role:user") {
			if h.Role != api.RoleUser {
				t.Errorf("role:user returned a %s message", h.Role)
			}
		}
		if len(SearchChat(msgs, "role:user")) != 2 {
			t.Errorf("role:user matched %d, want 2", len(SearchChat(msgs, "role:user")))
		}
	})

	t.Run("turn", func(t *testing.T) {
		hits := SearchChat(msgs, "turn:2")
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
		if got := SearchChat(msgs, "file:token.go"); len(got) != 1 || got[0].MessageID != "a1" {
			t.Errorf("file:token.go = %+v, want the reading turn", got)
		}
		if got := SearchChat(msgs, "file:composer.ts"); len(got) != 1 || got[0].MessageID != "a2" {
			t.Errorf("file:composer.ts = %+v, want the writing turn", got)
		}
	})

	t.Run("tool matches title or kind", func(t *testing.T) {
		if len(SearchChat(msgs, "tool:readFile")) != 1 {
			t.Error("tool:readFile did not match by title")
		}
		if len(SearchChat(msgs, "tool:read")) != 1 {
			t.Error("tool:read did not match by kind")
		}
	})

	t.Run("filters combine", func(t *testing.T) {
		if got := SearchChat(msgs, "role:user turn:1"); len(got) != 1 || got[0].MessageID != "u1" {
			t.Errorf("combined filters = %+v", got)
		}
		// A filter that excludes everything returns nothing rather than ignoring
		// itself.
		if got := SearchChat(msgs, "role:user turn:99"); len(got) != 0 {
			t.Errorf("impossible combination returned %d hits", len(got))
		}
	})

	t.Run("filter plus free text", func(t *testing.T) {
		if got := SearchChat(msgs, "role:user composer"); len(got) != 1 || got[0].MessageID != "u2" {
			t.Errorf("filter+text = %+v", got)
		}
	})
}

// A reader typing a URL means it literally, so an unknown prefix stays text.
func TestSearchChat_UnknownPrefixStaysFreeText(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "see https://example.com for more")}
	if len(SearchChat(msgs, "https://example.com")) == 0 {
		t.Error("a colon-bearing term was parsed as a filter and lost")
	}
}

func TestSearchChat_NonNumericTurnStaysFreeText(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, "the turn:abc marker")}
	if len(SearchChat(msgs, "turn:abc")) == 0 {
		t.Error("an unparseable turn filter should fall back to text")
	}
}

func TestSearchChat_ExcerptCarriesContextAndCollapsesWhitespace(t *testing.T) {
	long := strings.Repeat("a ", 100) + "needle " + strings.Repeat("b ", 100)
	hits := SearchChat([]api.Message{msg("u1", api.RoleUser, long)}, "needle")
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

func TestSearchChat_CapsTheResultCount(t *testing.T) {
	msgs := []api.Message{msg("u1", api.RoleUser, strings.Repeat("hit ", maxSearchHits+50))}
	if got := len(SearchChat(msgs, "hit")); got != maxSearchHits {
		t.Errorf("got %d hits, want the cap of %d", got, maxSearchHits)
	}
}

// The turn numbers a hit reports and the ones the rail draws come from the same
// projection, so they cannot disagree by construction.
func TestSearchChat_TurnNumbersMatchTheRailProjection(t *testing.T) {
	msgs := transcript()
	summaries := api.ProjectTurnSummaries(msgs, false)
	byOpener := make(map[string]int, len(summaries))
	for _, s := range summaries {
		byOpener[s.ID] = s.N
	}
	for _, h := range SearchChat(msgs, "the") {
		if byOpener[h.TurnMessageID] != h.Turn {
			t.Errorf("hit %+v disagrees with the projection (%d)", h, byOpener[h.TurnMessageID])
		}
	}
}
