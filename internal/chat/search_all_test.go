package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// seedChat writes one chat file into a store's dir.
func seedChat(t *testing.T, s *Store, id, name string, msgs []vibekit.Message) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.Mutate(ctx, vibekit.ChatID(id), func(c *vibekit.Chat, _ bool) bool {
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
	titled := scoreChat(1, 1, 200)
	rambling := scoreChat(30, 0, 200_000)
	if titled <= rambling {
		t.Errorf("a titled match (%v) must outrank a long mention-heavy chat (%v)", titled, rambling)
	}
}

// TestScoreChat_TitleHitsMultiply pins the detail that differs from a boolean
// flag: naming the subject twice counts twice.
func TestScoreChat_TitleHitsMultiply(t *testing.T) {
	once := scoreChat(0, 1, 1000)
	twice := scoreChat(0, 2, 1000)
	if twice <= once {
		t.Errorf("two title hits (%v) must score above one (%v)", twice, once)
	}
}

// TestScoreChat_LengthNormalisesByChars is why the normaliser reads characters
// rather than message count: the same hit count in far more text is a weaker
// signal, and message count cannot see the difference.
func TestScoreChat_LengthNormalisesByChars(t *testing.T) {
	short := scoreChat(3, 0, 500)
	long := scoreChat(3, 0, 500_000)
	if short <= long {
		t.Errorf("the same hits in less text must score higher: short=%v long=%v", short, long)
	}
}

// TestScoreChat_TinyChatIsNotDividedByZero covers the `1 +` guard.
func TestScoreChat_TinyChatIsNotDividedByZero(t *testing.T) {
	got := scoreChat(1, 0, 0)
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
	if m.Best == nil || m.Best.Excerpt == "" {
		t.Errorf("match must carry a best hit with an excerpt to show, got %+v", m.Best)
	}
	if got.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", got.Scanned)
	}
	if got.Matched != 1 {
		t.Errorf("Matched = %d, want 1", got.Matched)
	}
	if got.Truncated {
		t.Errorf("a 2-chat store must not report a truncated scan")
	}
}

// A row's hit count and its score spend EVERY occurrence in the chat, not the
// length of the capped hit list the in-chat scan carries. A chat with 250 mentions
// outranks one with 200 of the same text; clamped at 200 the two would tie on
// count and the shorter chat would win on volume, and the busier row would read
// "and 199 more" for a chat holding 249. The best hit is untouched by the cap: the
// list is in message order, so the earliest turn's hit is always inside it.
func TestSearchAll_HitsCountEveryOccurrencePastTheHitCap(t *testing.T) {
	s, _ := newTestStore(t)
	seedMentions := func(id string, n int) {
		msgs := make([]vibekit.Message, 0, n)
		for i := range n {
			msgs = append(msgs, msg(fmt.Sprintf("%s-%03d", id, i), vibekit.RoleUser, "needle here"))
		}
		seedChat(t, s, id, "seeded", msgs)
	}
	busier := maxSearchHits + 50
	seedMentions("c-aaaaaaaa", busier)
	seedMentions("c-bbbbbbbb", maxSearchHits)

	got := s.SearchAll(t.Context(), "needle")
	if len(got.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d (%+v)", len(got.Matches), got.Matches)
	}
	first, second := got.Matches[0], got.Matches[1]
	if first.ID != "c-aaaaaaaa" {
		t.Errorf("the chat with %d mentions must outrank the one with %d, got %s first (scores %v, %v)",
			busier, maxSearchHits, first.ID, first.Score, second.Score)
	}
	if first.Hits != busier {
		t.Errorf("Hits = %d, want %d (every occurrence, not the %d-hit list)", first.Hits, busier, maxSearchHits)
	}
	if second.Hits != maxSearchHits {
		t.Errorf("Hits = %d, want %d", second.Hits, maxSearchHits)
	}
	if first.Best == nil || first.Best.Turn != 1 {
		t.Errorf("Best = %+v, want the hit in turn 1, the earliest", first.Best)
	}
}

