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

// replayChunk decodes a replayed user/agent/thought message chunk.
type replayChunk struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
	Meta ACPKiroMeta `json:"_meta"`
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
	// Watermark is the id of the compaction event this replay produced, for
	// the caller to stamp on Chat.CompactionWatermark. Empty when the
	// session was never compacted.
	Watermark string

	messages []api.Message

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
	p.userText += c.Content.Text
	p.userPending = true
}

func (p *Projection) ingestAgentText(raw json.RawMessage, thinking bool) {
	var c replayChunk
	if json.Unmarshal(raw, &c) != nil || c.Content.Text == "" {
		return
	}
	p.ensureTurn()
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
	call := api.ToolCall{
		ID:             tc.ToolCallID,
		Title:          tc.Title,
		Kind:           tc.Kind,
		Status:         tc.Status,
		Input:          tc.RawInput,
		AgentSubtaskID: tc.Meta.Kiro.AgentSubtaskID,
		Locations:      tc.Locations,
		Ts:             p.turnStart,
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
func (p *Projection) applySummary(sum *struct {
	Content string `json:"content"`
},
) {
	if sum == nil || p.compactAt < 0 {
		return
	}
	evt := api.Message{
		ID:        p.newID(),
		Role:      api.RoleEvent,
		EventKind: api.EventCompacted,
		Content:   sum.Content,
		Ts:        time.Now().UnixMilli(),
	}
	// Insert at the separator's position so a later turn replayed after a
	// compaction still sorts after the boundary.
	at := min(p.compactAt, len(p.messages))
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
	p.turnStart = time.Now().UnixMilli()
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
		ID:        p.newID(),
		Role:      api.RoleAssistant,
		Content:   b.Content.String(),
		Reasoning: b.Reasoning.String(),
		Blocks:    b.Blocks,
		ToolCalls: b.ToolCalls,
		Ts:        p.turnStart,
	})
}

// flushUser emits the accumulated user-message text as one user message.
func (p *Projection) flushUser() {
	if !p.userPending {
		return
	}
	text := p.userText
	p.userText = ""
	p.userPending = false
	if text == "" {
		return
	}
	p.messages = append(p.messages, api.Message{
		ID:      p.newID(),
		Role:    api.RoleUser,
		Content: text,
		Ts:      time.Now().UnixMilli(),
	})
}

// Messages closes any still-open turn and returns the projected transcript.
// Safe to call once; the projection is spent afterwards.
func (p *Projection) Messages() []api.Message {
	p.closeTurn()
	p.flushUser()
	return p.messages
}
