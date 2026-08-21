package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// seedChat writes one chat file into a store's dir.
func seedChat(t *testing.T, s *Store, id, name string, msgs []vibekit.Message) {
	t.Helper()
	ctx := t.Context()
	if err := s.Mutate(ctx, vibekit.ChatID(id), func(c *vibekit.Chat, _ bool) bool {
		c.Name = name
		c.Messages = msgs
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// TestScoreChat_TitleBeatsVolume is the ranking's whole purpose: a short chat
// whose TITLE names the subject must outrank a long one that merely mentions it
// many times. Without the title boost, volume wins and the useful result is
// buried.
func TestScoreChat_TitleBeatsVolume(t *testing.T) {
	titled := scoreChat(1, 1, 200, "")
	rambling := scoreChat(30, 0, 200_000, "")
	if titled <= rambling {
		t.Errorf("a titled match (%v) must outrank a long mention-heavy chat (%v)", titled, rambling)
	}
}

// TestScoreChat_TitleHitsMultiply pins the detail that differs from a boolean
// flag: naming the subject twice counts twice.
func TestScoreChat_TitleHitsMultiply(t *testing.T) {
	once := scoreChat(0, 1, 1000, "")
	twice := scoreChat(0, 2, 1000, "")
	if twice <= once {
		t.Errorf("two title hits (%v) must score above one (%v)", twice, once)
	}
}

// TestScoreChat_LengthNormalisesByChars is why the normaliser reads characters
// rather than message count: the same hit count in far more text is a weaker
// signal, and message count cannot see the difference.
func TestScoreChat_LengthNormalisesByChars(t *testing.T) {
	short := scoreChat(3, 0, 500, "")
	long := scoreChat(3, 0, 500_000, "")
	if short <= long {
		t.Errorf("the same hits in less text must score higher: short=%v long=%v", short, long)
	}
}

// TestScoreChat_TinyChatIsNotDividedByZero covers the `1 +` guard.
func TestScoreChat_TinyChatIsNotDividedByZero(t *testing.T) {
	got := scoreChat(1, 0, 0, "")
	if got <= 0 {
		t.Errorf("an empty-length chat must still score its content hits, got %v", got)
	}
}

// TestTitleHits_IgnoresFilterOnlyQueries: `file:x` names no title text, so it
// must not boost every chat whose name happens to contain "file".
func TestTitleHits_IgnoresFilterOnlyQueries(t *testing.T) {
	if n := titleHits("my file notes", "file:main.go"); n != 0 {
		t.Errorf("a filter-only query must not match a title, got %d", n)
	}
	if n := titleHits("Redis migration", "redis file:main.go"); n != 1 {
		t.Errorf("free text alongside a filter must still match, got %d", n)
	}
}

func TestSearchAll(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c-aaaaaaaa", "Redis migration", []vibekit.Message{
		msg("m1", vibekit.RoleUser, "we moved the cache to redis today"),
	})
	seedChat(t, s, "c-bbbbbbbb", "Grocery list", []vibekit.Message{
		msg("m2", vibekit.RoleUser, "nothing relevant here at all"),
	})
	ctx := t.Context()

	got := s.SearchAll(ctx, "redis")
	if len(got.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d (%+v)", len(got.Matches), got.Matches)
	}
	m := got.Matches[0]
	if m.ID != "c-aaaaaaaa" {
		t.Errorf("matched the wrong chat: %s", m.ID)
	}
	if m.Name != "Redis migration" {
		t.Errorf("match must carry the chat name for the row, got %q", m.Name)
	}
	if m.Hits < 1 {
		t.Errorf("match must report its hit count, got %d", m.Hits)
	}
	if m.Best.Excerpt == "" {
		t.Errorf("match must carry a best hit with an excerpt to show")
	}
	if got.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", got.Scanned)
	}
	if got.Truncated {
		t.Errorf("a 2-chat store must not report a truncated scan")
	}
}

// TestSearchAll_EmptyQuery must not fan out over every chat for nothing.
func TestSearchAll_EmptyQuery(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c-aaaaaaaa", "Redis", []vibekit.Message{msg("m1", vibekit.RoleUser, "redis")})
	for _, q := range []string{"", "   "} {
		got := s.SearchAll(t.Context(), q)
		if len(got.Matches) != 0 {
			t.Errorf("query %q returned %d matches", q, len(got.Matches))
		}
		if got.Scanned != 0 {
			t.Errorf("query %q scanned %d chats", q, got.Scanned)
		}
	}
}

