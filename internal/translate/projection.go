package translate

// Wire projection: rebuilding a chat transcript from a `session/load` replay.
//
// KAS answers `session/load` by replaying the session's stored transcript as
// ordinary `session/update` notifications, each tagged
// `update._meta.kiro.replay: true`. This file turns that stream back into
// []api.Message.
//
// It is a PURE ACCUMULATOR: no chat store, no broadcaster, no clock beyond an
// injected id generator. That is deliberate — the replay must be staged and
// swapped into the chat record atomically, never merged into it frame by
// frame, because a compaction marker arrives only AFTER the turns it applies
// to and a half-ingested replay is a transcript with history that should have
// been summarised.
//
// The frame sequence is measured, not inferred (kiro-cli 2.16.0, 2026-08-02).
// A two-turn session that was then compacted replays as:
//
//	user_message_chunk                      "Reply with exactly: ONE"
//	session_info_update turn_start
//	agent_message_chunk                     "ONE"
//	session_info_update context_usage
//	session_info_update turn_completion
//	session_info_update turn_end
//	  … the same six for turn two …
//	session_info_update summarization_separator
//	session_info_update summary_message      {content: "## Goal…"}
//	available_commands_update      ← NOT replay-tagged
//	session_info_update context_usage   ← NOT replay-tagged
//	config_option_update           ← NOT replay-tagged
//
// Three properties of that shape drive the design:
//
//  1. **The user message precedes `turn_start`**, so it is not inside the
//     assistant turn's bracket. It is flushed when the bracket opens.
//  2. **Turns are fully bracketed** by `turn_start` / `turn_end`. Live code
//     opens a turn on the first chunk and ends it from the `session/prompt`
//     response; a replay has no such response, so without these brackets
//     every replayed turn merges into one message.
//  3. **Both compaction markers arrive at the TAIL**, after every original
//     turn, and the separator carries a bare `summarizationSeparator: true`
//     with no message id. So "the segment before the separator" is the only
//     thing the wire can mean, and appending the compaction event at that
//     point is equivalent to honouring an id.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
)

// replayInfoMeta decodes the `_meta.kiro` block of a replayed
// session_info_update. Only the sub-kinds the projection consumes are
// declared; everything else is identified by Kind and ignored.
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

// replayTS converts KAS's RFC3339-with-milliseconds timestamp to the epoch
// millis api.Message carries. A missing or unparseable value yields 0, which
// callers replace with their own fallback — never with time.Now() at the top
// level, because stamping replayed history with the load's clock is the bug
// this function exists to avoid.
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

// workflowProgressIDPrefix is the id KAS gives a workflow-progress row it
// writes onto the LAUNCHING chat's transcript.
//
// Read out of the 2.16.0 bundle's persistWorkflowEvent, which appends
//
//	id:      `wf-progress-${randomUUID()}`
//	payload: {type: "user", source: "steer",
//	          content: JSON.stringify({method, ...payload}),
//	          _meta: {kiro: {notification: {kind: "workflow-progress", …}}}}
//
// So it replays as a user_message_chunk whose content is a JSON blob, and
// rendering it as user prose would put raw JSON in the transcript claiming the
// user typed it.
const workflowProgressIDPrefix = "wf-progress-"

// workflowProgressKind is the semantic discriminator on the same row.
const workflowProgressKind = "workflow-progress"

// isWorkflowProgress reports whether a replayed user chunk is really a workflow
// progress row.
//
// BOTH discriminators are checked on purpose. The id prefix is the reliable one
// — `_meta.kiro.messageId` is measured to reach the wire on every content frame
// — whereas whether the nested `notification` block survives KAS's replay
// mapping is NOT verified here (no workflow was available to probe). Checking
// the semantic field too means this starts working the moment it does survive,
// without waiting for a second discovery.
func isWorkflowProgress(m *ACPKiroMeta) bool {
	return strings.HasPrefix(m.Kiro.MessageID, workflowProgressIDPrefix) ||
		m.Kiro.Notification.Kind == workflowProgressKind
}

// Projection accumulates a replayed transcript. Not safe for concurrent use;
// one Projection belongs to one in-flight `session/load`.
type Projection struct {
	// Field order is driven by govet fieldalignment: the pointer-bearing
	// fields first with a SLICE last among them, so its non-pointer len/cap
	// words end the GC scan region, then the scalars.
	newID func() string
	buf   *buffer.Buffer

	userText string
	// userID is the wire identity of the user message being accumulated,
	// taken from the FIRST chunk of it (its timestamp is userTs, below with
	// the other scalars — govet fieldalignment wants the pointer-bearing
	// fields unbroken).
	userID string
	// turnID is the open assistant turn's id, adopted from the first content
	// frame inside the turn. turn_start itself carries none (measured: it
	// carries only turnStart/kind/replay).
	turnID string
	// Watermark is the id of the compaction event this replay produced, for
	// the caller to stamp on Chat.CompactionWatermark. Empty when the
	// session was never compacted.
	Watermark string

	messages []api.Message

	userTs    int64
	turnStart int64
	// compactAt is the index in messages where a summarization_separator
	// landed, or -1. The summary_message that follows collapses onto it.
	compactAt int

	userPending bool
	turnOpen    bool
}

