package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/textsearch"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Transcript search: the in-chat scan.
//
// SERVER-SIDE, because the client's store is a paginated window: a store-only
// search would cover the resident tail while presenting itself as the whole
// conversation. It is also what makes progressive collapse acceptable, which
// takes both halves of the reply: the tally is the COUNT and the hit list is the
// client's STEP LIST (find-in-chat.ts's `stepOrder`), so a hit inside a
// collapsed box is reported and reachable.
//
// MATCHING is textsearch's; this file owns the segmentation (which spans are
// searched, in what order), the scoped filters, and what travels beside a hit.
// This scan has NO INDEX: it runs over one record already in memory, where a
// linear pass is the whole cost (the cross-chat candidate index is
// search_index.go's). LEXICAL, not `_kiro/knowledge`, which is workspace-global
// and file-granular and could not answer "which turn".

// searchExcerptRadius is how much context surrounds a hit in its excerpt.
//
// The client's ranker slices the SAME radius out of the rendered text before
// comparing it against this excerpt (find-in-chat.ts's EXCERPT_RADIUS), so the two
// sides of that similarity score span the same amount of context. Nothing on the
// wire carries the number and neither side is generated, so the pair is held
// together by static-src/chat-search.node.test.ts, which reads both files as text.
const searchExcerptRadius = 60

// maxSearchHits caps the hit LIST. A query matching every turn is a query the
// reader will refine, not page through, and an unbounded response on a
// thousand-turn chat is a wire cost paid for nothing. Counting continues past
// the cap (SearchResult.Matched), so a reader sees the total rather than 200.
const maxSearchHits = 200

// SegmentKind identifies which span of a message a hit landed in, so the
// client can pick the right rendered surface before applying the offset.
type SegmentKind string

// Segment kinds, in RENDERED order. A tool block exposes several SEPARATE
// segments sharing one block index, so the kind is what disambiguates their
// offsets — and declaring them in the order the card renders them is what makes
// a reader's walk through one card follow the card rather than an accident of
// declaration.
const (
	SegmentContent       SegmentKind = "content"
	SegmentReasoning     SegmentKind = "reasoning"
	SegmentToolTitle     SegmentKind = "tool_title"
	SegmentToolDisclosed SegmentKind = "tool_disclosed"
	SegmentToolDiff      SegmentKind = "tool_diff"
	SegmentToolDenial    SegmentKind = "tool_denial"
	SegmentToolInput     SegmentKind = "tool_input"
	SegmentToolOutput    SegmentKind = "tool_output"

	// The three MESSAGE-level kinds. They carry no block index — they are
	// properties of the message rather than of one of its blocks — and two of
	// them render OUTSIDE the message row entirely (the attachment pills sit in
	// the turn HEADER, the failure reason in the card-level notice), which is
	// why the client resolves those two from the turn rather than from the row.
	SegmentPlan        SegmentKind = "plan"
	SegmentAttachment  SegmentKind = "attachment"
	SegmentTurnFailure SegmentKind = "turn_failure"

	// SegmentMessage is the filter-only kind: a query with filters and no free
	// text yields one synthetic hit per matching message, locating the message
	// rather than a span inside it (offset 0, zero segment length, no block).
	SegmentMessage SegmentKind = "message"
)

// segmentKinds is every declared kind, in emission order. Declared HERE so the
// exhaustiveness test and the golden's kind loop read ONE list rather than each
// spelling its own literal — two literals for one vocabulary is how the two
// drift, and a kind declared with no producer then passes both quietly.
var segmentKinds = []SegmentKind{
	SegmentContent, SegmentReasoning, SegmentToolTitle, SegmentToolDisclosed,
	SegmentToolDiff, SegmentToolDenial, SegmentToolInput, SegmentToolOutput,
	SegmentPlan, SegmentAttachment, SegmentTurnFailure, SegmentMessage,
}