// The score's denominator is the text the scan READ. A chat whose reasoning trace
// dwarfs its prose is normalised by that trace: one mention in far more searched
// text is the weaker signal, whichever segment kind holds the bulk. Two chats with
// the same prose and the same single mention would otherwise tie and fall through
// to recency, which is what the newer, more verbose one is seeded to win.
func TestSearchAll_RankingDenominatorCoversEverySearchedSpan(t *testing.T) {
	s, _ := newTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prose := "we moved the cache to redis"
	verbose := &vibekit.Chat{
		ID:        "c-aaaaaaaa",
		Name:      "Verbose",
		UpdatedAt: base.Add(time.Hour).UnixMilli(),
		Messages: []vibekit.Message{{
			ID: "a1", Role: vibekit.RoleAssistant, Content: prose,
			Blocks: []vibekit.Block{
				{Type: vibekit.BlockThinking, Thinking: strings.Repeat("weighing the options. ", 2000)},
				{Type: vibekit.BlockText, Text: prose},
			},
		}},
	}
	terse := &vibekit.Chat{
		ID:        "c-bbbbbbbb",
		Name:      "Terse",
		UpdatedAt: base.UnixMilli(),
		Messages: []vibekit.Message{{
			ID: "b1", Role: vibekit.RoleAssistant, Content: prose,
			Blocks: []vibekit.Block{{Type: vibekit.BlockText, Text: prose}},
		}},
	}
	seedChatFile(t, s, verbose, base.Add(time.Hour))
	seedChatFile(t, s, terse, base)

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d (%+v)", len(got.Matches), got.Matches)
	}
	first, second := got.Matches[0], got.Matches[1]
	if first.ID != "c-bbbbbbbb" {
		t.Errorf("the terse chat must outrank the verbose one, got %s first (scores %v, %v)",
			first.ID, first.Score, second.Score)
	}
	if first.Score <= second.Score {
		t.Errorf("scores %v and %v: the verbose chat's reasoning must count against it, not tie",
			first.Score, second.Score)
	}
}

// A chat the reader refuses is NOT scanned. Scanned is the number the History page
// prints beside "no matches", so counting a chat whose contents were never read
// tells the reader their text is in none of N conversations when one of the N was
// skipped. The refusal sets Truncated instead, which is the fact the copy beside
// that count is worded for.
func TestSearchAll_AnUnreadChatIsNotScanned(t *testing.T) {
	const capBytes = 4096
	tests := []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{
			name:  "over_the_file_cap",
			plant: func(t *testing.T, path string) { writeOversizeChat(t, path, capBytes) },
		},
		{
			name: "undecodable",
			plant: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{not a chat"), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newCappedTestStore(t, capBytes)
			seedChat(t, s, "c-aaaaaaaa", "Redis migration", []vibekit.Message{
				msg("m1", vibekit.RoleUser, "we moved the cache to redis today"),
			})
			seedChat(t, s, "c-bbbbbbbb", "Grocery list", []vibekit.Message{
				msg("m2", vibekit.RoleUser, "nothing relevant here at all"),
			})
			tc.plant(t, filepath.Join(s.dir, "c-cccccccc"+chatFileSuffix))

			got := s.SearchAll(t.Context(), "redis")

			if got.Scanned != 2 {
				t.Errorf("Scanned = %d, want 2: the unread chat is not among the chats the scan read", got.Scanned)
			}
			if !got.Truncated {
				t.Error("Truncated = false with a chat left unread, want true")
			}
			if got.Matched != 1 || len(got.Matches) != 1 || got.Matches[0].ID != "c-aaaaaaaa" {
				t.Errorf("Matched = %d, Matches = %+v, want the one readable match", got.Matched, got.Matches)
			}
		})
	}
}

// A chat matched on its NAME alone has no line inside the transcript, so its
// row carries no best hit rather than a zero one: a zero hit would claim a
// segment kind of "" on the wire, which the generated decoder refuses.
func TestSearchAll_TitleOnlyMatchCarriesNoBestHit(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c-aaaaaaaa", "Redis migration", []vibekit.Message{
		msg("m1", vibekit.RoleUser, "we moved the cache today"),
	})

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d (%+v)", len(got.Matches), got.Matches)
	}
	if m := got.Matches[0]; m.Best != nil || m.Hits != 0 {
		t.Errorf("title-only match = %+v, want no best hit and zero hits", m)
	}
}