// NewProjection returns an empty Projection. newID must produce unique
// message ids; the caller supplies it so the projection stays deterministic
// under test.
func NewProjection(newID func() string) *Projection {
	return &Projection{newID: newID, compactAt: -1}
}

// Ingest folds one replayed session/update frame into the projection.
// Unknown kinds are ignored: a replay carries catalog and telemetry frames
// that contribute nothing to a transcript.
func (p *Projection) Ingest(kind api.ACPUpdateKind, raw json.RawMessage) {
	switch kind {
	case api.ACPUpdateSessionInfo:
		p.ingestInfo(raw)
	case api.ACPUpdateAgentChunk:
		p.ingestAgentText(raw, false)
	case api.ACPUpdateThoughtChunk:
		p.ingestAgentText(raw, true)
	case api.ACPUpdateToolCall:
		p.ingestToolCall(raw)
	case api.ACPUpdateToolUpdate:
		p.ingestToolUpdate(raw)
	default:
		// user_message_chunk is handled here rather than in the switch above
		// because its kind constant lives outside the ACPUpdate* set vibekit
		// declares (it has never had a live handler — see hub/translate.go).
		if kind == replayUserChunkKind {
			p.ingestUserText(raw)
		}
	}
}

// replayUserChunkKind is KAS's user-message replay frame. vibekit declares no
// api.ACPUpdate* constant for it because the LIVE path deliberately has no
// handler (vibekit echoes its own user bubbles), so a replay is the only
// context in which it means anything.
const replayUserChunkKind api.ACPUpdateKind = "user_message_chunk"

func (p *Projection) ingestUserText(raw json.RawMessage) {
	var c replayChunk
	if json.Unmarshal(raw, &c) != nil {
		return
	}
	// A workflow-progress row rides this same frame type. It is machine state
	// for the run card, not something the user said, so it must not become a
	// user bubble full of JSON.
	if isWorkflowProgress(&c.Meta) {
		return
	}
	if !p.userPending {
		// First chunk of this user message owns its identity. KAS echoes back
		// the messageId vibekit sent on session/prompt, so this is the id the
		// chat record already knows the message by.
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
		p.buf.Reasoning.WriteString(c.Content.Text)
		return
	}
	p.buf.AppendTextDelta(c.Content.Text, sub)
	p.buf.Content.WriteString(c.Content.Text)
}

func (p *Projection) ingestToolCall(raw json.RawMessage) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil || tc.ToolCallID == "" {
		return
	}
	p.ensureTurn()
	p.adoptTurnIdentity(tc.Meta.Kiro.MessageID, tc.Meta.Kiro.Timestamp)
	call := api.ToolCall{
		ID:             tc.ToolCallID,
		Title:          tc.Title,
		Kind:           tc.Kind,
		Status:         tc.Status,
		Input:          tc.RawInput,
		AgentSubtaskID: tc.Meta.Kiro.AgentSubtaskID,
		Locations:      tc.Locations,
		Ts:             p.frameTS(tc.Meta.Kiro.Timestamp),
	}
	p.buf.ToolCalls = append(p.buf.ToolCalls, call)
	if p.buf.ToolCallIndex == nil {
		p.buf.ToolCallIndex = make(map[string]int)
	}
	p.buf.ToolCallIndex[tc.ToolCallID] = len(p.buf.ToolCalls) - 1
	p.buf.AppendToolUseBlock(tc.ToolCallID, tc.Meta.Kiro.AgentSubtaskID)
}

// ingestToolUpdate folds a replayed tool_call_update into the call the
// preceding tool_call opened. A replay always sends both: the persisted
// status is `approved`, which KAS maps to `in_progress` on the way out, and
// the update carries the terminal status plus the output. So a projected tool
// card lands complete rather than stuck mid-flight.
func (p *Projection) ingestToolUpdate(raw json.RawMessage) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil || p.buf == nil {
		return
	}
	idx, ok := p.buf.ToolCallIndex[tu.ToolCallID]
	if !ok || idx >= len(p.buf.ToolCalls) {
		return
	}
	tc := &p.buf.ToolCalls[idx]
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
			tc.Output += api.SanitizeOutput(item.Content.Text) + "\n"
		}
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
}