// Hit locates one match. The client fetches only the turns it needs to reveal
// and highlights locally, so this carries position rather than markup. Position
// is segment-relative: Offset indexes runes inside the one segment named by
// SegmentKind + BlockIndex, never a concatenation of the message.
type Hit struct {
	// BlockIndex is the matched segment's block position in the message's
	// chronological Blocks array. Nil for messages persisted before blocks
	// existed and for message-kind hits. First for govet fieldalignment: a
	// pointer after the strings would extend the GC scan past their len words.
	BlockIndex *int `json:"block_index,omitempty"`
	// MessageID is the matched message.
	MessageID string `json:"message_id"`
	// TurnMessageID is the matched turn's OPENING message id.
	//
	// Carried alongside MessageID because a hit can land on an assistant
	// message inside a turn while the fold state keys on the turn's opener.
	// The turn NUMBER cannot substitute — it is session-absolute here and
	// window-relative in the client's projection.
	TurnMessageID string `json:"turn_message_id"`
	Excerpt       string `json:"excerpt"`
	// Role of the matched message, so a result list can say where a hit came
	// from without a second lookup.
	Role vibekit.Role `json:"role"`
	// SegmentKind names the span the hit landed in: content | reasoning |
	// tool_title | tool_disclosed | tool_diff | tool_denial | tool_input |
	// tool_output | plan | attachment | turn_failure, or message for a
	// filter-only hit.
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
// Filters make "the turn where you edited the composer" expressible, which
// a bare substring cannot do.
type searchQuery struct {
	text string
	file string
	tool string
	role string
	// needle is the free text prepared for the scan. The reader's case choice
	// applies to it ALONE: the scoped filters stay case-insensitive whatever
	// was asked, because `role:` is an enum and a path is typed from memory.
	needle textsearch.Needle
	turn   int
	// needleRunes is the free text's rune length, which is how long a match is
	// in the segment it landed in.
	needleRunes int
}

// parseSearchQuery splits `file:` / `tool:` / `role:` / `turn:` prefixes out
// of the raw query. Unknown prefixes stay in the free text rather than
// being dropped: a reader typing `http://` means it literally.
func parseSearchQuery(raw string, caseSensitive bool) searchQuery {
	q := searchQuery{turn: -1}
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
	q.needle = textsearch.NewNeedle(q.text, caseSensitive)
	q.needleRunes = utf8.RuneCountInString(q.text)
	return q
}

// SearchResult is GET /api/chats/{id}/search's reply: the hits, cut at
// maxSearchHits, beside the tally that says how many there were.
type SearchResult struct {
	Matches []Hit `json:"matches"`
	textsearch.Tally
}

// hitScan accumulates one scan: the hits kept so far, every occurrence
// counted, including the ones past the cap that mint no Hit, and the bytes of
// every segment the walk read.
type hitScan struct {
	hits    []Hit
	matched int
	chars   int
}

// add counts one occurrence and keeps it while the list has room. The hit is
// built lazily because past the cap its excerpt would be discarded.
func (sc *hitScan) add(mk func() Hit) {
	sc.matched++
	if len(sc.hits) < maxSearchHits {
		sc.hits = append(sc.hits, mk())
	}
}

// Search scans a chat's messages for a query and reports the hits beside their
// tally: every message is read whatever the hit list holds, so Scanned is the
// message count, Matched the occurrence count, and Truncated false, since nothing
// here can fail to read. Turn numbers come from the same projection the timeline
// rail draws. caseSensitive governs the FREE TEXT only; both halves of the
// in-chat search have to agree on it, so the flag travels on the request rather
// than being a server default either side could get wrong.
func Search(msgs []vibekit.Message, raw string, caseSensitive bool) SearchResult {
	res, _ := searchChat(msgs, raw, caseSensitive)
	return res
}

// searchChat is Search beside the byte volume of the segments the scan read.
// Cross-chat ranking divides a chat's occurrence count by that volume, and
// taking both from one walk is what keeps the numerator and the denominator
// over the same spans: a message a filter excludes is in neither, a reasoning
// or tool span the scan searches is in both.
func searchChat(msgs []vibekit.Message, raw string, caseSensitive bool) (res SearchResult, chars int) {
	q := parseSearchQuery(raw, caseSensitive)
	if q.text == "" && q.file == "" && q.tool == "" && q.role == "" && q.turn < 0 {
		return SearchResult{Matches: []Hit{}}, 0
	}
	turnOf, openerOf := turnIndexByMessage(msgs)
	sc := hitScan{hits: make([]Hit, 0, 16)}
	for i := range msgs {
		m := &msgs[i]
		turn := turnOf[m.ID]
		if !messageMatchesFilters(m, &q, turn) {
			continue
		}
		appendMessageHits(&sc, m, &q, turn, openerOf[m.ID])
	}
	return SearchResult{
		Matches: sc.hits,
		Scanned: len(msgs),
		Matched: sc.matched,
	}, sc.chars
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

// messageTouchesFile matches a substring against changed-file paths AND
// tool locations — a turn that only READ a file never appears in
// changed_files.
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

// appendMessageHits adds every match within one message, matching each
// segment independently — a hit's Offset and SegmentLen are the segment's,
// so a match can never span two segments.
//
// A filter-only query still yields one hit per matching message, so a
// scoped search without free text lists turns rather than finding nothing.
func appendMessageHits(sc *hitScan, m *vibekit.Message, q *searchQuery, turn int, opener string) {
	if q.text == "" {
		sc.add(func() Hit {
			return Hit{
				MessageID:     m.ID,
				TurnMessageID: opener,
				Role:          m.Role,
				Turn:          turn,
				SegmentKind:   SegmentMessage,
				Excerpt:       excerptAround([]rune(searchableText(m)), 0, 0),
			}
		})
		return
	}
	for _, seg := range messageSegments(m) {
		sc.chars += len(seg.text)
		appendSegmentHits(sc, m, q, turn, opener, &seg)
	}
}

// appendSegmentHits counts every occurrence of the query text inside one segment.
func appendSegmentHits(sc *hitScan, m *vibekit.Message, q *searchQuery, turn int, opener string, seg *segment) {
	var runes []rune
	for hit := range q.needle.Occurrences(seg.text) {
		if runes == nil {
			runes = []rune(seg.text)
		}
		sc.add(func() Hit {
			return Hit{
				MessageID:      m.ID,
				TurnMessageID:  opener,
				Role:           m.Role,
				Turn:           turn,
				SegmentKind:    seg.kind,
				AgentSubtaskID: seg.subtaskID,
				BlockIndex:     seg.blockIndex,
				Offset:         hit.Rune,
				SegmentLen:     len(runes),
				Excerpt:        excerptAround(runes, hit.Rune, q.needleRunes),
			}
		})
	}
}

// segment is one searchable span of a message: the unit a hit's offset is
// relative to.
type segment struct {
	// blockIndex is the owning block's position in Message.Blocks, nil on the
	// legacy blockless fallback. Every segment of one tool block shares it; the
	// kind is what tells their offsets apart.
	blockIndex *int
	kind       SegmentKind
	text       string
	subtaskID  string
}

// messageSegments lists a message's searchable spans in order. Block-bearing
// messages segment per block — the legacy Content/Reasoning fields mirror the
// block texts, so reading both would double every hit. Messages persisted
// before blocks existed fall back to one content segment over the legacy
// concatenation plus each tool call's own spans (toolSegments). Both shapes end
// with the same message-level tail (messageTailSegments).
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
	return append(segs, messageTailSegments(m)...)
}

// legacySegments is the fallback for messages with no block array: the
// prose and thinking trace as ONE content segment over their concatenation,
// plus each tool call's own spans (toolSegments) and the shared message-level
// tail, none of it block-addressed.
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
	return append(segs, messageTailSegments(m)...)
}

