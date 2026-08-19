package chat

import (
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// Transcript search.
//
// SERVER-SIDE, because the client cannot honestly search the chat. Its store is
// a paginated window, so a store-only search trades a DOM-shaped blind spot for
// a pagination-shaped one while still presenting itself as searching the
// conversation — and silently searching a window is the one unacceptable
// outcome, since it looks identical to searching everything right up to the
// moment it costs someone an answer.
//
// It is also the counterweight that makes progressive collapse acceptable at
// all: a collapse that hides content from search is a data-loss bug, not a
// polish item. The client's DOM walker had three blind spots (non-resident
// pages, resident rows whose `content-visibility: auto` reports invisible while
// rendering is skipped, and hidden or collapsed subtrees) and folding would have
// added a fourth. Enumerating here removes all four at once rather than patching
// the walker per case.
//
// NO INDEX, deliberately. A persistent inverted index would be exactly the
// second store this architecture exists to avoid, and it would need
// invalidation on every append. A linear scan over one chat is fast enough at
// this scale and is stateless, so there is nothing to keep in sync. Revisit only
// on a profile, and even then the answer is caching the projection rather than
// indexing it.
//
// LEXICAL, not semantic — specifically not `_kiro/knowledge`, which is
// workspace-global, indexes files on disk, and is granular at the document
// level. Pointing it at chat JSON would index vibekit's own serialisation
// format, pollute the user's code knowledge base with transcript noise, and
// still not answer "which turn".

// searchExcerptRadius is how much context surrounds a hit in its excerpt.
const searchExcerptRadius = 60

// maxSearchHits caps a response. A query matching every turn is a query the
// reader will refine, not page through, and an unbounded response on a
// thousand-turn chat is a wire cost paid for nothing.
const maxSearchHits = 200

// SearchHit locates one match. The client fetches only the turns it needs to
// reveal and highlights locally, so this carries position rather than markup.
type SearchHit struct {
	// MessageID is the matched message.
	MessageID string `json:"message_id"`
	// TurnMessageID is the matched turn's OPENING message id.
	//
	// Carried alongside MessageID because the two answer different questions and
	// the client needs both: a hit can land on an assistant message inside a
	// turn, while the fold state keys on the turn's opener. The turn NUMBER
	// cannot substitute — it is session-absolute here and window-relative in the
	// client's projection, so the two agree only when the whole session happens
	// to be resident.
	TurnMessageID string `json:"turn_message_id"`
	Excerpt       string `json:"excerpt"`
	// Role of the matched message, so a result list can say where a hit came
	// from without a second lookup.
	Role api.Role `json:"role"`
	// Turn is the 1-based session-absolute turn ordinal, matching
	// projectTurnSummaries so a hit can mark the timeline rail.
	Turn int `json:"turn"`
	// Offset is the rune index of the match within the searched text, so the
	// client can highlight the right occurrence rather than the first.
	Offset int `json:"offset"`
}

// searchQuery is a parsed query: scoped filters plus the free text.
//
// Filters make "the turn where you edited the composer" expressible, which a
// bare substring cannot do.
type searchQuery struct {
	text string
	file string
	tool string
	role string
	turn int
	// caseSensitive applies to the FREE TEXT only. The scoped filters stay
	// case-insensitive whatever the reader asked for: `role:` is an enum, and a
	// path filter that suddenly cared about case would be a behaviour change
	// nobody requested by ticking a box labelled "match case".
	caseSensitive bool
}

// parseSearchQuery splits `file:` / `tool:` / `role:` / `turn:` prefixes out of
// the raw query. Unknown prefixes stay in the free text rather than being
// dropped: a reader typing `http://` means it literally.
func parseSearchQuery(raw string, caseSensitive bool) searchQuery {
	q := searchQuery{turn: -1, caseSensitive: caseSensitive}
	var text []string
	for tok := range strings.FieldsSeq(raw) {
		name, val, ok := strings.Cut(tok, ":")
		if !ok || val == "" {
			text = append(text, tok)
			continue
		}
		switch strings.ToLower(name) {
		case "file":
			q.file = strings.ToLower(val)
		case "tool":
			q.tool = strings.ToLower(val)
		case "role":
			q.role = strings.ToLower(val)
		case "turn":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				q.turn = n
			} else {
				text = append(text, tok)
			}
		default:
			text = append(text, tok)
		}
	}
	q.text = strings.Join(text, " ")
	if !caseSensitive {
		q.text = strings.ToLower(q.text)
	}
	return q
}

// Search scans a chat's messages for a query.
//
// Turn numbers come from the same projection the timeline rail draws, so a hit's
// turn number and a rail marker's number are the same thing by construction
// rather than by two implementations agreeing.
//
// caseSensitive governs the FREE TEXT only (see searchQuery). Both halves of the
// in-chat search have to agree on it — the client highlights and counts in the
// DOM while this enumerates session-wide — so the flag travels on the request
// rather than being a server default either side could get wrong.
func Search(msgs []api.Message, raw string, caseSensitive bool) []SearchHit {
	q := parseSearchQuery(raw, caseSensitive)
	if q.text == "" && q.file == "" && q.tool == "" && q.role == "" && q.turn < 0 {
		return []SearchHit{}
	}
	turnOf, openerOf := turnIndexByMessage(msgs)
	hits := make([]SearchHit, 0, 16)
	for i := range msgs {
		m := &msgs[i]
		turn := turnOf[m.ID]
		if !messageMatchesFilters(m, &q, turn) {
			continue
		}
		hits = appendMessageHits(hits, m, &q, turn, openerOf[m.ID])
		if len(hits) >= maxSearchHits {
			return hits[:maxSearchHits]
		}
	}
	return hits
}