// The result LIST is cut at maxChatResults and the COUNT is not, so a reader
// shown 50 rows out of 51 is told 51. Truncated stays false: every chat was read,
// and the cut is Matched exceeding the list.
func TestSearchAll_MatchedCountsPastTheResultCap(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range maxChatResults + 1 {
		seedChatFileAt(t, s, fmt.Sprintf("chat-%03d", i), "needle", base.Add(time.Duration(i)*time.Minute))
	}

	got := s.SearchAll(t.Context(), "needle")
	if len(got.Matches) != maxChatResults {
		t.Errorf("len(Matches) = %d, want %d (the cap)", len(got.Matches), maxChatResults)
	}
	if got.Matched != maxChatResults+1 {
		t.Errorf("Matched = %d, want %d (every matching chat, cut or not)", got.Matched, maxChatResults+1)
	}
	if got.Scanned != maxChatResults+1 {
		t.Errorf("Scanned = %d, want %d", got.Scanned, maxChatResults+1)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false: every chat was read and the cut is Matched > len(Matches)")
	}
}

// A cancelled cross-chat scan may not claim it read every chat.
//
// `Scanned` is the number the UI prints ("no matches across N conversations"), so
// a short scan reporting the OFFERED count tells the reader their text is nowhere
// when most of their chats were never opened. `Truncated` already means "the
// answer is short", which is what the copy beside that count is worded for, so the
// cancelled case rides it instead of adding a wire field.
//
// WHAT THIS PINS is the entry-collection half: an already-cancelled context cuts
// `chatEntries` short, and without its `truncated = true` the empty list takes
// SearchAll's own early return and publishes `{matches: [], scanned: 0,
// truncated: false}` — an authoritative "your text is in none of your chats" for a
// scan that opened none of them.
//
// WHAT IT DOES NOT PIN is the fan-out half (`scanned < len(entries)`), and that is
// stated rather than papered over: reaching it needs the context to die BETWEEN
// chatEntries returning a full list and Bounded draining it, which no
// public-API test can schedule. Cancel earlier and the collection loop reports the
// truncation first; cancel later and there is nothing to cut. The branch is a
// defence for that real window, `parallel.Bounded`'s own tests pin the count it
// reads, and a production seam to make it reachable would be worse than the gap.
func TestSearchAll_CancelledCollectionIsNotAnEmptyAnswer(t *testing.T) {
	s, _ := newTestStore(t)
	for _, id := range []string{"c-aaaaaaaa", "c-bbbbbbbb", "c-cccccccc"} {
		seedChat(t, s, id, "Redis migration", []vibekit.Message{
			msg("m1", vibekit.RoleUser, "we moved the cache to redis today"),
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got := s.SearchAll(ctx, "redis")

	if got.Scanned != 0 {
		t.Errorf("Scanned = %d over a scan whose collection was cancelled before it "+
			"read anything, want 0", got.Scanned)
	}
	if !got.Truncated {
		t.Error("Truncated = false after a cancelled collection; the reader is owed " +
			"the fact that their chats went unread")
	}
	if len(got.Matches) != 0 {
		t.Errorf("Matches = %d from a scan that opened nothing", len(got.Matches))
	}
}

// A store directory that cannot be listed read nothing it was asked to, so the
// reply is truncated rather than an authoritative "in none of your chats".
//
// The directory is stood in for by a regular file, so ReadDir fails with ENOTDIR
// under any uid: a permission refusal would not reproduce as root.
func TestSearchAll_UnlistableDirIsNotAnEmptyAnswer(t *testing.T) {
	s, _ := newTestStore(t)
	seedChat(t, s, "c-aaaaaaaa", "Redis migration", []vibekit.Message{
		msg("m1", vibekit.RoleUser, "we moved the cache to redis today"),
	})
	notADir := filepath.Join(t.TempDir(), "chats")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write stand-in: %v", err)
	}
	s.dir = notADir

	got := s.SearchAll(t.Context(), "redis")

	if len(got.Matches) != 0 || got.Scanned != 0 || got.Matched != 0 {
		t.Errorf("SearchAll over an unlistable dir = %d matches, scanned %d, matched %d; want 0/0/0",
			len(got.Matches), got.Scanned, got.Matched)
	}
	if !got.Truncated {
		t.Error("Truncated = false over a directory the scan could not list, want true")
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

// The ranking formula, at exact values. Every other score test compares two
// scores, which a formula that returns NaN for every input satisfies: NaN is
// neither greater nor smaller, so an ordering assertion passes on garbage.
// Inputs are chosen so the normaliser is exact in binary: docChars of 3 KiB
// gives sqrt(1+3) = 2.
func TestScoreChat_MatchesTheDocumentedFormula(t *testing.T) {
	tests := []struct {
		name        string
		contentHits int
		titleHits   int
		docChars    int
		want        float64
	}{
		{name: "content_hits_divided_by_the_normaliser", contentHits: 2, titleHits: 0, docChars: 3 * 1024, want: 1},
		{name: "title_hits_multiplied_by_the_boost", contentHits: 0, titleHits: 3, docChars: 0, want: 30},
		{name: "both_terms_added", contentHits: 2, titleHits: 1, docChars: 3 * 1024, want: 11},
		{name: "no_hits_at_all", contentHits: 0, titleHits: 0, docChars: 3 * 1024, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreChat(tc.contentHits, tc.titleHits, tc.docChars)
			if got != tc.want {
				t.Errorf("scoreChat(%d, %d, %d) = %v, want %v",
					tc.contentHits, tc.titleHits, tc.docChars, got, tc.want)
			}
		})
	}
}

// The row shows the EARLIEST hit, which is where the conversation first touches
// the subject. Ties within one turn keep the first hit found, so the excerpt a
// result row shows does not move around between searches.
func TestBestHit_PicksTheEarliestTurnAndKeepsTheFirstOfATie(t *testing.T) {
	tests := []struct {
		name string
		hits []Hit
		want string
	}{
		{
			name: "lowest_turn_wins_whatever_the_order",
			hits: []Hit{
				{MessageID: "c", Turn: 3},
				{MessageID: "a", Turn: 1},
				{MessageID: "b", Turn: 2},
			},
			want: "a",
		},
		{
			name: "a_tie_keeps_the_first",
			hits: []Hit{
				{MessageID: "first", Turn: 2},
				{MessageID: "second", Turn: 2},
			},
			want: "first",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bestHit(tc.hits).MessageID; got != tc.want {
				t.Errorf("bestHit(%+v).MessageID = %q, want %q", tc.hits, got, tc.want)
			}
		})
	}
}

// seedChatFileAt writes one one-message chat file verbatim, bypassing Mutate so a
// seeding loop over hundreds of chats costs a file write each and no broadcast.
func seedChatFileAt(t *testing.T, s *Store, id, body string, mtime time.Time) {
	t.Helper()
	seedChatFile(t, s, &vibekit.Chat{
		ID:       id,
		Name:     "seeded",
		Messages: []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: body}},
	}, mtime)
}