// messageTailSegments is the MESSAGE-level span set both message shapes share:
// each attachment's name, each plan entry's content, then the turn failure
// reason. None carries a block index, because none belongs to a block.
//
// Appended AFTER the block (or legacy) segments, which is close enough to
// rendered order for both shapes to need no per-role branch: a user message
// renders its text and then its pills, an assistant message renders its blocks,
// then its plan card, then the turn notice.
//
// ATTACHMENT NAME, NOT PATH. attachment-pill.ts renders `att.name` as the pill's
// text and puts `att.path` in the `title` ATTRIBUTE, which the client's DOM
// walker cannot mark — so searching the path would mint hits that always land on
// "not in rendered text". Loss: the directory part is unfindable.
//
// Two conditional-surface losses, both stated rather than hidden. Only a turn's
// TRIGGER attachments are drawn (messages.ts feeds `t.trigger?.attachments` to
// the header), so a mid-turn STEER's own attachment name is searched with nothing
// rendering it. And turnFailureText renders no reason for a clean, running or
// CANCELLED outcome, and lets the last interrupted event message's content win
// over it — so of the 240 corpus messages carrying a reason, ~17 are silenced
// outright and ~27 more sit on turns where an event can win. Both land on the
// honest notice: the hit selects the TURN CARD and says "not in rendered text".
func messageTailSegments(m *vibekit.Message) []segment {
	segs := make([]segment, 0, len(m.Attachments)+len(m.Plan)+1)
	for i := range m.Attachments {
		segs = append(segs, segment{kind: SegmentAttachment, text: m.Attachments[i].Name})
	}
	for i := range m.Plan {
		segs = append(segs, segment{kind: SegmentPlan, text: m.Plan[i].Content})
	}
	if m.TurnFailureReason != "" {
		segs = append(segs, segment{kind: SegmentTurnFailure, text: m.TurnFailureReason})
	}
	return segs
}

