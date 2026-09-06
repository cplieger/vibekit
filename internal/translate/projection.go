package translate

// Wire projection: rebuilding a chat transcript from a `session/load` replay, which
// KAS answers by replaying the stored transcript as ordinary `session/update`
// notifications tagged `_meta.kiro.replay`. A pure accumulator: no chat store, no
// broadcaster, no clock beyond the injected id generator.
//
// The result is staged and swapped atomically, never merged frame by frame, because
// a compaction marker arrives only AFTER the turns it applies to; and a replay has
// no `session/prompt` response, so the turn brackets are what keep turns apart.

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayInfoMeta decodes the `_meta.kiro` block of a replayed session_info_update.
// Only the sub-kinds the projection consumes are declared.
type replayInfoMeta struct {
	Meta struct {
		Kiro struct {
			SummaryMessage *struct {
				Content string `json:"content"`
			} `json:"summaryMessage"`
			Kind string `json:"kind"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// replayTS converts KAS's RFC3339 timestamp to the epoch millis vibekit.Message
// carries. A missing or unparseable value yields 0 for the caller's own fallback —
// never time.Now(), which would stamp replayed history with the load's clock.
func replayTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// replayChunk decodes a replayed user/agent/thought message chunk.
type replayChunk struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
	Meta ACPKiroMeta `json:"_meta"`
}

// workflowProgressIDPrefix is the id KAS gives a workflow-progress row it writes onto
// the LAUNCHING chat's transcript. It replays as a user_message_chunk whose content is
// a JSON blob, so rendering it as prose would claim the user typed JSON.
const workflowProgressIDPrefix = "wf-progress-"

// workflowProgressKind is the semantic discriminator on the same row.
const workflowProgressKind = "workflow-progress"

// The two discriminators on a `send_message` note's durable copy. It replays on the
// SAME frame type a real prompt does and neither discriminator above matches it,
// which is why a question a workflow step asked used to come back as a user bubble
// on the launching chat, attributed to the reader.
const (
	stepNoticeIDPrefix = "notify-"
	stepNoticeKind     = "system-notification"
)

// isStepNotice reports whether a replayed user chunk is really a step's note. BOTH
// discriminators, so this keeps working if the id format moves.
func isStepNotice(m *ACPKiroMeta) bool {
	return strings.HasPrefix(m.Kiro.MessageID, stepNoticeIDPrefix) ||
		m.Kiro.Notification.Kind == stepNoticeKind
}

// isWorkflowProgress reports whether a replayed user chunk is really a workflow
// progress row. BOTH discriminators: the id prefix is the one measured to reach the
// wire, and the semantic field starts working if the nested block survives too.
func isWorkflowProgress(m *ACPKiroMeta) bool {
	return strings.HasPrefix(m.Kiro.MessageID, workflowProgressIDPrefix) ||
		m.Kiro.Notification.Kind == workflowProgressKind
}

// Projection accumulates a replayed transcript. Not safe for concurrent use;
// one Projection belongs to one in-flight `session/load`.
type Projection struct {
	// Field order is govet fieldalignment's: pointer-bearing fields first with the
	// SLICE last among them, so its len/cap words end the GC scan region.
	newID func() string
	buf   *buffer.Buffer

	userText string
	// userID is the wire identity of the user message being accumulated, taken from
	// the FIRST chunk of it (its timestamp is userTs, below with the other scalars).
	userID string
	// turnID is the open assistant turn's id, adopted from the first content frame
	// inside the turn. turn_start itself carries none (measured).
	turnID string
	// Watermark is the id of the compaction event this replay produced, for the caller
	// to stamp on Chat.CompactionWatermark. Empty when the session was never compacted.
	Watermark string

	messages []vibekit.Message

	userTs    int64
	turnStart int64
	// compactAt is the index in messages where a summarization_separator landed, or -1.
	// The summary_message that follows collapses onto it.
	compactAt int

	userPending bool
	turnOpen    bool
	// dropNextTurn is set when flushUser drops a prime preamble: the bracket that opens
	// next is the prime's own turn, and its reply is as invisible as its prompt.
	dropNextTurn bool
	// turnPrimed is whether the OPEN turn is a prime's, so closeTurn discards it.
	turnPrimed bool
}

// NewProjection returns an empty Projection. newID must produce unique message ids;
// the caller supplies it so the projection stays deterministic under test.
func NewProjection(newID func() string) *Projection {
	return &Projection{newID: newID, compactAt: -1}
}

// Ingest folds one replayed session/update frame into the projection. Unknown kinds
// are ignored: a replay carries catalog and telemetry frames a transcript cannot use.
func (p *Projection) Ingest(kind vibekit.ACPUpdateKind, raw json.RawMessage) {
	switch kind {
	case vibekit.ACPUpdateSessionInfo:
		p.ingestInfo(raw)
	case vibekit.ACPUpdateAgentChunk:
		p.ingestAgentText(raw, false)
	case vibekit.ACPUpdateThoughtChunk:
		p.ingestAgentText(raw, true)
	case vibekit.ACPUpdateToolCall:
		p.ingestToolCall(raw)
	case vibekit.ACPUpdateToolUpdate:
		p.ingestToolUpdate(raw)
	default:
		// user_message_chunk is handled here because its kind constant lives outside
		// the ACPUpdate* set vibekit declares (it has never had a live handler).
		if kind == replayUserChunkKind {
			p.ingestUserText(raw)
		}
	}
}

// replayUserChunkKind is KAS's user-message replay frame. vibekit declares no
// ACPUpdate* constant for it because the LIVE path deliberately has no handler
// (vibekit echoes its own user bubbles).
const replayUserChunkKind vibekit.ACPUpdateKind = "user_message_chunk"

func (p *Projection) ingestUserText(raw json.RawMessage) {
	var c replayChunk
	if json.Unmarshal(raw, &c) != nil {
		return
	}
	// A workflow-progress row rides this same frame type. It is machine state for the
	// run card, not something the user said.
	if isWorkflowProgress(&c.Meta) {
		return
	}
	// A step's own note rides the same frame type. KEPT rather than dropped — it is the
	// only durable copy of a question the ask registry holds in memory — but as an
	// EVENT, because attributing it to the user made the transcript claim the reader
	// typed the step's question. Emitted whole: KAS writes the note as one row.
	if isStepNotice(&c.Meta) {
		p.appendStepNotice(&c)
		return
	}
	if !p.userPending {
		// First chunk owns the identity: KAS echoes back the messageId vibekit sent on
		// session/prompt, so this is the id the chat record already knows.
		p.userID = c.Meta.Kiro.MessageID
		p.userTs = replayTS(c.Meta.Kiro.Timestamp)
	}
	p.userText += c.Content.Text
	p.userPending = true
}

func (p *Projection) ingestAgentText(raw json.RawMessage, thinking bool) {
	var c replayChunk
	if json.Unmarshal(raw, &c) != nil || c.Content.Text == "" {
		return
	}
	p.ensureTurn()
	p.adoptTurnIdentity(c.Meta.Kiro.MessageID, c.Meta.Kiro.Timestamp)
	sub := c.Meta.Kiro.AgentSubtaskID
	if thinking {
		p.buf.AppendThinkingDelta(c.Content.Text, sub)
		return
	}
	// The same marker filter as the live path, and not optional here: KAS replays its
	// own log on every resume and the marker it never scrubbed is stored in that log.
	// The acknowledgements it lifts out are DISCARDED — a Projection has no Broadcast,
	// and KAS clears its steering buffer at every turn boundary, so no chip is left.
	prev, _ := p.buf.SteerCarry()
	text, carry, _ := stripSteerAcks(prev, c.Content.Text)
	p.buf.SetSteerCarry(carry, sub)
	if text == "" {
		return
	}
	p.buf.AppendTextDelta(text, sub)
}

func (p *Projection) ingestToolCall(raw json.RawMessage) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil || tc.ToolCallID == "" {
		return
	}
	// The live path's internal-tool suppression, applied to the replay: KAS's log stores
	// the cloud-config fetch it announced during session creation, so without this a
	// resumed chat regains the card the live stream dropped.
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		return
	}
	p.ensureTurn()
	p.adoptTurnIdentity(tc.Meta.Kiro.MessageID, tc.Meta.Kiro.Timestamp)
	call := vibekit.ToolCall{
		ID:             tc.ToolCallID,
		Title:          tc.Title,
		Kind:           tc.Kind,
		Status:         tc.Status,
		Input:          tc.RawInput,
		AgentSubtaskID: tc.Meta.Kiro.AgentSubtaskID,
		Locations:      tc.Locations,
		Ts:             p.frameTS(tc.Meta.Kiro.Timestamp),
	}
	p.buf.AppendToolCall(&call)
	p.buf.AppendToolUseBlock(tc.ToolCallID, tc.Meta.Kiro.AgentSubtaskID)
}

// ingestToolUpdate folds a replayed tool_call_update into the call the preceding
// tool_call opened. A replay always sends both — the persisted status is `approved`
// and the update carries the terminal one — so a projected card lands complete.
func (p *Projection) ingestToolUpdate(raw json.RawMessage) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil || p.buf == nil {
		return
	}
	tc, idx, ok := p.buf.ToolCall(tu.ToolCallID)
	if !ok {
		return
	}
	if tu.Title != "" {
		tc.Title = tu.Title
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	if tu.Status != "" {
		tc.Status = tu.Status
	}
	if len(tu.Locations) > 0 {
		tc.Locations = tu.Locations
	}
	for _, item := range tu.Content {
		if item.Type == ContentTypeContent && item.Content.Text != "" {
			tc.Output += sanitize.Output(item.Content.Text) + "\n"
		}
	}
	mergeCheckpoint(&tc, tu.Meta.Kiro.Checkpoint)
	p.buf.SetToolCall(idx, &tc)
}

func (p *Projection) ingestInfo(raw json.RawMessage) {
	var u replayInfoMeta
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	switch u.Meta.Kiro.Kind {
	case "turn_start":
		// Close, THEN flush, THEN open. A start with a turn already open means the end
		// never arrived: opening without closing discards that turn's reply, and flushing
		// first attributes the orphan to the NEXT prompt. Messages() repeats the order.
		p.closeTurn()
		p.flushUser()
		p.openTurn()
	case "turn_end":
		p.closeTurn()
	case "summarization_separator":
		// Everything so far is the segment the summary replaces. Close any open turn
		// first so the boundary lands between messages.
		p.closeTurn()
		p.compactAt = len(p.messages)
	case "summary_message":
		p.applySummary(u.Meta.Kiro.SummaryMessage)
	}
}

// applySummary appends the compaction event for a replayed summary, folding onto the
// same shape the live path produces. The originals are KEPT: vibekit's model is a
// watermark, not a deletion, the context bar counts up to it, and collapsing is a
// render decision available downstream at no cost.
func (p *Projection) applySummary(sum *struct {
	Content string `json:"content"`
},
) {
	if sum == nil || p.compactAt < 0 {
		return
	}
	// Insert at the separator's position so a later turn still sorts after the boundary.
	at := min(p.compactAt, len(p.messages))
	// The summary_message frame carries no timestamp (measured), so it inherits the last
	// message of the segment; the load's wall clock would sort it to the end.
	ts := int64(0)
	if at > 0 {
		ts = p.messages[at-1].Ts
	}
	evt := vibekit.Message{
		ID:        p.newID(),
		Role:      vibekit.RoleEvent,
		EventKind: vibekit.EventCompacted,
		Content:   sum.Content,
		Ts:        ts,
	}
	p.messages = append(p.messages[:at], append([]vibekit.Message{evt}, p.messages[at:]...)...)
	p.Watermark = evt.ID
	p.compactAt = -1
}

func (p *Projection) ensureTurn() {
	if !p.turnOpen {
		p.flushUser()
		p.openTurn()
	}
}

func (p *Projection) openTurn() {
	p.buf = &buffer.Buffer{}
	p.turnOpen = true
	p.turnID = ""
	p.turnStart = 0
	// A prime's user message was just dropped, so this bracket is the prime's own turn.
	p.turnPrimed = p.dropNextTurn
	p.dropNextTurn = false
}

// adoptTurnIdentity gives the open turn the id and timestamp of the first content
// frame inside it; `turn_start` carries neither (measured). KAS records one message per
// say/tool, so taking the FIRST is what makes the projection idempotent across loads.
func (p *Projection) adoptTurnIdentity(messageID, timestamp string) {
	if p.turnID == "" {
		p.turnID = messageID
	}
	if p.turnStart == 0 {
		p.turnStart = replayTS(timestamp)
	}
}

// closeTurn assembles the open turn's buffer into one assistant message. A turn that
// produced nothing is dropped rather than persisted as an empty bubble.
func (p *Projection) closeTurn() {
	if !p.turnOpen {
		return
	}
	p.turnOpen = false
	b := p.buf
	p.buf = nil
	primed := p.turnPrimed
	p.turnPrimed = false
	if b == nil || primed {
		// A prime's ANSWER goes with its preamble. The prime asks the model for one line
		// confirming it is caught up, and that reply replays as an ordinary bracketed
		// turn — so filtering only the user half left it in the transcript.
		return
	}
	// Settle anything the marker filter withheld before the emptiness check reads
	// Content — a turn whose only text was a held candidate would be judged empty.
	FlushSteerCarry(b)
	if b.Content.Len() == 0 && b.Reasoning.Len() == 0 && len(b.ToolCalls) == 0 {
		return
	}
	p.messages = append(p.messages, vibekit.Message{
		ID:        p.idOr(p.turnID),
		Role:      vibekit.RoleAssistant,
		Content:   b.Content.String(),
		Reasoning: b.Reasoning.String(),
		Blocks:    b.Blocks,
		ToolCalls: b.ToolCalls,
		Ts:        p.turnStart,
	})
}

// idOr prefers the wire's own message id and falls back to a generated one. The
// fallback is the degraded case: a generated id makes the projection non-deterministic.
func (p *Projection) idOr(wireID string) string {
	if wireID != "" {
		return wireID
	}
	return p.newID()
}

// frameTS is a frame's own timestamp, falling back to the turn's start when
// the frame carries none.
func (p *Projection) frameTS(timestamp string) int64 {
	if ts := replayTS(timestamp); ts != 0 {
		return ts
	}
	return p.turnStart
}

// appendStepNotice emits a step's note as an inline event message. Its id and timestamp
// come off the wire, so two loads of one session produce the same transcript.
func (p *Projection) appendStepNotice(c *replayChunk) {
	if c.Content.Text == "" {
		return
	}
	p.messages = append(p.messages, vibekit.Message{
		ID:        p.idOr(c.Meta.Kiro.MessageID),
		Role:      vibekit.RoleEvent,
		EventKind: vibekit.EventStepNotice,
		Content:   c.Content.Text,
		Ts:        replayTS(c.Meta.Kiro.Timestamp),
	})
}

// flushUser emits the accumulated user-message text as one user message.
func (p *Projection) flushUser() {
	if !p.userPending {
		return
	}
	text := p.userText
	id, ts := p.userID, p.userTs
	p.userText, p.userID, p.userTs = "", "", 0
	p.userPending = false
	if text == "" {
		return
	}
	// A PRIME is vibekit's own transcript replay, sent as a real session/prompt, so KAS
	// persists and replays it here. Dropped: the live path publishes none of a prime's
	// frames, so keeping it would make a resume the one place the preamble shows up.
	if IsPrimePreamble(text) {
		// The prime's own TURN goes too (see closeTurn): the next bracket is the prime's.
		p.dropNextTurn = true
		return
	}
	p.messages = append(p.messages, vibekit.Message{
		ID:      p.idOr(id),
		Role:    vibekit.RoleUser,
		Content: text,
		Ts:      ts,
	})
}

// Messages closes any still-open turn and returns the projected transcript as a fresh
// slice, so a caller appending cannot write into the projection's backing array.
// Idempotent: closeTurn and flushUser are both no-ops once they have run.
func (p *Projection) Messages() []vibekit.Message {
	p.closeTurn()
	p.flushUser()
	return slices.Clone(p.messages)
}