// seedChatFile writes one chat record verbatim, stamped with a chosen mtime.
func seedChatFile(t *testing.T, s *Store, c *vibekit.Chat, mtime time.Time) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal chat %s: %v", c.ID, err)
	}
	path := filepath.Join(s.dir, c.ID+chatFileSuffix)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write chat %s: %v", c.ID, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("stamp chat %s: %v", c.ID, err)
	}
}

// Every chat is a candidate, however many there are and however old. The oldest
// hundred of six hundred chats carry a word the rest do not; all six hundred are
// scanned, the word is found, and nothing is reported unread. The fixture puts the
// old chats at MIDDLE filename positions so the verdict cannot ride on how a
// directory listing happens to be ordered.
func TestSearchAll_ReadsEveryChatHoweverMany(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const chats, old = 600, 100
	for i := range chats {
		body, mtime := "recentword", base.Add(time.Duration(1000+i)*time.Minute)
		if i >= 250 && i < 250+old {
			body, mtime = "stalewordxyz", base.Add(time.Duration(i-250)*time.Minute)
		}
		seedChatFileAt(t, s, fmt.Sprintf("chat-%03d", i), body, mtime)
	}

	got := s.SearchAll(t.Context(), "stalewordxyz")

	if got.Matched != old {
		t.Errorf("SearchAll(stalewordxyz) over %d chats: Matched = %d, want %d (the oldest hundred)", chats, got.Matched, old)
	}
	if got.Scanned != chats {
		t.Errorf("SearchAll(stalewordxyz) over %d chats: Scanned = %d, want %d", chats, got.Scanned, chats)
	}
	if got.Truncated {
		t.Errorf("SearchAll(stalewordxyz) over %d chats: Truncated = true, want false: every chat was read", chats)
	}
}