// toolSegments is one tool call's searchable spans, all sharing its block index,
// in the order the CARD renders them: title, the disclosed claim that REPLACES
// it, the diff preview, the denial block, the input, then the output. An empty
// span contributes no segment, so a call carrying only a title yields only a
// title. Stepping a card therefore walks it the way a reader reads it, which is
// also what pins bestHit's list-position tie-break to a stated order rather than
// to declaration accident.
//
// The disclosed span is Disclosed.DisplayName and nothing else: URI is not
// rendered and Type is a class name. It closes a live half-defect — disclosedClaim
// REPLACES the card's title in the display, so a reader searching a skill name
// could see it on screen and the server could not find it.
//
// The denial span is Denial.Resource and nothing else: it is the one field
// carrying reader-facing text (a command or a path). Capability and the rule's
// Effect come from a closed vocabulary, and the rule's match patterns are policy
// text reachable through Settings -> Permissions.
//
// The diff span is Diffs[0].NewText and nothing else, on two measurements.
// NEW TEXT ONLY: 97.4% of old_text's lines are also in new_text, so searching
// both mints a second hit for one rendered line. The loss is stated rather than
// hidden — a line an edit REMOVED is not findable here, and a delete whose
// new_text is empty contributes nothing at all — and its recovery routes are the
// diff pane's own find and git. DIFFS[0] ONLY: nothing renders or fetches a
// second diff (the card renders opts.diffs[0], and the fetch fallback's handler
// renders bulk.diffs[0]), so a hit past the first would be a counted match with
// no destination; 5 of 7,285 diff-bearing calls fleet-wide carry one.
//
// Path is deliberately not searched: it is already reachable through the `file:`
// filter (messageTouchesFile) and through the title, and searching it would give
// every diff a hit for a query naming its directory.
func toolSegments(tc *vibekit.ToolCall, subtaskID string, blockIndex *int) []segment {
	segs := []segment{{kind: SegmentToolTitle, text: tc.Title, subtaskID: subtaskID, blockIndex: blockIndex}}
	if tc.Disclosed != nil && tc.Disclosed.DisplayName != "" {
		segs = append(segs, segment{kind: SegmentToolDisclosed, text: tc.Disclosed.DisplayName, subtaskID: subtaskID, blockIndex: blockIndex})
	}
	newText := ""
	if len(tc.Diffs) > 0 {
		newText = tc.Diffs[0].NewText
	}
	if newText != "" {
		segs = append(segs, segment{kind: SegmentToolDiff, text: newText, subtaskID: subtaskID, blockIndex: blockIndex})
	}
	// Above the input, because detailsBody builds the denial block BEFORE the
	// input <pre>.
	if tc.Denial != nil && tc.Denial.Resource != "" {
		segs = append(segs, segment{kind: SegmentToolDenial, text: tc.Denial.Resource, subtaskID: subtaskID, blockIndex: blockIndex})
	}
	if input := inputLeafText(tc.Input, newText); input != "" {
		segs = append(segs, segment{kind: SegmentToolInput, text: input, subtaskID: subtaskID, blockIndex: blockIndex})
	}
	if tc.Output != "" {
		segs = append(segs, segment{kind: SegmentToolOutput, text: tc.Output, subtaskID: subtaskID, blockIndex: blockIndex})
	}
	return segs
}

// inputLeafDedupeMin is the leaf length at which a leaf occurring verbatim in its
// own call's new_text stops being searched. Below it a leaf is a path, a pattern
// or a short command — the members the claim line is built from — and cannot be
// the write payload. At or above it, 7,281 of the 7,361 calls fleet-wide carrying
// both an input and diffs hold such a leaf verbatim in that new_text (9.66 MB of
// 12.2 MB of leaf bytes), so searching both would mint a second hit for one
// rendered write.
const inputLeafDedupeMin = 40

