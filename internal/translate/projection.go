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
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayInfoMeta decodes the `_meta.kiro` block of a replayed session_info_update. Only
// the sub-kinds the projection consumes are declared, and the three turn fields reuse the
// LIVE path's types rather than twins — the replay carries the same payloads, so a second
// declaration would be a second place a field can be forgotten.
type replayInfoMeta struct {
	Meta struct {
		Kiro struct {
			SummaryMessage *struct {
				Content string `json:"content"`
			} `json:"summaryMessage"`
			TurnEnd             *turnEndBlock       `json:"turnEnd"`
			Kind                string              `json:"kind"`
			PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
			ElapsedTime         float64             `json:"elapsedTime"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// pendingTurn holds the facts the wire's own turn frames carry until the turn closes.
// Held rather than stamped on arrival because neither frame is the close: measured on
// kiro-cli 2.21.4 a turn replays as `turn_completion` then `turn_end`, and the close keys
// on the second, so the metering must survive one frame.
type pendingTurn struct {
	// conclusion is the turn_end payload, read. nil separates a bracket carrying no
	// payload — which still closes the turn — from one reporting a stop reason.
	conclusion *vibekit.TurnConclusion
	credits    float64
	elapsedMs  float64
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
	// pending is the open turn's facts, filled by the two turn frames and stamped by
	// closeTurn. Leads the scalars because it holds a pointer.
	pending pendingTurn

	// workDir is the workspace root a projected diff path is made relative to. A plain
	// string keeps the projection dependency-free — no store, no broadcaster, no clock
	// beyond the injected id generator.
	workDir  string
	userText string
	// userID is the wire identity of the user message being accumulated, taken from
	// the FIRST chunk of it (its timestamp is userTs, below with the other scalars).
	userID string
	// userKASID is the same id recorded as the KAS-side one, so a replayed row is
	// addressable by rewind through the one field a live row is addressable by.
	userKASID string
	// userKind is that message's kind, also taken from its FIRST chunk.
	userKind vibekit.UserKind
	// userTag is _meta.kiro.userMessageTag off that same first chunk: presence marks a row
	// KAS filed as a prompt rather than as steering.
	userTag string
	// steerCandidates are the steer rows emitted since the last turn OPENED, the window a
	// resend can be matched against. ackedSteers are the steer ids an assistant chunk has
	// acknowledged so far, read SYNCHRONOUSLY at flush time: on the measured specimen the
	// ack for a genuinely resent steer arrives 43 s LATER, inside the turn the resend
	// opened, so a whole-replay set would veto a real resend.
	steerCandidates []steerCandidate
	ackedSteers     map[string]struct{}
	// turnID is the open assistant turn's id, adopted from the first content frame
	// inside the turn. turn_start itself carries none (measured).
	turnID string
	// Watermark is the id of the compaction event this replay produced, for the caller
	// to stamp on Chat.CompactionWatermark. Empty when the session was never compacted.
	Watermark string
	// toolStarts holds the create-frame timestamp of each tool call in the open turn, and
	// ONLY where that frame carried one. deriveDuration cannot read the call's own Ts for
	// this: frameTS falls back to turnStart, and turnStart is itself adopted from the first
	// in-turn frame, so a timestamp-bearing first create and a timestamp-less later one are
	// indistinguishable there.
	toolStarts map[string]int64

	messages []vibekit.Message

	userTs    int64
	turnStart int64
	// compactAt is the index in messages where a summarization_separator landed, or -1.
	// The summary_message that follows collapses onto it.
	compactAt int
	// compactSeq numbers a compaction event that lands at position 0, which has no
	// predecessor message to derive an id from. Per projection, so two loads agree.
	compactSeq int

	userPending bool
	turnOpen    bool
}

// NewProjection returns an empty Projection. newID must produce unique message ids;
// the caller supplies it so the projection stays deterministic under test. workDir is
// the workspace root a diff path is made relative to; empty leaves paths as sent.
//
// The two parameters have different types, so a caller cannot transpose them.
func NewProjection(newID func() string, workDir string) *Projection {
	return &Projection{newID: newID, workDir: workDir, compactAt: -1}
}

// relPath normalizes a wire path reference to workspace-relative form, the same rule
// the live path applies (translate.relPathIn).
func (p *Projection) relPath(ref string) string {
	return relPathIn(p.workDir, ref)
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
	// Two user rows with no assistant frame between them merge into one message
	// carrying the FIRST row's identity, so an empty steering-boundary row hijacks
	// the prompt that follows it and stamps `steer` onto the reader's own words.
	if id := c.Meta.Kiro.MessageID; p.userPending && id != "" && id != p.userID {
		p.flushUser()
	}
	if !p.userPending {
		// First chunk owns the identity. `_meta.kiro.messageId` on a REPLAYED user chunk
		// is the agent's own RECORD id (the frame is built FROM the record, not from
		// anything the client sent), so it serves as both ID and KASMessageID.
		p.userID = c.Meta.Kiro.MessageID
		p.userKASID = c.Meta.Kiro.MessageID
		p.userTs = replayTS(c.Meta.Kiro.Timestamp)
		// KAS stamps source="steer" on ALL FOUR steering shapes, so the two returns
		// above must stay above this, and a boundary row must flush EMPTY so
		// flushUser's `text == ""` check drops it rather than opening a steer row.
		p.userKind = ""
		if c.Meta.Kiro.Source == "steer" {
			p.userKind = vibekit.UserKindSteer
		}
		p.userTag = c.Meta.Kiro.UserMessageTag
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
	text, carry, acks := stripSteerAcks(prev, c.Content.Text)
	p.buf.SetSteerCarry(carry, sub)
	// ABOVE the empty-text return: a chunk whose whole content was the marker emits
	// nothing, and that is exactly the chunk an acknowledgement arrives on.
	for _, a := range acks {
		if p.ackedSteers == nil {
			p.ackedSteers = make(map[string]struct{})
		}
		p.ackedSteers[a.SteerID] = struct{}{}
	}
	if text == "" {
		return
	}
	p.buf.AppendTextDelta(text, sub)
	// A refusal's explanation IS this chunk's text and _meta.kiro.refusal classifies
	// it, so the block was decoded and thrown away. Same gate and same position as the
	// live path's: text chunks only, so a stray tagged thought cannot mark the turn.
	if r := refusalFrom(c.Meta.Kiro.Refusal); r != nil {
		p.buf.SetRefusal(r)
	}
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
	// The live path's own builder, so one frame decodes one way: the hand-written literal
	// it replaces is how the projection came to drop the terminal link, the diffs, the
	// disclosure and the denial. Frame timestamp, never a clock.
	if start := replayTS(tc.Meta.Kiro.Timestamp); start != 0 {
		if p.toolStarts == nil {
			p.toolStarts = map[string]int64{}
		}
		p.toolStarts[tc.ToolCallID] = start
	}
	call := toolCallFromWire(&tc, tc.Meta.Kiro.AgentSubtaskID, "",
		parseToolContent(p.relPath, tc.ToolCallID, tc.Content),
		p.frameTS(tc.Meta.Kiro.Timestamp))
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
		tc.Title = displayText(tu.Title)
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
	// The live path's own parser rather than a second switch over the same content union.
	// Output and diffs accumulate — KAS repeats a write's block on every streaming frame
	// and the card keeps them all — and the terminal link is adopted once.
	content := parseToolContent(p.relPath, tu.ToolCallID, tu.Content)
	tc.Output += content.output
	tc.Diffs = append(tc.Diffs, content.diffs...)
	if tc.TerminalID == "" {
		tc.TerminalID = content.terminalID
	}
	p.trackChangedFiles(&tc, content.diffs)
	p.deriveDuration(&tc, tu.Meta.Kiro.Timestamp)
	// What makes the card the RUN's card, so a resumed transcript regains its run cards.
	// The meta read leads because it is what carries the id on a replay; the rawOutput
	// read is the live path's own spelling of the same fact.
	if tc.WorkflowID == "" {
		tc.WorkflowID = cmp.Or(tu.Meta.Kiro.WorkflowID, rawOutputWorkflowID(tu.RawOutput))
	}
	mergeCheckpoint(&tc, tu.Meta.Kiro.Checkpoint)
	// A denial is decided when the call is ATTEMPTED, so it can arrive on the update
	// rather than the create; the disclosure can too.
	mergeToolMeta(&tc, &tu)
	p.buf.SetToolCall(idx, &tc)
}

// deriveDuration stamps how long a replayed tool call took, from the two frames' OWN
// timestamps — toolStarts rather than the call's Ts, which can be a turnStart fallback and
// would make the difference time-since-turn-start under a duration's name. The buffer's
// duration helpers read a wall clock, so they would time the load. A fast tool's two records
// share the millisecond (measured), so 0 is the wire's own resolution rather than a miss.
func (p *Projection) deriveDuration(tc *vibekit.ToolCall, timestamp string) {
	start, ok := p.toolStarts[tc.ID]
	if tc.DurationMs != 0 || !ok {
		return
	}
	if end := replayTS(timestamp); end > start {
		tc.DurationMs = int(end - start)
	}
}

// trackChangedFiles feeds a completed write's diffs into the turn's file ledger, the
// route ChangedFiles reaches the closing message by.
//
// Gated on `completed` because KAS repeats a write's diff block on every streaming frame,
// so counting each arrival would claim a file changed when the write failed. isNewFile is
// always false: a replayed create already carries `completed`, so the live path's
// pending-edit discriminator has no state to read here.
func (p *Projection) trackChangedFiles(tc *vibekit.ToolCall, diffs []vibekit.ToolDiff) {
	if len(diffs) == 0 || tc.Status != vibekit.ToolCompleted {
		return
	}
	p.buf.TrackFileChanges(diffs, false)
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
	case "turn_completion":
		p.noteTurnMetering(u.Meta.Kiro.PromptTurnSummaries, u.Meta.Kiro.ElapsedTime)
	case "turn_end":
		// The payload BEFORE the close, so closeTurn can stamp it on the message it
		// appends; the close still keys on the KIND, so a bracket carrying no payload
		// closes the turn exactly as it always did.
		p.noteTurnEnd(u.Meta.Kiro.TurnEnd)
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
		ID:        p.compactionID(at),
		Role:      vibekit.RoleEvent,
		EventKind: vibekit.EventCompacted,
		Content:   sum.Content,
		Ts:        ts,
	}
	p.messages = append(p.messages[:at], append([]vibekit.Message{evt}, p.messages[at:]...)...)
	p.Watermark = evt.ID
	p.compactAt = -1
}

// noteTurnMetering records what the replayed turn_completion frame says this turn
// spent. The credit sum matches persistTurnSummary's — an empty unit or the credit
// unit counts, anything else is a dimension this does not price.
func (p *Projection) noteTurnMetering(summaries []promptTurnSummary, elapsedMs float64) {
	for i := range summaries {
		if summaries[i].Unit == "" || summaries[i].Unit == meteringUnitCredit {
			p.pending.credits += summaries[i].Usage
		}
	}
	p.pending.elapsedMs = elapsedMs
}

// noteTurnEnd reads the replayed turn_end payload into the open turn's facts; a nil
// payload is a no-op, so the caller hands it whatever the frame carried. The outcome
// mapping is delegated, never re-implemented, and the reason is forward compatibility: 0
// of 1,472 persisted turn_end records carry stopDetails.
func (p *Projection) noteTurnEnd(e *turnEndBlock) {
	if e == nil {
		return
	}
	c := vibekit.ConcludeStopReason(vibekit.StopReason(e.StopReason))
	c.Reason = displayText(stopDetailsText(e.StopDetails))
	p.pending.conclusion = &c
}

// compactionID is the id for the compaction event at position `at`, DERIVED so two loads
// of one session mint the same one.
//
// A generated id made every load produce a row the record did not hold, so the reader saw
// the same 12-16 KB summary twice (measured: five pairs in one chat), and it moved the
// watermark, so a compacted chat wrote on every load. The predecessor's id is unique per
// boundary; position 0 has none, hence the counter.
func (p *Projection) compactionID(at int) string {
	if at > 0 {
		return p.messages[at-1].ID + compactedIDSuffix
	}
	p.compactSeq++
	return "compacted-" + strconv.Itoa(p.compactSeq)
}

// compactedIDSuffix marks a projected compaction event's derived id.
const compactedIDSuffix = "-compacted"

func (p *Projection) ensureTurn() {
	if !p.turnOpen {
		p.flushUser()
		p.openTurn()
	}
}

func (p *Projection) openTurn() {
	// The window closes when a turn OPENS, and both callers run flushUser() first, so the
	// prompt's own flush still sees the candidates and the clear lands one line later.
	// In closeTurn it would empty at turn_end, before the resent prompt arrives.
	p.steerCandidates = nil
	p.buf = buffer.New()
	p.turnOpen = true
	p.turnID = ""
	p.turnStart = 0
	p.toolStarts = nil
	p.pending = pendingTurn{}
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
	// Taken and cleared on EVERY path out, so a turn that produced nothing cannot
	// leave its metering to be stamped on the next one.
	facts := p.pending
	p.pending = pendingTurn{}
	if b == nil {
		return
	}
	// Settle anything the marker filter withheld before the emptiness check reads
	// Content — a turn whose only text was a held candidate would be judged empty.
	FlushSteerCarry(b)
	// A replayed non-terminal tool call is stale BY CONSTRUCTION, so settling it here
	// is a statement of fact rather than a guess: KAS refuses session/load on a busy
	// session, so everything this projection sees is history, and the process that
	// owned the call is gone. Without it a turn whose process died mid-call replays a
	// delegate card that spins for the chat's whole life. The buffer's own method, so the
	// rule has ONE owner rather than a second loop here.
	b.MarkInFlightToolsAborted()
	// AFTER the abort, so the snapshot carries the settled statuses. One guarded read
	// rather than field-by-field, which is also what carries Refusal and ChangedFiles.
	snap := b.TakeTurn()
	if snap.EmittedNothing {
		return
	}
	msg := vibekit.Message{
		ID:           p.idOr(p.turnID),
		KASMessageID: p.turnID,
		Role:         vibekit.RoleAssistant,
		Content:      snap.Content,
		Reasoning:    snap.Reasoning,
		Blocks:       snap.Blocks,
		ToolCalls:    snap.ToolCalls,
		Refusal:      snap.Refusal,
		ChangedFiles: snap.ChangedFiles,
		Ts:           p.turnStart,
	}
	facts.stampOn(&msg)
	p.messages = append(p.messages, msg)
}

// stampOn writes the turn's facts onto the message that ends it, which is what restores
// the footer, the outcome word, the rail tint and the turn BOUNDARY — a present
// TurnOutcome is what closes a turn for both projections.
func (t pendingTurn) stampOn(m *vibekit.Message) {
	m.TurnCredits = t.credits
	m.TurnElapsedMs = t.elapsedMs
	if t.conclusion == nil {
		return
	}
	m.TurnOutcome = t.conclusion.Outcome
	m.TurnStopReasonRaw = t.conclusion.RawStop
	m.TurnTruncated = t.conclusion.Truncated
	m.TurnFailureReason = t.conclusion.Reason
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
	id, kasID, ts, kind := p.userID, p.userKASID, p.userTs, p.userKind
	tag := p.userTag
	p.userText, p.userID, p.userKASID, p.userTs, p.userKind = "", "", "", 0, ""
	p.userTag = ""
	p.userPending = false
	if text == "" {
		return
	}
	switch {
	case kind == vibekit.UserKindSteer && id != "":
		// A wire-supplied id is the requirement: idOr mints one for a chunk that carried
		// none, and a minted id names no row a later mark could find.
		p.steerCandidates = append(p.steerCandidates, steerCandidate{id: id, text: text})
	case kind == "" && tag != "":
		p.markResentSteers(text)
	}
	p.messages = append(p.messages, vibekit.Message{
		ID:           p.idOr(id),
		Role:         vibekit.RoleUser,
		UserKind:     kind,
		KASMessageID: kasID,
		Content:      text,
		Ts:           ts,
	})
}

// steerCandidate is one steer row a resend could name: its wire id and its text.
type steerCandidate struct {
	id   string
	text string
}

// markResentSteers stamps SteerStateDropped on every steer row this prompt re-sent.
//
// A row with no state renders under the label a DELIVERED steer gets, so nothing on screen
// says why the identical prompt below it exists. It STAMPS rather than deletes: the live
// store holds both rows too, so a deletion here would make a resumed chat disagree with a
// live one. EVERY match rather than the nearest, because two steers with identical content
// share one prompt on the measured volume.
func (p *Projection) markResentSteers(promptText string) {
	kept := p.steerCandidates[:0]
	for _, c := range p.steerCandidates {
		_, acked := p.ackedSteers[c.id]
		if !acked && c.text == promptText {
			p.markSteerDropped(c.id)
			continue
		}
		kept = append(kept, c)
	}
	p.steerCandidates = kept
}

// markSteerDropped finds the emitted steer row by id and marks it unread.
//
// BACKWARD, because the row is near the tail and applySummary can insert a compaction event
// ahead of it, so an index recorded at emit time would address the wrong row.
func (p *Projection) markSteerDropped(id string) {
	for i := len(p.messages) - 1; i >= 0; i-- {
		if p.messages[i].ID != id {
			continue
		}
		if p.messages[i].UserKind == vibekit.UserKindSteer {
			p.messages[i].SteerState = vibekit.SteerStateDropped
		}
		return
	}
}

// Messages closes any still-open turn and returns the projected transcript as a fresh
// slice, so a caller appending cannot write into the projection's backing array.
// Idempotent: closeTurn and flushUser are both no-ops once they have run.
func (p *Projection) Messages() []vibekit.Message {
	p.closeTurn()
	p.flushUser()
	return slices.Clone(p.messages)
}