func (p *Projection) ingestInfo(raw json.RawMessage) {
	var u replayInfoMeta
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	switch u.Meta.Kiro.Kind {
	case "turn_start":
		p.flushUser()
		p.openTurn()
	case "turn_end":
		p.closeTurn()
	case "summarization_separator":
		// Everything accumulated so far is the segment the summary replaces.
		// Close any open turn first so the boundary lands between messages.
		p.closeTurn()
		p.compactAt = len(p.messages)
	case "summary_message":
		p.applySummary(u.Meta.Kiro.SummaryMessage)
	}
}

// applySummary appends the compaction event for a replayed summary. It folds
// onto the SAME domain shape the live path produces (an api.RoleEvent message
// with EventKind compacted, plus a watermark) rather than introducing a second
// representation of a compacted transcript.
//
// The originals are KEPT, matching live behaviour: vibekit's model is a
// watermark, not a deletion. The separator's position is what the watermark
// means, which is why compactAt is recorded rather than the messages dropped.
//
// KNOWN DISAGREEMENT with the design doc (§16.3), recorded rather than
// silently resolved. §16.3 says to "collapse that segment to the summary" and
// its risk table wants a fixture asserting pre-summary turns are ABSENT. This
// keeps them, for three reasons measured in this codebase:
//
//  1. The LIVE path keeps them. translate/compact.go's
//     handleCompactionCompleted appends the summary as an EventCompacted
//     message and sets CompactionWatermark; it deletes nothing. Collapsing on
//     replay would make a chat's transcript change shape across a container
//     restart — and §16.3 itself asks for all three compaction shapes to
//     "fold onto one domain event", which collapse-on-replay-only is not.
//  2. It would break the context bar. static-src/context-ui.ts derives
//     summarizedCount by counting messages up to the watermark; with the
//     pre-summary messages gone that count is the constant 1.
//  3. Collapse remains available downstream at zero cost. The watermark marks
//     the boundary, so hiding the segment is a render decision the client can
//     take later — whereas dropping it here throws away data no consumer can
//     recover.
func (p *Projection) applySummary(sum *struct {
	Content string `json:"content"`
},
) {
	if sum == nil || p.compactAt < 0 {
		return
	}
	// Insert at the separator's position so a later turn replayed after a
	// compaction still sorts after the boundary.
	at := min(p.compactAt, len(p.messages))
	// The summary_message frame carries no timestamp of its own (measured),
	// so it inherits the last message of the segment it summarises. That keeps
	// the event ordered where it belongs instead of at the load's wall clock,
	// which would sort every replayed compaction to the end of the transcript.
	ts := int64(0)
	if at > 0 {
		ts = p.messages[at-1].Ts
	}
	evt := api.Message{
		ID:        p.newID(),
		Role:      api.RoleEvent,
		EventKind: api.EventCompacted,
		Content:   sum.Content,
		Ts:        ts,
	}
	p.messages = append(p.messages[:at], append([]api.Message{evt}, p.messages[at:]...)...)
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
}

// adoptTurnIdentity gives the open turn the id and timestamp of the first
// content frame inside it. `turn_start` carries neither (measured: only
// turnStart/kind/replay), so the bracket cannot supply them.
//
// KAS records one message per say/tool rather than one per turn, so a turn
// with several say blocks offers several ids; taking the first makes the
// projected id a deterministic function of the replay, which is what
// idempotence across repeated loads requires.
func (p *Projection) adoptTurnIdentity(messageID, timestamp string) {
	if p.turnID == "" {
		p.turnID = messageID
	}
	if p.turnStart == 0 {
		p.turnStart = replayTS(timestamp)
	}
}

// closeTurn assembles the open turn's buffer into one assistant message.
// A turn that produced nothing at all is dropped rather than persisted as an
// empty bubble.
func (p *Projection) closeTurn() {
	if !p.turnOpen {
		return
	}
	p.turnOpen = false
	b := p.buf
	p.buf = nil
	if b == nil {
		return
	}
	if b.Content.Len() == 0 && b.Reasoning.Len() == 0 && len(b.ToolCalls) == 0 {
		return
	}
	p.messages = append(p.messages, api.Message{
		ID:        p.idOr(p.turnID),
		Role:      api.RoleAssistant,
		Content:   b.Content.String(),
		Reasoning: b.Reasoning.String(),
		Blocks:    b.Blocks,
		ToolCalls: b.ToolCalls,
		Ts:        p.turnStart,
	})
}

// idOr prefers the wire's own message id and falls back to a generated one.
// The fallback exists for frames that carry no messageId at all rather than as
// the normal path: a generated id makes the projection non-deterministic
// across loads, so it is the degraded case, not the default.
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
	p.messages = append(p.messages, api.Message{
		ID:      p.idOr(id),
		Role:    api.RoleUser,
		Content: text,
		Ts:      ts,
	})
}

// Messages closes any still-open turn and returns the projected transcript.
// Safe to call once; the projection is spent afterwards.
func (p *Projection) Messages() []api.Message {
	p.closeTurn()
	p.flushUser()
	return p.messages
}