// inputLeafText is ToolCall.Input's string LEAF VALUES, one per line, in DOCUMENT
// order, minus the leaves diffText already carries (inputLeafInDiff). Each of
// those three words is load-bearing: a number or a bool is not text a reader
// searches for; every string at any depth is covered, object members and array
// elements alike; and KEYS are skipped, because a raw scan matches `command`,
// `path` and every escape sequence — measured on one real chat, `workflow` occurs
// 15 times in the raw JSON and 13 times in the leaf values.
//
// Document order is what the card prints (JSON.stringify(input, null, 2),
// tool-card.ts:509), which is why this walks Decoder.Token() rather than round
// tripping through map[string]json.RawMessage: Go map iteration is randomised, so
// hit order would change between two requests for one query, moving the client's
// cursor and making the golden unstable.
//
// Malformed bytes and the literal `null` boundInput writes for an input it could
// neither parse nor shorten (tool_bounds.go:265) both yield "", and therefore no
// segment, with no error to the caller: a search must not fail because one tool
// call's input is odd, and a log line per odd input per query is noise nobody can
// act on. No size or depth cap either — the store bounds the input to
// persistBudget.inputTotal (32 KiB) and inputMember (8 KiB), and a 32 KiB
// document bounds this walk's stack with it.
//
// COST, per chat per query: a second encoding/json pass over that chat's stored
// input, plus one strings.Contains per long leaf over its call's own new_text
// (<=64 KiB). SearchAll multiplies that by the fan-out rather than adding a new
// one — searchOneChat runs the scan under searchWorkers over every chat its
// filter admits, each file bounded by fileCap — so fleet-wide the increment is
// one more pass over roughly 42 MB of stored tool input, against the whole-file
// unmarshal readChatFile already pays for each of those chats.
//
// What a hit here can reach: the rendered <pre class="tool-input"> is built from
// the PREVIEW's input, which drops a member over previewBudget.inputMember (4
// KiB) AND drops whole members to fit its inputTotal (16 KiB), while
// fetchOutputBulk (tool-card.ts:532) replaces the OUTPUT only — so a hit in a
// member between 4 and 8 KiB, and one in a small member the preview dropped
// whole, land on the card with "not in rendered text", which is the truth.
func inputLeafText(raw json.RawMessage, diffText string) string {
	if len(raw) == 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var c leafCollector
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ""
		}
		c.take(tok, diffText)
	}
	if len(c.stack) > 0 {
		// The bytes ran out inside a container: Token reports that as a plain EOF,
		// so an open frame is the only evidence the document was truncated, and
		// half a document is malformed like any other non-JSON input.
		return ""
	}
	return c.b.String()
}

// leafCollector is inputLeafText's walk state: the open containers, and the leaf
// lines gathered so far. Never copied — Builder forbids it.
type leafCollector struct {
	stack []leafFrame
	b     strings.Builder
}

// take folds one token into the walk: a structural token moves the stack, a
// member KEY is skipped, and a string VALUE the call's own diff does not already
// carry becomes a line of its own.
func (c *leafCollector) take(tok json.Token, diffText string) {
	if d, ok := tok.(json.Delim); ok {
		if d == '}' || d == ']' {
			c.stack = c.stack[:len(c.stack)-1]
			return
		}
		// A container is its parent's value, so the parent's slot is spent before
		// this frame exists.
		takeLeafSlot(c.stack)
		c.stack = append(c.stack, leafFrame{object: d == '{', expectKey: d == '{'})
		return
	}
	if takeLeafSlot(c.stack) {
		return
	}
	leaf, ok := tok.(string)
	if !ok || inputLeafInDiff(leaf, diffText) {
		return
	}
	if c.b.Len() > 0 {
		c.b.WriteString("\n")
	}
	c.b.WriteString(leaf)
}

// leafFrame is one open container in inputLeafText's walk. expectKey is
// meaningful for an object only, and is what tells a member's key from its value:
// on the token stream both are plain strings.
type leafFrame struct {
	object    bool
	expectKey bool
}

// takeLeafSlot spends the innermost object's next slot, reporting whether it was
// a KEY. An array element and a top-level value fill no slot, so both answer
// false.
func takeLeafSlot(stack []leafFrame) bool {
	n := len(stack) - 1
	if n < 0 || !stack[n].object {
		return false
	}
	key := stack[n].expectKey
	stack[n].expectKey = !key
	return key
}

// inputLeafInDiff reports whether a leaf is the write payload its call's own diff
// segment already covers. Length-gated first, on inputLeafDedupeMin's
// measurement: a short leaf can sit inside a payload without being one.
func inputLeafInDiff(leaf, diffText string) bool {
	return len(leaf) >= inputLeafDedupeMin && strings.Contains(diffText, leaf)
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

// searchableText is a message's prose, thinking and tool title/output,
// concatenated. It is NOT the searched surface — matching runs per segment via
// messageSegments, which covers spans this omits — and survives only as the
// excerpt source for a filter-only hit, where the excerpt has to open with
// something a reader recognises rather than be exhaustive.
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
