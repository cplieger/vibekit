package textsearch

import (
	"iter"
	"strings"
	"unicode/utf8"
)

// Needle is a query prepared once per search.
type Needle struct {
	text          string
	runes         int
	caseSensitive bool
}

// NewNeedle prepares query for scanning any number of texts. A
// case-insensitive needle is folded here, once; an invalid byte in the query
// becomes U+FFFD in either mode, so a match can only start on a rune boundary
// of the text and every Hit addresses a whole rune.
func NewNeedle(query string, caseSensitive bool) Needle {
	query = strings.ToValidUTF8(query, "\uFFFD")
	if !caseSensitive {
		query = Fold(query)
	}
	return Needle{text: query, runes: utf8.RuneCountInString(query), caseSensitive: caseSensitive}
}

// Hit is one occurrence, addressed into the ORIGINAL text.
type Hit struct{ Rune, Byte int }

// Occurrences yields non-overlapping hits in text order. An empty needle yields
// nothing: it is no query, and strings.Index would report it at every position.
func (n Needle) Occurrences(text string) iter.Seq[Hit] {
	return func(yield func(Hit) bool) {
		if n.text == "" {
			return
		}
		hay := text
		if !n.caseSensitive {
			hay = Fold(text)
		}
		n.scan(hay, text, yield)
	}
}

// scan walks hay and text in parallel. Fold preserves rune count, so a rune
// distance measured in hay is the same distance in text; the byte cursor walks
// text by that many runes.
func (n Needle) scan(hay, text string, yield func(Hit) bool) {
	var hit Hit
	hp := 0
	for {
		at := strings.Index(hay[hp:], n.text)
		if at < 0 {
			return
		}
		gap := utf8.RuneCountInString(hay[hp : hp+at])
		hit.Byte = advanceRunes(text, hit.Byte, gap)
		hit.Rune += gap
		if !yield(hit) {
			return
		}
		hp += at + len(n.text)
		hit.Byte = advanceRunes(text, hit.Byte, n.runes)
		hit.Rune += n.runes
	}
}

// Count reports how many hits Occurrences would yield, without building them.
func (n Needle) Count(text string) int {
	c := 0
	for range n.Occurrences(text) {
		c++
	}
	return c
}

// Contains reports whether text holds at least one occurrence.
func (n Needle) Contains(text string) bool {
	for range n.Occurrences(text) {
		return true
	}
	return false
}

// advanceRunes returns the byte offset n runes past from in s.
func advanceRunes(s string, from, n int) int {
	for range n {
		_, w := utf8.DecodeRuneInString(s[from:])
		from += w
	}
	return from
}
