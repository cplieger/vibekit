package chat

import (
	"strings"
	"sync"

	"github.com/cplieger/vibekit/internal/textsearch"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The cross-chat candidate index: one Bloom filter per chat over the rune
// trigrams of what the per-chat scan searches (the messageSegments spans plus
// the title, each through textsearch.Fold), so a query reads only the chats that
// can hold it. It PRUNES and never decides: a Bloom filter has no false
// negatives, so a chat holding the folded query holds every trigram and is
// always read, an admitted chat is scanned exactly as an unindexed one, and a
// saturated filter admits everything, which is the unindexed behaviour.
//
// In memory only, never persisted: a persisted index is a second store to keep
// in step with the chat files, and the directory listing is this store's one
// index. The store is those files' single writer, so writeChat and Remove drop a
// chat's entry under the per-chat lock they already hold and the next query
// rebuilds it from the read it makes anyway; nothing is built at boot, because a
// corpus read for a feature the session may never use is the wrong default.
// Memory is chats x filterBytes.

const (
	// filterBytes is one chat's filter, and so the index's cost per chat.
	filterBytes = 64 << 10
	filterWords = filterBytes / 8
	filterBits  = filterBytes * 8
	// filterHashes is the positions set per trigram, taken by double hashing
	// from the two halves of one 64-bit hash.
	filterHashes = 3
	trigramRunes = 3
)

// chatFilter is one chat's Bloom filter. Complete before it enters the index and
// never mutated after, so a lookup hands out a pointer read without any lock.
type chatFilter struct {
	words [filterWords]uint64
}

// buildChatFilter indexes one decoded chat: its title and every span the scan
// reads, folded as the scan folds them. A trigram never straddles two spans,
// because a hit never does.
func buildChatFilter(c *vibekit.Chat) *chatFilter {
	f := new(chatFilter)
	f.addText(c.Name)
	for i := range c.Messages {
		for _, seg := range messageSegments(&c.Messages[i]) {
			f.addText(seg.text)
		}
	}
	return f
}

// addText records every rune trigram of one folded span.
func (f *chatFilter) addText(s string) {
	var a, b rune
	seen := 0
	for _, r := range textsearch.Fold(s) {
		seen++
		if seen >= trigramRunes {
			f.add(trigramKey(a, b, r))
		}
		a, b = b, r
	}
}

// add sets the trigram's positions.
func (f *chatFilter) add(key uint64) {
	h1, h2 := splitHash(key)
	for i := range uint64(filterHashes) {
		pos := (h1 + i*h2) & (filterBits - 1)
		f.words[pos>>6] |= 1 << (pos & 63)
	}
}

// holdsAll reports whether every key's positions are set. No keys means no
// demand, so it holds: that is how a query under three runes makes every chat a
// candidate.
func (f *chatFilter) holdsAll(keys []uint64) bool {
	for _, key := range keys {
		h1, h2 := splitHash(key)
		for i := range uint64(filterHashes) {
			pos := (h1 + i*h2) & (filterBits - 1)
			if f.words[pos>>6]&(1<<(pos&63)) == 0 {
				return false
			}
		}
	}
	return true
}

// A rune is at most 21 bits, so three fit in one key with room to spare.
const (
	runeBits = 21
	runeMask = 1<<runeBits - 1
)

// trigramKey packs three runes into one key; distinct trigrams never share one.
func trigramKey(a, b, c rune) uint64 {
	return uint64(a&runeMask)<<(2*runeBits) | uint64(b&runeMask)<<runeBits | uint64(c&runeMask)
}

// splitHash is the one 64-bit hash of a trigram, split into the two halves the
// positions are derived from. The key is three packed fields, so it goes through
// a finalizer with full avalanche (splitmix64's) before the split, or the halves
// would carry the runes' own structure. The step is forced odd so the three
// positions can never collapse onto one.
func splitHash(key uint64) (h1, h2 uint64) {
	h := key
	h = (h ^ (h >> 30)) * 0xbf58476d1ce4e5b9
	h = (h ^ (h >> 27)) * 0x94d049bb133111eb
	h ^= h >> 31
	return h & (1<<32 - 1), h>>32 | 1
}

// queryTrigrams returns the trigram keys of the free text the needle will scan
// for, or nil when that text is under three runes.
//
// The text is prepared exactly as NewNeedle prepares it: invalid UTF-8 repaired
// to one U+FFFD per run BEFORE folding. Folding the raw text instead would give
// such a run one U+FFFD per byte, and a filter asked for a trigram the needle
// never scans with could reject a chat the scan matches.
func queryTrigrams(text string) []uint64 {
	runes := []rune(textsearch.Fold(strings.ToValidUTF8(text, "\uFFFD")))
	if len(runes) < trigramRunes {
		return nil
	}
	keys := make([]uint64, 0, len(runes)-trigramRunes+1)
	for i := range len(runes) - trigramRunes + 1 {
		keys = append(keys, trigramKey(runes[i], runes[i+1], runes[i+2]))
	}
	return keys
}

// searchIndex holds the filters by chat id. The zero value is ready to use.
type searchIndex struct {
	filters map[vibekit.ChatID]*chatFilter
	mu      sync.Mutex
}

func (x *searchIndex) lookup(id vibekit.ChatID) (*chatFilter, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f, ok := x.filters[id]
	return f, ok
}

func (x *searchIndex) put(id vibekit.ChatID, f *chatFilter) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.filters == nil {
		x.filters = make(map[vibekit.ChatID]*chatFilter)
	}
	x.filters[id] = f
}

func (x *searchIndex) drop(id vibekit.ChatID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.filters, id)
}
