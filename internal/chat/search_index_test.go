package chat

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// A filter that has filled up says yes to everything, which is the unindexed
// behaviour: a chat too large for its filter is read for every query, never
// skipped for one.
func TestChatFilter_SaturatedAdmitsEveryQuery(t *testing.T) {
	f := new(chatFilter)
	for i := range f.words {
		f.words[i] = ^uint64(0)
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for range 1000 {
		key := trigramKey(rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)))
		if !f.holdsAll([]uint64{key}) {
			t.Fatalf("a saturated filter rejected trigram key %#x", key)
		}
	}
}

// One trigram sets three positions and a lookup demands all three: clear any one
// of them and the trigram is rejected. Forcing the step odd is what keeps the
// three distinct, so the count is exact rather than at most three.
func TestChatFilter_ATrigramSetsAndDemandsThreePositions(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	for range 200 {
		key := trigramKey(rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)))
		f := new(chatFilter)
		f.add(key)
		var set []int
		for w, word := range f.words {
			for b := range 64 {
				if word&(1<<b) != 0 {
					set = append(set, w*64+b)
				}
			}
		}
		if len(set) != filterHashes {
			t.Fatalf("trigram key %#x set %d positions, want %d", key, len(set), filterHashes)
		}
		if !f.holdsAll([]uint64{key}) {
			t.Fatalf("trigram key %#x is not held by the filter it was added to", key)
		}
		for _, pos := range set {
			f.words[pos>>6] &^= 1 << (pos & 63)
			if f.holdsAll([]uint64{key}) {
				t.Fatalf("trigram key %#x held with position %d cleared", key, pos)
			}
			f.words[pos>>6] |= 1 << (pos & 63)
		}
	}
}

// Distinct trigrams never share a key, so a filter holding one rejects the three
// single-rune variants of it: the rune packing has to keep all three fields.
func TestChatFilter_DistinctTrigramsAreDistinctKeys(t *testing.T) {
	f := new(chatFilter)
	f.addText("abc")
	for _, variant := range []string{"xbc", "axc", "abx", "cba", "bca"} {
		if f.holdsAll(queryTrigrams(variant)) {
			t.Errorf("a filter over %q holds %q", "abc", variant)
		}
	}
	keys := map[uint64]bool{}
	for _, a := range []rune{'a', 'b', 0x10FFFF} {
		for _, b := range []rune{'a', 'b', 0x10FFFF} {
			for _, c := range []rune{'a', 'b', 0x10FFFF} {
				keys[trigramKey(a, b, c)] = true
			}
		}
	}
	if len(keys) != 27 {
		t.Errorf("27 distinct trigrams packed into %d keys", len(keys))
	}
}

// Under three runes there is no trigram to demand, so every chat is a candidate;
// at three the demand is one trigram of RUNES, whatever their byte lengths, and
// case is folded out of it. Invalid bytes in the query collapse per run before the
// fold, exactly as NewNeedle repairs them, so the filter is asked for the string
// the needle scans with and never for one the needle would not.
func TestQueryTrigrams_UnderThreeRunesDemandsNothing(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		wantN int
	}{
		{name: "empty", text: "", wantN: 0},
		{name: "one_rune", text: "a", wantN: 0},
		{name: "two_runes", text: "ab", wantN: 0},
		{name: "two_multibyte_runes", text: "é✓", wantN: 0},
		{name: "three_runes", text: "abc", wantN: 1},
		{name: "three_multibyte_runes", text: "é✓ß", wantN: 1},
		{name: "four_runes", text: "abcd", wantN: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(queryTrigrams(tc.text)); got != tc.wantN {
				t.Errorf("queryTrigrams(%q) has %d keys, want %d", tc.text, got, tc.wantN)
			}
		})
	}
	if a, b := queryTrigrams("AbC"), queryTrigrams("abc"); !slices.Equal(a, b) {
		t.Errorf("queryTrigrams(%q) = %v, want the folded %v", "AbC", a, b)
	}
	if a, b := queryTrigrams("a\xff\xfeb"), queryTrigrams("a\uFFFDb"); !slices.Equal(a, b) {
		t.Errorf("queryTrigrams(%q) = %v, want one U+FFFD per invalid run, %v", "a\xff\xfeb", a, b)
	}
}