// TestSearchAll_RanksTitleMatchFirst is the end-to-end form of the ranking test.
func TestSearchAll_RanksTitleMatchFirst(t *testing.T) {
	s, _ := newTestStore(t)
	// A genuinely long chat mentioning the word many times. The length has to be
	// REALISTIC (tens of KiB, like a real transcript) because the normaliser is
	// calibrated in KiB: at a few hundred characters it barely discounts, and
	// twenty mentions in a tiny document legitimately IS a strong signal.
	padding := strings.Repeat("context and discussion that surrounds the mention. ", 40)
	many := make([]vibekit.Message, 0, 20)
	for i := range 20 {
		many = append(many, msg(string(rune('a'+i)), vibekit.RoleUser,
			"some long passage mentioning redis in passing. "+padding))
	}
	seedChat(t, s, "c-bbbbbbbb", "Assorted debugging", many)
	seedChat(t, s, "c-aaaaaaaa", "Redis migration", []vibekit.Message{msg("m1", vibekit.RoleUser, "moved the cache")})

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) < 2 {
		t.Fatalf("expected both chats to match, got %+v", got.Matches)
	}
	if got.Matches[0].ID != "c-aaaaaaaa" {
		t.Errorf("the titled chat must rank first, got %s (scores: %v, %v)",
			got.Matches[0].ID, got.Matches[0].Score, got.Matches[1].Score)
	}
}

// TestSearchAll_IsAlwaysCaseInsensitive pins the DECISION behind the missing
// `case` parameter, so the History page's box is not given a toggle wired to
// nothing.
//
// searchOneChat states the reason: a cross-chat "which conversation was that in"
// is asked from memory, and memory does not remember capitalisation. The
// match-case toggle belongs to the in-chat search, which is a different question
// on a different endpoint (handleSearch, which DOES read `case`). Two halves have
// to hold for that to be true end to end — the body scan and the title boost —
// because titleHits folds independently of Search.
//
// If a future change adds a case parameter here, this test is the record of what
// it is overturning, and the client's toggle has to arrive in the same commit.
func TestSearchAll_IsAlwaysCaseInsensitive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		query string
		// wantChats is every chat id the query must return, whatever the casing.
		wantChats []string
	}{
		{name: "lowercase query", query: "redis", wantChats: []string{"c-aaaaaaaa", "c-bbbbbbbb"}},
		{name: "uppercase query", query: "REDIS", wantChats: []string{"c-aaaaaaaa", "c-bbbbbbbb"}},
		{name: "mixed-case query", query: "ReDiS", wantChats: []string{"c-aaaaaaaa", "c-bbbbbbbb"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestStore(t)
			// One chat matches only in its TITLE (the titleHits half), the other
			// only in its BODY (the Search half), and both are spelled in a
			// case the queries above disagree with.
			seedChat(t, s, "c-aaaaaaaa", "REDIS migration", []vibekit.Message{
				msg("m1", vibekit.RoleUser, "moved the cache over"),
			})
			seedChat(t, s, "c-bbbbbbbb", "Assorted notes", []vibekit.Message{
				msg("m2", vibekit.RoleUser, "we touched Redis in passing"),
			})

			got := s.SearchAll(t.Context(), tc.query)
			ids := make(map[string]bool, len(got.Matches))
			for i := range got.Matches {
				ids[string(got.Matches[i].ID)] = true
			}
			for _, want := range tc.wantChats {
				if !ids[want] {
					t.Errorf("query %q missed chat %s; matches: %+v", tc.query, want, got.Matches)
				}
			}
		})
	}
}

// TestSearchAll_TakesNoCaseArgument is the compile-time half of the decision.
//
// A signature test rather than a behaviour one, because the client's toggle is
// gated on the parameter's ABSENCE: the moment SearchAll grows one, a
// silently-ignored `?case=1` becomes possible and the History box must gain its
// `Aa` button in the same change. handleSearchAll forwards only `q`.
func TestSearchAll_TakesNoCaseArgument(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	// A NAMED type, so the assignment is a real assertion rather than an inferred
	// one: adding a third parameter to SearchAll stops it compiling here, and the
	// failure lands in this file rather than as a client toggle that does nothing.
	var f searchAllSignature = s.SearchAll
	if got := f(t.Context(), ""); len(got.Matches) != 0 {
		t.Errorf("empty query must return no matches, got %+v", got.Matches)
	}
}

// searchAllSignature is the shape handleSearchAll forwards to: a context and a
// query, and NO case flag.
type searchAllSignature func(context.Context, string) SearchAllResult