// turnIndexByMessage maps every message id to its turn's absolute ordinal, via
// the shared projection so numbering cannot disagree with the rail's.
func turnIndexByMessage(msgs []api.Message) (turns map[string]int, openers map[string]string) {
	turns = make(map[string]int, len(msgs))
	openers = make(map[string]string, len(msgs))
	summaries := projectTurnSummaries(msgs, false)
	// A summary carries its opening message id; walk the messages in order and
	// advance the turn whenever the next turn's opener is reached.
	next := 0
	current := 0
	opener := ""
	for i := range msgs {
		if next < len(summaries) && msgs[i].ID == summaries[next].ID {
			current = summaries[next].N
			opener = summaries[next].ID
			next++
		}
		turns[msgs[i].ID] = current
		openers[msgs[i].ID] = opener
	}
	return turns, openers
}

// messageMatchesFilters applies the scoped filters, all of which must hold.
func messageMatchesFilters(m *api.Message, q *searchQuery, turn int) bool {
	if q.turn >= 0 && turn != q.turn {
		return false
	}
	if q.role != "" && !strings.EqualFold(string(m.Role), q.role) {
		return false
	}
	if q.file != "" && !messageTouchesFile(m, q.file) {
		return false
	}
	if q.tool != "" && !messageUsesTool(m, q.tool) {
		return false
	}
	return true
}

// messageTouchesFile matches a substring against changed-file paths AND tool
// locations. Both, because a turn that only READ a file never appears in
// changed_files, and "the turn where you looked at auth.go" is a real question.
func messageTouchesFile(m *api.Message, want string) bool {
	for path := range m.ChangedFiles {
		if strings.Contains(strings.ToLower(path), want) {
			return true
		}
	}
	for i := range m.ToolCalls {
		for _, loc := range m.ToolCalls[i].Locations {
			if strings.Contains(strings.ToLower(loc.Path), want) {
				return true
			}
		}
	}
	return false
}

func messageUsesTool(m *api.Message, want string) bool {
	for i := range m.ToolCalls {
		tc := &m.ToolCalls[i]
		if strings.Contains(strings.ToLower(tc.Title), want) ||
			strings.Contains(strings.ToLower(string(tc.Kind)), want) {
			return true
		}
	}
	return false
}

// appendMessageHits adds every match within one message.
//
// A filter-only query (`file:auth.go` with no text) still yields one hit per
// matching message, so a scoped search without free text is a way to LIST turns
// rather than a query that finds nothing.
func appendMessageHits(hits []SearchHit, m *api.Message, q *searchQuery, turn int, opener string) []SearchHit {
	body := searchableText(m)
	if q.text == "" {
		return append(hits, SearchHit{
			MessageID:     m.ID,
			TurnMessageID: opener,
			Role:          m.Role,
			Turn:          turn,
			Offset:        0,
			Excerpt:       excerptAround([]rune(body), 0, 0),
		})
	}
	runes := []rune(body)
	// The haystack is folded only when the query was folded, so the two always
	// agree; parseSearchQuery owns the needle's side of that.
	hay := body
	if !q.caseSensitive {
		hay = strings.ToLower(body)
	}
	needle := q.text
	from := 0
	for {
		idx := strings.Index(hay[from:], needle)
		if idx < 0 {
			return hits
		}
		byteAt := from + idx
		runeAt := len([]rune(hay[:byteAt]))
		hits = append(hits, SearchHit{
			MessageID:     m.ID,
			TurnMessageID: opener,
			Role:          m.Role,
			Turn:          turn,
			Offset:        runeAt,
			Excerpt:       excerptAround(runes, runeAt, len([]rune(needle))),
		})
		if len(hits) >= maxSearchHits {
			return hits
		}
		from = byteAt + len(needle)
	}
}

// searchableText is everything in a message a reader could plausibly search:
// the prose, the thinking trace, and each tool call's title and output. Tool
// output matters — "which turn printed that error" is asked more often than
// "which turn mentioned it".
func searchableText(m *api.Message) string {
	var b strings.Builder
	b.Grow(len(m.Content) + len(m.Reasoning) + 64)
	b.WriteString(m.Content)
	if m.Reasoning != "" {
		b.WriteString("\n")
		b.WriteString(m.Reasoning)
	}
	for i := range m.ToolCalls {
		tc := &m.ToolCalls[i]
		b.WriteString("\n")
		b.WriteString(tc.Title)
		if tc.Output != "" {
			b.WriteString("\n")
			b.WriteString(tc.Output)
		}
	}
	return b.String()
}

// excerptAround returns the match plus surrounding context, with ellipses where
// it was cut. Rune-indexed so a multi-byte character is never split.
func excerptAround(runes []rune, at, length int) string {
	start := max(at-searchExcerptRadius, 0)
	end := min(at+length+searchExcerptRadius, len(runes))
	var b strings.Builder
	if start > 0 {
		b.WriteString("\u2026")
	}
	b.WriteString(strings.Join(strings.Fields(string(runes[start:end])), " "))
	if end < len(runes) {
		b.WriteString("\u2026")
	}
	return b.String()
}
