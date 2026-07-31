package mcp

import (
	"strconv"
	"strings"
	"testing"
)

// searchCacheKey's query component is untrusted HTTP input: handleSearch
// rejects control characters and caps the length, but ':' and '\' pass through.
// An ordinary query contains neither, so the encoded key must stay
// byte-identical to the plain "q:limit" concatenation — the property that makes
// this a pure encoding change with no cache-key churn for real searches.
func TestSearchCacheKey_ByteIdenticalForOrdinaryQueries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		q     string
		limit int
	}{
		{"", 20},
		{"github", 20},
		{"file system", 1},
		{"weather-api_v2", maxSearchLimit},
	}
	for _, tc := range cases {
		want := tc.q + ":" + strconv.Itoa(tc.limit)
		if got := searchCacheKey(tc.q, tc.limit); got != want {
			t.Errorf("searchCacheKey(%q, %d) = %q, want the plain colon join %q",
				tc.q, tc.limit, got, want)
		}
	}
}

// A query carrying ':' or '\' must not be able to spell another (query, limit)
// pair's key. The pre-keyenc "%s|%d" form happened to stay injective, but only
// because the one separator-bearing field sat FIRST and the trailing field was
// a clamped digit run — the boundary was recoverable as "the last '|'". That is
// an accident of field order and of `limit`'s alphabet, not a property of the
// key: the number of BOUNDARIES in the old key varied with untrusted input, so
// appending a third component or moving `limit` ahead of `q` would have made
// two distinct searches share one cache entry. keyenc escapes the query's
// reserved characters, so the key carries exactly one boundary whatever the
// query contains, and both assertions below hold irrespective of the clamp.
//
// Consequence of a collapse: one client is served the cached upstream body of a
// different search for up to registryCacheTTL, and because the entry is shared,
// so is every other client.
func TestSearchCacheKey_UntrustedQueryCannotForgeABoundary(t *testing.T) {
	t.Parallel()

	// Every limit here is inside the clamp handleSearch applies, so each case
	// is a reachable request.
	queries := []string{
		"",
		"q",
		"q:",
		":q",
		"q:5",
		"q:5:20",
		`q\`,
		`q\:5`,
		`q\\`,
		":",
		"::",
	}
	limits := []int{1, 5, 20, maxSearchLimit}

	keys := make(map[string]string, len(queries)*len(limits))
	for _, q := range queries {
		for _, limit := range limits {
			key := searchCacheKey(q, limit)
			// The query's reserved characters are escaped rather than emitted
			// as boundaries, so the key always has exactly one. Under the old
			// '|' form the boundary count was strings.Count(q, "|") + 1, i.e.
			// it varied with untrusted input.
			if n := unescapedSeparators(key); n != 1 {
				t.Errorf("searchCacheKey(%q, %d) = %q has %d unescaped separators, want 1",
					q, limit, key, n)
			}
			id := strconv.Quote(q) + "/" + strconv.Itoa(limit)
			if prev, dup := keys[key]; dup {
				t.Errorf("searchCacheKey collapsed %s and %s onto %q", prev, id, key)
				continue
			}
			keys[key] = id
		}
	}
}

// The query cap and the escaping interact: handleSearch bounds q at
// maxSearchQueryLen RAW bytes, and a fully-reserved query doubles under
// escaping. The key stays an escaped join (not a hashed identity) at that size,
// so the cache still holds one entry per distinct search rather than per hash.
func TestSearchCacheKey_MaxLengthQueryStaysAnEscapedJoin(t *testing.T) {
	t.Parallel()

	q := strings.Repeat(":", maxSearchQueryLen)
	key := searchCacheKey(q, maxSearchLimit)
	if strings.HasPrefix(key, "sha256:") {
		t.Fatalf("searchCacheKey for a %d-byte query returned a hashed identity %q", len(q), key)
	}
	if n := unescapedSeparators(key); n != 1 {
		t.Errorf("key %q has %d unescaped separators, want 1", key, n)
	}
	// A one-character-shorter query is a distinct search and a distinct key.
	if other := searchCacheKey(q[:len(q)-1], maxSearchLimit); other == key {
		t.Errorf("two queries of different length share the key %q", key)
	}
}

// unescapedSeparators counts the ':' bytes that act as component boundaries,
// i.e. those not preceded by an escape.
func unescapedSeparators(key string) int {
	n := 0
	escaped := false
	for i := range len(key) {
		switch {
		case escaped:
			escaped = false
		case key[i] == '\\':
			escaped = true
		case key[i] == ':':
			n++
		}
	}
	return n
}
