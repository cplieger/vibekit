package chat

import (
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
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

// SegmentKind identifies which span of a message a hit landed in, so the
// client can pick the right rendered surface before applying the offset.
type SegmentKind string

// Segment kinds. A tool block exposes its title and its output as SEPARATE
// segments sharing one block index, so the kind is what disambiguates their
// offsets.
const (
	SegmentContent    SegmentKind = "content"
	SegmentReasoning  SegmentKind = "reasoning"
	SegmentToolTitle  SegmentKind = "tool_title"
	SegmentToolOutput SegmentKind = "tool_output"
	// SegmentMessage is the filter-only kind: a query with filters and no free
	// text yields one synthetic hit per matching message, locating the message
	// rather than a span inside it (offset 0, zero segment length, no block).
	SegmentMessage SegmentKind = "message"
)

// SearchHit locates one match. The client fetches only the turns it needs to
// reveal and highlights locally, so this carries position rather than markup.
// Position is segment-relative: Offset indexes runes inside the one segment
// named by SegmentKind + BlockIndex, never a concatenation of the message.
//
// NOT wiregen-registered, deliberately: the TS mirror in chat-search.ts is
// hand-maintained (the generated namespace's SearchHit name is taken by the
// tools type), so field names here must match its spelling.
type SearchHit struct {
	// BlockIndex is the matched segment's block position in the message's
	// chronological Blocks array. Nil for messages persisted before blocks
	// existed and for message-kind hits. First for govet fieldalignment: a
	// pointer after the strings would extend the GC scan past their len words.
	BlockIndex *int `json:"block_index,omitempty"`
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
	Role vibekit.Role `json:"role"`
	// SegmentKind names the span the hit landed in: content | reasoning |
	// tool_title | tool_output, or message for a filter-only hit.
	SegmentKind SegmentKind `json:"segment_kind"`
	// AgentSubtaskID is the subtask id of the agent that produced the matched
	// segment ("" = top-level agent), so a hit inside a delegate's stream can
	// open that delegate's chain before highlighting.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	// Turn is the 1-based session-absolute turn ordinal, matching
	// projectTurnSummaries so a hit can mark the timeline rail.
	Turn int `json:"turn"`
	// Offset is the rune index of the match inside its segment, so the client
	// can highlight the right occurrence rather than the first.
	Offset int `json:"offset"`
	// SegmentLen is the segment's rune length: the denominator for a relative
	// position, carried so the client never re-derives the server's
	// segmentation. Zero for message-kind hits.
	SegmentLen int `json:"segment_len"`
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
func Search(msgs []vibekit.Message, raw string, caseSensitive bool) []SearchHit {
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
func turnIndexByMessage(msgs []vibekit.Message) (turns map[string]int, openers map[string]string) {
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
func messageMatchesFilters(m *vibekit.Message, q *searchQuery, turn int) bool {
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
func messageTouchesFile(m *vibekit.Message, want string) bool {
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

func messageUsesTool(m *vibekit.Message, want string) bool {
	for i := range m.ToolCalls {
		tc := &m.ToolCalls[i]
		if strings.Contains(strings.ToLower(tc.Title), want) ||
			strings.Contains(strings.ToLower(string(tc.Kind)), want) {
			return true
		}
	}
	return false
}

// appendMessageHits adds every match within one message, matching each of the
// message's segments independently — a hit's Offset and SegmentLen are the
// segment's, so a match can never span two segments.
//
// A filter-only query (`file:auth.go` with no text) still yields one hit per
// matching message, so a scoped search without free text is a way to LIST turns
// rather than a query that finds nothing. That hit locates the MESSAGE
// (SegmentMessage, offset 0, zero length, no block index), not a span in it.
func appendMessageHits(hits []SearchHit, m *vibekit.Message, q *searchQuery, turn int, opener string) []SearchHit {
	if q.text == "" {
		return append(hits, SearchHit{
			MessageID:     m.ID,
			TurnMessageID: opener,
			Role:          m.Role,
			Turn:          turn,
			SegmentKind:   SegmentMessage,
			Excerpt:       excerptAround([]rune(searchableText(m)), 0, 0),
		})
	}
	for _, seg := range messageSegments(m) {
		hits = appendSegmentHits(hits, m, q, turn, opener, &seg)
		if len(hits) >= maxSearchHits {
			return hits
		}
	}
	return hits
}

// appendSegmentHits adds every occurrence of the query text inside one segment.
func appendSegmentHits(hits []SearchHit, m *vibekit.Message, q *searchQuery, turn int, opener string, seg *segment) []SearchHit {
	runes := []rune(seg.text)
	// The haystack is folded only when the query was folded, so the two always
	// agree; parseSearchQuery owns the needle's side of that. Folding maps rune
	// to rune, so an index into the folded string is an index into the original.
	hay := seg.text
	if !q.caseSensitive {
		hay = strings.ToLower(seg.text)
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
			MessageID:      m.ID,
			TurnMessageID:  opener,
			Role:           m.Role,
			Turn:           turn,
			SegmentKind:    seg.kind,
			AgentSubtaskID: seg.subtaskID,
			BlockIndex:     seg.blockIndex,
			Offset:         runeAt,
			SegmentLen:     len(runes),
			Excerpt:        excerptAround(runes, runeAt, len([]rune(needle))),
		})
		if len(hits) >= maxSearchHits {
			return hits
		}
		from = byteAt + len(needle)
	}
}

// segment is one searchable span of a message: the unit a hit's offset is
// relative to.
type segment struct {
	// blockIndex is the owning block's position in Message.Blocks, nil on the
	// legacy blockless fallback. A tool block's title and output segments share
	// it; the kind is what tells their offsets apart.
	blockIndex *int
	kind       SegmentKind
	text       string
	subtaskID  string
}

// messageSegments lists a message's searchable spans in order. Block-bearing
// messages segment per block — the legacy Content/Reasoning fields mirror the
// block texts, so reading both would double every hit. Messages persisted
// before blocks existed fall back to one content segment over the legacy
// concatenation plus one title/output pair per tool call.
func messageSegments(m *vibekit.Message) []segment {
	if len(m.Blocks) == 0 {
		return legacySegments(m)
	}
	segs := make([]segment, 0, len(m.Blocks)+2)
	for i := range m.Blocks {
		b := &m.Blocks[i]
		switch b.Type {
		case vibekit.BlockText:
			segs = append(segs, segment{kind: SegmentContent, text: b.Text, subtaskID: b.AgentSubtaskID, blockIndex: new(i)})
		case vibekit.BlockThinking:
			segs = append(segs, segment{kind: SegmentReasoning, text: b.Thinking, subtaskID: b.AgentSubtaskID, blockIndex: new(i)})
		case vibekit.BlockToolUse:
			tc := toolCallByID(m, b.ToolCallID)
			if tc == nil {
				continue
			}
			segs = append(segs, toolSegments(tc, b.AgentSubtaskID, new(i))...)
		}
	}
	return segs
}

// legacySegments is the fallback for messages with no block array: the prose
// and thinking trace as ONE content segment over their concatenation — the
// shape searchableText always exposed, kept so legacy offsets stay stable —
// and each tool call's title/output pair, none of it block-addressed.
func legacySegments(m *vibekit.Message) []segment {
	text := m.Content
	if m.Reasoning != "" {
		text += "\n" + m.Reasoning
	}
	segs := make([]segment, 0, 1+2*len(m.ToolCalls))
	segs = append(segs, segment{kind: SegmentContent, text: text})
	for i := range m.ToolCalls {
		tc := &m.ToolCalls[i]
		segs = append(segs, toolSegments(tc, tc.AgentSubtaskID, nil)...)
	}
	return segs
}

// toolSegments is a tool call's title and output as separate segments sharing
// one block index. The title is always searchable; an empty output contributes
// no segment, mirroring what the pre-segment concatenation exposed.
func toolSegments(tc *vibekit.ToolCall, subtaskID string, blockIndex *int) []segment {
	segs := []segment{{kind: SegmentToolTitle, text: tc.Title, subtaskID: subtaskID, blockIndex: blockIndex}}
	if tc.Output != "" {
		segs = append(segs, segment{kind: SegmentToolOutput, text: tc.Output, subtaskID: subtaskID, blockIndex: blockIndex})
	}
	return segs
}

// toolCallByID resolves a tool_use block's reference into Message.ToolCalls.
func toolCallByID(m *vibekit.Message, id string) *vibekit.ToolCall {
	for i := range m.ToolCalls {
		if m.ToolCalls[i].ID == id {
			return &m.ToolCalls[i]
		}
	}
	return nil
}

// searchableText is everything in a message a reader could plausibly search,
// concatenated: the prose, the thinking trace, and each tool call's title and
// output. Matching runs per segment via messageSegments; this concatenation
// survives only as the excerpt source for filter-only hits, which locate the
// whole message and so preview its start whatever shape it is stored in.
func searchableText(m *vibekit.Message) string {
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