// Every write path drops the chat's entry, so text written after a query is found
// by the next one: without the drop the stale filter would reject the new word
// and the chat would silently stop being searchable for it.
func TestSearchIndex_AWriteDropsTheEntryAndTheNextQueryFindsTheNewText(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChat(t, s, id, "Notes", []vibekit.Message{msg("m1", vibekit.RoleUser, "alpha bravo")})

	s.SearchAll(t.Context(), "alpha")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	if err := s.AppendMessage(t.Context(), id, &vibekit.Message{ID: "m2", Role: vibekit.RoleUser, Content: "zetaword"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, ok := s.index.lookup(id); ok {
		t.Error("the write left the chat's filter standing")
	}
	got := s.SearchAll(t.Context(), "zetaword")
	if got.Matched != 1 || len(got.Matches) != 1 || got.Matches[0].ID != id {
		t.Errorf("SearchAll(zetaword) after the write: Matched = %d, Matches = %+v, want the chat", got.Matched, got.Matches)
	}
}

// A delete drops the entry too, so the index never holds a filter for a file that
// is gone.
func TestSearchIndex_ADeleteDropsTheEntry(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChat(t, s, id, "Notes", []vibekit.Message{msg("m1", vibekit.RoleUser, "alpha bravo")})
	s.SearchAll(t.Context(), "alpha")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	if err := s.Delete(t.Context(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.index.lookup(id); ok {
		t.Error("the delete left the chat's filter standing")
	}
}

// Nothing is indexed at open: a store over an existing directory holds no filter
// until the first query, which then records one for EVERY chat it read, matching
// or not, and a second query reuses them rather than rebuilding.
func TestSearchIndex_IsBuiltByTheFirstQueryNotAtOpen(t *testing.T) {
	dir := t.TempDir()
	seeder, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ids := []string{"c-aaaaaaaa", "c-bbbbbbbb", "c-cccccccc"}
	for i, id := range ids {
		seedChatFileAt(t, seeder, id, "needle here", time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC))
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, id := range ids {
		if _, ok := s.index.lookup(vibekit.ChatID(id)); ok {
			t.Errorf("chat %s holds a filter before any query", id)
		}
	}

	s.SearchAll(t.Context(), "unrelatedword")
	first := make(map[string]*chatFilter, len(ids))
	for _, id := range ids {
		f, ok := s.index.lookup(vibekit.ChatID(id))
		if !ok {
			t.Errorf("chat %s holds no filter after the first query", id)
		}
		first[id] = f
	}

	s.SearchAll(t.Context(), "needle")
	for _, id := range ids {
		if f, _ := s.index.lookup(vibekit.ChatID(id)); f != first[id] {
			t.Errorf("chat %s was re-indexed by a second query over an unchanged file", id)
		}
	}
}

// A chat whose filter rejects the query is not opened, and the filter answers for
// it: it counts as scanned and leaves Truncated alone. The file is made
// undecodable AFTER its filter was recorded, which no writer of this store can do,
// so a read would report the chat unread; the query the filter rejects never
// makes that read, and the one it admits does.
func TestSearchAll_ARejectingFilterAnswersWithoutARead(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChat(t, s, id, "Notes", []vibekit.Message{msg("m1", vibekit.RoleUser, "alpha bravo")})
	s.SearchAll(t.Context(), "alpha")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}
	if err := os.WriteFile(filepath.Join(s.dir, id+chatFileSuffix), []byte("{not a chat"), 0o600); err != nil {
		t.Fatalf("corrupt chat file: %v", err)
	}

	pruned := s.SearchAll(t.Context(), "zetaword")
	if pruned.Scanned != 1 || pruned.Truncated || pruned.Matched != 0 {
		t.Errorf("SearchAll(zetaword) = scanned %d, truncated %t, matched %d; want 1, false, 0: the filter answered without a read",
			pruned.Scanned, pruned.Truncated, pruned.Matched)
	}
	admitted := s.SearchAll(t.Context(), "alpha")
	if admitted.Scanned != 0 || !admitted.Truncated {
		t.Errorf("SearchAll(alpha) = scanned %d, truncated %t; want 0, true: the filter admitted the chat and the read failed",
			admitted.Scanned, admitted.Truncated)
	}
	short := s.SearchAll(t.Context(), "zz")
	if short.Scanned != 0 || !short.Truncated {
		t.Errorf("SearchAll(zz) = scanned %d, truncated %t; want 0, true: under three runes every chat is read",
			short.Scanned, short.Truncated)
	}
}

// The filters are asked about the FREE text the needle scans with, never the raw
// query: a scoped filter's `role:user` token and the whitespace the parser
// collapses are not text any chat holds, and demanding their trigrams would prune
// every chat a scoped or loosely-typed query should match.
func TestSearchAll_AsksTheIndexAboutTheFreeTextOnly(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChat(t, s, id, "Notes", []vibekit.Message{msg("m1", vibekit.RoleUser, "we moved the cache to redis today")})
	s.SearchAll(t.Context(), "unrelatedword")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	for _, query := range []string{"redis role:user", "role:user", "redis   today", "  redis  "} {
		got := s.SearchAll(t.Context(), query)
		if got.Matched != 1 {
			t.Errorf("SearchAll(%q) matched %d chats, want 1: the filter must be asked about the free text alone", query, got.Matched)
		}
	}
}

// The index costs chats x 64 KiB and nothing else: one fixed-size filter per chat,
// sized as a power of two so a position is a mask rather than a division.
func TestChatFilter_MemoryIsSixtyFourKiBPerChat(t *testing.T) {
	if got := unsafe.Sizeof(chatFilter{}); got != 64<<10 {
		t.Errorf("sizeof(chatFilter) = %d, want %d", got, 64<<10)
	}
	if filterBits != filterBytes*8 || filterBits&(filterBits-1) != 0 {
		t.Errorf("filterBits = %d, want %d and a power of two", filterBits, filterBytes*8)
	}

	s, _ := newTestStore(t)
	ids := []string{"c-aaaaaaaa", "c-bbbbbbbb", "c-cccccccc"}
	for _, id := range ids {
		seedChat(t, s, id, "Notes", []vibekit.Message{msg("m1", vibekit.RoleUser, "alpha bravo")})
	}
	s.SearchAll(t.Context(), "alpha")
	s.index.mu.Lock()
	held := len(s.index.filters)
	s.index.mu.Unlock()
	if got, want := uintptr(held)*unsafe.Sizeof(chatFilter{}), uintptr(len(ids))*64<<10; got != want {
		t.Errorf("index over %d chats holds %d bytes of filters, want %d", len(ids), got, want)
	}
}
