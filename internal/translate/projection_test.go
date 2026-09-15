package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// seqIDs returns a deterministic id generator so projected transcripts can be
// compared by value.
func seqIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("m%d", n)
	}
}

// replayFrame builds one session/update `update` object. sub is the
// _meta.kiro.kind for a session_info_update; extra merges into _meta.kiro.
func replayFrame(t *testing.T, kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) (vibekit.ACPUpdateKind, json.RawMessage) {
	t.Helper()
	kiro := map[string]any{"replay": true}
	if sub != "" {
		kiro["kind"] = sub
	}
	maps.Copy(kiro, extra)
	u := map[string]any{
		"sessionUpdate": string(kind),
		"_meta":         map[string]any{"kiro": kiro},
	}
	if text != "" {
		u["content"] = map[string]any{"type": "text", "text": text}
	}
	return kind, mustJSON(t, u)
}

// pair packs replayFrame's two returns into the shape ingestAll takes, so a
// frame list reads as one line per frame.
func pair(kind vibekit.ACPUpdateKind, raw json.RawMessage) [2]any {
	return [2]any{kind, raw}
}

// ingestAll feeds a sequence of (kind, raw) pairs into a fresh Projection.
func ingestAll(p *Projection, frames [][2]any) {
	for _, f := range frames {
		p.Ingest(f[0].(vibekit.ACPUpdateKind), f[1].(json.RawMessage))
	}
}

// measuredCompactedReplay is the EXACT frame sequence a session/load returns for a
// two-turn session that was then compacted, captured from kiro-cli 2.16.0 on 2026-08-02
// (wire-A.jsonl beside the plan), the two turn frames' own `_meta.kiro` payloads
// included. The ONE fixture in this file that must be the wire rather than a shape
// inferred from prose. The trailing untagged catalog frames are omitted: only
// replay-tagged frames are routed here.
func measuredCompactedReplay(t *testing.T) [][2]any {
	t.Helper()
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	completion := map[string]any{
		"promptTurnSummaries": []map[string]any{
			{"unit": "credit", "unitPlural": "credits", "usage": measuredTurnCredits},
		},
		"elapsedTime": measuredTurnElapsedMs,
		"status":      "success",
	}
	turnEnd := map[string]any{
		"turnEnd":    map[string]any{"stopReason": "end_turn"},
		"stopReason": "end_turn",
	}
	return [][2]any{
		f(replayUserChunkKind, "Reply with exactly: ONE", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(vibekit.ACPUpdateAgentChunk, "ONE", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "context_usage", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_completion", completion),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", turnEnd),
		f(replayUserChunkKind, "Reply with exactly: TWO", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(vibekit.ACPUpdateAgentChunk, "TWO", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "context_usage", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_completion", completion),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", turnEnd),
		f(vibekit.ACPUpdateSessionInfo, "", "summarization_separator", map[string]any{"summarizationSeparator": true}),
		f(vibekit.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
			"summaryMessage": map[string]any{"content": "## Goal\nRespond exactly."},
		}),
	}
}

// The two metering values wire-A.jsonl carries, named so the fixture and the assertions
// cannot drift apart.
const (
	measuredTurnCredits   = 0.11506304842454396
	measuredTurnElapsedMs = 1683
)

// TestProjection_MeasuredCompactedReplay drives the projection with the real
// captured frame sequence and pins the whole resulting transcript.
//
// This is the test that matters: it is the wire, verbatim, rather than a shape
// inferred from prose. The design's own account of this sequence was wrong
// once (an earlier draft had resolveForUIReplay collapsing the summary
// server-side, which it does not), so the fixture is a measurement.
func TestProjection_MeasuredCompactedReplay(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()

	type want struct {
		role    vibekit.Role
		content string
		kind    vibekit.EventKind
		outcome vibekit.TurnOutcome
	}
	expect := []want{
		{role: vibekit.RoleUser, content: "Reply with exactly: ONE"},
		{role: vibekit.RoleAssistant, content: "ONE", outcome: vibekit.TurnOutcomeCompleted},
		{role: vibekit.RoleUser, content: "Reply with exactly: TWO"},
		{role: vibekit.RoleAssistant, content: "TWO", outcome: vibekit.TurnOutcomeCompleted},
		{role: vibekit.RoleEvent, content: "## Goal\nRespond exactly.", kind: vibekit.EventCompacted},
	}
	if len(got) != len(expect) {
		t.Fatalf("projected %d messages, want %d:\n%s", len(got), len(expect), dumpMessages(got))
	}
	for i, w := range expect {
		if got[i].Role != w.role || got[i].Content != w.content || got[i].EventKind != w.kind {
			t.Errorf("message %d = {role:%s content:%q kind:%s}, want {role:%s content:%q kind:%s}",
				i, got[i].Role, got[i].Content, got[i].EventKind, w.role, w.content, w.kind)
		}
		if got[i].TurnOutcome != w.outcome {
			t.Errorf("message %d TurnOutcome = %q, want %q", i, got[i].TurnOutcome, w.outcome)
		}
	}
	// Both assistant rows carry the wire's own metering. Asserted here rather than only
	// in TurnFactsFromTheWire because this fixture is the wire verbatim, so a regression
	// that only shows up on real payloads shows up here.
	for _, i := range []int{1, 3} {
		if got[i].TurnCredits != measuredTurnCredits {
			t.Errorf("message %d TurnCredits = %v, want %v", i, got[i].TurnCredits, measuredTurnCredits)
		}
		if got[i].TurnElapsedMs != measuredTurnElapsedMs {
			t.Errorf("message %d TurnElapsedMs = %v, want %v",
				i, got[i].TurnElapsedMs, measuredTurnElapsedMs)
		}
	}
	if p.Watermark == "" {
		t.Error("Watermark is empty; a replayed compaction must give the caller an id to stamp")
	}
	if p.Watermark != got[4].ID {
		t.Errorf("Watermark = %q, want the compaction event's id %q", p.Watermark, got[4].ID)
	}
}

// TestProjection_TurnBracketsSeparateTurns is the failure this whole file exists to prevent.
// Live, a turn opens on its first chunk and is finalised from the session/prompt response's
// stopReason; a whole-session replay has no such response, so without honouring
// turn_start/turn_end every replayed turn merges into one assistant message with one id.
func TestProjection_TurnBracketsSeparateTurns(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()

	var assistants []vibekit.Message
	for _, m := range got {
		if m.Role == vibekit.RoleAssistant {
			assistants = append(assistants, m)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("got %d assistant messages, want 2 (turns merged?):\n%s", len(assistants), dumpMessages(got))
	}
	if assistants[0].ID == assistants[1].ID {
		t.Error("both turns share one message id; turn brackets were not honoured")
	}
	if strings.Contains(assistants[0].Content, "TWO") {
		t.Errorf("turn one absorbed turn two's text: %q", assistants[0].Content)
	}
}

// TestProjection_UserMessagePrecedesTheBracket pins the ordering the wire uses:
// user_message_chunk arrives BEFORE turn_start, so the user message is not inside the
// assistant turn's bracket. What it catches is flushing the user message at turn CLOSE, which
// emits the pair backwards because the assistant message is appended on close. It is
// deliberately NOT sensitive to flushUser running just before or after openTurn, which only
// allocates a buffer.
func TestProjection_UserMessagePrecedesTheBracket(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()
	if len(got) < 2 {
		t.Fatalf("projected %d messages, want at least 2", len(got))
	}
	if got[0].Role != vibekit.RoleUser || got[1].Role != vibekit.RoleAssistant {
		t.Errorf("first two roles = %s, %s; want user then assistant", got[0].Role, got[1].Role)
	}
}

// TestProjection_CompactionKeepsTheOriginals pins that a replayed compaction
// produces a watermark rather than a deletion — the same model the LIVE path
// uses (handleCompactionCompleted appends an event and stamps
// Chat.CompactionWatermark). Two representations of a compacted transcript
// would be a second source of truth.
func TestProjection_CompactionKeepsTheOriginals(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()

	var texts []string
	for _, m := range got {
		texts = append(texts, m.Content)
	}
	joined := strings.Join(texts, "|")
	for _, pre := range []string{"ONE", "TWO"} {
		if !strings.Contains(joined, pre) {
			t.Errorf("pre-compaction turn %q was dropped; vibekit's model is a watermark, not a deletion.\n%s",
				pre, dumpMessages(got))
		}
	}
}

// A separator arriving MID-TURN splits that turn, so the summary is projected
// between what the model said before the compaction and what it said after.
//
// This is what makes the replay agree with the live path, which seals the same
// boundary itself (agent.BridgeCoordinator.SealTurnSegment): the two must produce
// the same array or a chat's transcript changes shape on reload. The separator
// carries no message id, so its POSITION is the only thing the wire can mean.
func TestProjection_MidTurnSeparatorSplitsTheTurn(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true})),
		pair(replayFrame(t, vibekit.ACPUpdateAgentChunk, "before", "", map[string]any{
			"messageId": "m-pre", "timestamp": "2026-08-02T10:00:00.000Z",
		})),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
			map[string]any{"summarizationSeparator": true})),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
			"summaryMessage": map[string]any{"content": "the summary"},
		})),
		pair(replayFrame(t, vibekit.ACPUpdateAgentChunk, "after", "", map[string]any{
			"messageId": "m-post", "timestamp": "2026-08-02T10:00:05.000Z",
		})),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_end", nil)),
	})
	got := p.Messages()

	type want struct {
		role    vibekit.Role
		content string
		kind    vibekit.EventKind
	}
	expect := []want{
		{role: vibekit.RoleAssistant, content: "before"},
		{role: vibekit.RoleEvent, content: "the summary", kind: vibekit.EventCompacted},
		{role: vibekit.RoleAssistant, content: "after"},
	}
	if len(got) != len(expect) {
		t.Fatalf("projected %d messages, want %d:\n%s", len(got), len(expect), dumpMessages(got))
	}
	for i, w := range expect {
		if got[i].Role != w.role || got[i].Content != w.content || got[i].EventKind != w.kind {
			t.Errorf("message %d = {role:%s content:%q kind:%s}, want {role:%s content:%q kind:%s}",
				i, got[i].Role, got[i].Content, got[i].EventKind, w.role, w.content, w.kind)
		}
	}
	if p.Watermark != got[1].ID {
		t.Errorf("Watermark = %q, want the compaction event's id %q", p.Watermark, got[1].ID)
	}
	// The event inherits the segment it summarises, so it sorts where the
	// compaction happened rather than at the load's wall clock.
	if got[1].Ts != got[0].Ts {
		t.Errorf("event Ts = %d, want segment 1's %d", got[1].Ts, got[0].Ts)
	}
}

// The complement of the case above, and the reason both are pinned: whether KAS replays a
// mid-turn separator at its chronological POSITION is unmeasured, so the wire may deliver one
// after that turn's turn_end. This pins what the projection then does — it reconstructs no
// boundary the wire did not state — so a change that started splitting on a tail separator,
// or stopped splitting on a mid-turn one, fails one of the two.
func TestProjection_TailSeparatorLeavesTheTurnWhole(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true})),
		pair(replayFrame(t, vibekit.ACPUpdateAgentChunk, "before", "", map[string]any{
			"messageId": "m-say", "timestamp": "2026-08-02T10:00:00.000Z",
		})),
		pair(replayFrame(t, vibekit.ACPUpdateAgentChunk, " and after", "", map[string]any{
			"messageId": "m-say", "timestamp": "2026-08-02T10:00:05.000Z",
		})),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_end", nil)),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
			map[string]any{"summarizationSeparator": true})),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
			"summaryMessage": map[string]any{"content": "the summary"},
		})),
	})
	got := p.Messages()

	if len(got) != 2 {
		t.Fatalf("projected %d messages, want 2 (one whole turn, then the event):\n%s",
			len(got), dumpMessages(got))
	}
	if got[0].Role != vibekit.RoleAssistant || got[0].Content != "before and after" {
		t.Errorf("message 0 = {role:%s content:%q}, want the whole reply in one assistant message",
			got[0].Role, got[0].Content)
	}
	if got[1].Role != vibekit.RoleEvent || got[1].EventKind != vibekit.EventCompacted {
		t.Errorf("message 1 = {role:%s kind:%s}, want the compaction event",
			got[1].Role, got[1].EventKind)
	}
	if p.Watermark != got[1].ID {
		t.Errorf("Watermark = %q, want the compaction event's id %q", p.Watermark, got[1].ID)
	}
}

// TestProjection_ToolCallLandsComplete pins that a replayed tool call arrives
// finished rather than stuck in_progress.
//
// A replay always sends the pair: the persisted status is `approved`, which
// KAS maps to in_progress on the way out, and the following update carries the
// terminal status plus the output. Consuming only the tool_call would render a
// permanently-spinning card in every restored transcript.
func TestProjection_ToolCallLandsComplete(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)

	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-1",
		"title":         "Read File",
		"kind":          "read",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-1",
		"status":        "completed",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"type": "text", "text": "file body"}},
		},
		"_meta": map[string]any{"kiro": map[string]any{"replay": true}},
	}))

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1 assistant turn:\n%s", len(got), dumpMessages(got))
	}
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(got[0].ToolCalls))
	}
	tc := got[0].ToolCalls[0]
	if tc.Status != vibekit.ToolCompleted {
		t.Errorf("tool status = %q, want %q (the update's terminal status must be folded in)",
			tc.Status, vibekit.ToolCompleted)
	}
	if !strings.Contains(tc.Output, "file body") {
		t.Errorf("tool output = %q, want the update's content", tc.Output)
	}
	// The block array must anchor the tool so the client renders a card
	// rather than dropping it.
	var sawToolBlock bool
	for _, b := range got[0].Blocks {
		if b.Type == vibekit.BlockToolUse && b.ToolCallID == "tc-1" {
			sawToolBlock = true
		}
	}
	if !sawToolBlock {
		t.Errorf("no tool_use block anchors tc-1; blocks = %+v", got[0].Blocks)
	}
}

// TestProjection_ReasoningBecomesAThinkingBlock pins that agent_thought_chunk
// is projected. Reasoning is the most numerous payload class in a real
// transcript, so omitting it from the vocabulary would silently drop most of
// a restored conversation's content.
func TestProjection_ReasoningBecomesAThinkingBlock(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	_, th := replayFrame(t, vibekit.ACPUpdateThoughtChunk, "weighing options", "", nil)
	p.Ingest(vibekit.ACPUpdateThoughtChunk, th)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1", len(got))
	}
	if got[0].Reasoning != "weighing options" {
		t.Errorf("Reasoning = %q, want the thought text", got[0].Reasoning)
	}
	var sawThinking bool
	for _, b := range got[0].Blocks {
		if b.Type == vibekit.BlockThinking && b.Thinking == "weighing options" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Errorf("no thinking block; blocks = %+v", got[0].Blocks)
	}
}

// TestProjection_EmptyTurnIsDropped pins that a bracket carrying nothing
// produces no message. KAS brackets every turn, including ones whose content
// it suppressed (an empty Reasoning record is dropped upstream), and an empty
// assistant bubble in a restored transcript is a visible artefact.
func TestProjection_EmptyTurnIsDropped(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	for _, sub := range []string{"turn_start", "turn_end"} {
		_, raw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", sub, nil)
		p.Ingest(vibekit.ACPUpdateSessionInfo, raw)
	}
	if got := p.Messages(); len(got) != 0 {
		t.Errorf("projected %d messages from an empty turn, want 0:\n%s", len(got), dumpMessages(got))
	}
}

// TestProjection_SummaryWithoutSeparatorIsIgnored pins the guard on the
// marker pair. A summary_message with no preceding separator has no segment
// to mark, and inventing a boundary from it would put the watermark in an
// arbitrary place.
func TestProjection_SummaryWithoutSeparatorIsIgnored(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, raw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
		"summaryMessage": map[string]any{"content": "orphan summary"},
	})
	p.Ingest(vibekit.ACPUpdateSessionInfo, raw)

	if got := p.Messages(); len(got) != 0 {
		t.Errorf("projected %d messages, want 0:\n%s", len(got), dumpMessages(got))
	}
	if p.Watermark != "" {
		t.Errorf("Watermark = %q, want empty (no separator preceded the summary)", p.Watermark)
	}
}

func dumpMessages(ms []vibekit.Message) string {
	var b strings.Builder
	for i, m := range ms {
		fmt.Fprintf(&b, "  [%d] role=%s kind=%s id=%s content=%q tools=%d\n",
			i, m.Role, m.EventKind, m.ID, m.Content, len(m.ToolCalls))
	}
	if b.Len() == 0 {
		return "  (none)"
	}
	return b.String()
}

// probe23Turn is one turn of a capture from kiro-cli 2.16.0 (2026-08-01) with its REAL
// messageId and timestamp values. The ids are the measured shapes — a bare uuid for the user
// message, `<toolCallId>-call` / `-result` for the tool pair, `<uuid>-say` for agent text —
// because the projection's identity rules key on which frame arrives first, and a synthetic
// id would not exercise that.
func probe23Turn(t *testing.T) [][2]any {
	t.Helper()
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	toolCall := mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tooluse_bNV19vGaS2y5nx7WcVCyFx",
		"title":         "Write File",
		"kind":          "edit",
		"status":        "in_progress",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay":    true,
			"messageId": "tooluse_bNV19vGaS2y5nx7WcVCyFx-call",
			"timestamp": "2026-08-01T00:33:15.522Z",
		}},
	})
	return [][2]any{
		f(replayUserChunkKind, "Create probe23.txt containing MANGO, then say done.", "", map[string]any{
			"messageId": "ca4b4050-d45b-44d9-8a99-f72e79cc2767",
			"timestamp": "2026-08-01T00:33:12.051Z",
		}),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		{vibekit.ACPUpdateToolCall, toolCall},
		f(vibekit.ACPUpdateAgentChunk, "Done.", "", map[string]any{
			"messageId": "2f5d57c4-152e-4825-8dcf-fda9668b4693-say",
			"timestamp": "2026-08-01T00:33:17.880Z",
		}),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", nil),
	}
}

// TestProjection_AdoptsWireIdentity pins that the projection takes its message ids and
// timestamps FROM THE WIRE. Both halves are load-bearing: a fabricated id makes the
// projection non-deterministic, so the same stored session projects differently on every load
// and a resume has no message id to address, and a wall-clock timestamp makes a resumed
// transcript claim all of its history happened at the moment of the resume.
func TestProjection_AdoptsWireIdentity(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, probe23Turn(t))
	got := p.Messages()

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (user + assistant turn)", len(got))
	}

	// The user message keeps the id KAS echoed back — which for a real prompt
	// is the id vibekit itself generated and sent on session/prompt.
	if got[0].ID != "ca4b4050-d45b-44d9-8a99-f72e79cc2767" {
		t.Errorf("user id = %q, want the wire's messageId", got[0].ID)
	}
	if got[0].Ts != 1785544392051 {
		t.Errorf("user ts = %d, want 1785544392051 (2026-08-01T00:33:12.051Z)", got[0].Ts)
	}

	// The assistant turn adopts its FIRST content frame's identity. Here that
	// is the tool call, not the later agent text.
	if got[1].ID != "tooluse_bNV19vGaS2y5nx7WcVCyFx-call" {
		t.Errorf("assistant id = %q, want the first in-turn frame's messageId", got[1].ID)
	}
	if got[1].Ts != 1785544395522 {
		t.Errorf("assistant ts = %d, want 1785544395522 (the tool call's timestamp)", got[1].Ts)
	}

	// No id may come from the generator on this input: every content frame
	// carried one. seqIDs() hands out "m1", "m2", ... so a generated id is
	// recognisable.
	for i, m := range got {
		if strings.HasPrefix(m.ID, "m") && len(m.ID) <= 3 {
			t.Errorf("message %d id = %q: generated despite the wire carrying one", i, m.ID)
		}
	}
}

// TestProjection_IsDeterministicAcrossLoads is the property the wire ids buy:
// projecting the same stored session twice yields byte-identical identity.
//
// This is what makes the projection safe to swap into a chat record. A second
// resume must not renumber the transcript, or every reconnecting client sees a
// wholly new set of messages and the client store's upsert-by-id merge (see
// vibekit.md "ingestMessage") duplicates the entire history.
func TestProjection_IsDeterministicAcrossLoads(t *testing.T) {
	// The two loads get DISTINGUISHABLE generators on purpose. Production's
	// generator is time+random based, so a fabricated id differs between
	// loads; two fresh seqIDs() would both hand out "m1" and hide exactly the
	// defect this test exists to catch.
	prefixIDs := func(p string) func() string {
		n := 0
		return func() string {
			n++
			return fmt.Sprintf("%s%d", p, n)
		}
	}

	first := NewProjection(prefixIDs("load1-"), "")
	ingestAll(first, probe23Turn(t))
	a := first.Messages()

	second := NewProjection(prefixIDs("load2-"), "")
	ingestAll(second, probe23Turn(t))
	b := second.Messages()

	if len(a) != len(b) {
		t.Fatalf("load 1 produced %d messages, load 2 produced %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("message %d: id %q on load 1, %q on load 2", i, a[i].ID, b[i].ID)
		}
		if a[i].Ts != b[i].Ts {
			t.Errorf("message %d: ts %d on load 1, %d on load 2", i, a[i].Ts, b[i].Ts)
		}
	}
}

// TestProjection_CompactionEventSortsWithItsSegment pins that the compaction
// event inherits the timestamp of the last message it summarises.
//
// The summary_message frame carries no timestamp (measured), so the obvious
// implementation stamps time.Now() — which sorts a replayed compaction to
// "now" and puts it after turns that actually followed it.
func TestProjection_CompactionEventSortsWithItsSegment(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, probe23Turn(t))
	ingestAll(p, [][2]any{
		func() [2]any {
			k, raw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
				map[string]any{"summarizationSeparator": true})
			return [2]any{k, raw}
		}(),
		func() [2]any {
			k, raw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message",
				map[string]any{"summaryMessage": map[string]any{"content": "## Goal\nMANGO."}})
			return [2]any{k, raw}
		}(),
	})
	got := p.Messages()

	last := got[len(got)-1]
	if last.EventKind != vibekit.EventCompacted {
		t.Fatalf("last message event kind = %q, want %q", last.EventKind, vibekit.EventCompacted)
	}
	// The assistant turn it follows is stamped 1785544395522.
	if last.Ts != 1785544395522 {
		t.Errorf("compaction event ts = %d, want 1785544395522 (its segment's last message)", last.Ts)
	}
	if p.Watermark != last.ID {
		t.Errorf("watermark = %q, want the compaction event's id %q", p.Watermark, last.ID)
	}
}

// TestProjection_TwiceCompactedKeepsEveryTurn. Probed on kiro-cli 2.16.0 (2026-08-02): a
// four-turn session compacted after turn 2 and again after turn 4 replays 28 frames — all
// four turns in full plus TWO separator/summary pairs, and the separators carry
// `{summarizationSeparator, kind, replay}` and nothing else, with no id and no count on the
// wire. That is what rules COLLAPSE out: applied positionally twice, the second separator
// subsumes the first summary and the whole transcript becomes one paragraph — and compaction
// fires automatically at 80% context, so a long-lived chat would collapse on every resume.
func TestProjection_TwiceCompactedKeepsEveryTurn(t *testing.T) {
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	turn := func(prompt, reply string, n int) [][2]any {
		return [][2]any{
			f(replayUserChunkKind, prompt, "", map[string]any{
				"messageId": fmt.Sprintf("user-%d", n),
				"timestamp": fmt.Sprintf("2026-08-02T20:0%d:00.000Z", n),
			}),
			f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
			f(vibekit.ACPUpdateAgentChunk, reply, "", map[string]any{
				"messageId": fmt.Sprintf("agent-%d-say", n),
				"timestamp": fmt.Sprintf("2026-08-02T20:0%d:30.000Z", n),
			}),
			f(vibekit.ACPUpdateSessionInfo, "", "turn_end", nil),
		}
	}
	compaction := func(summary string) [][2]any {
		return [][2]any{
			f(vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
				map[string]any{"summarizationSeparator": true}),
			f(vibekit.ACPUpdateSessionInfo, "", "summary_message",
				map[string]any{"summaryMessage": map[string]any{"content": summary}}),
		}
	}

	p := NewProjection(seqIDs(), "")
	for _, batch := range [][][2]any{
		turn("Reply with exactly: ONE", "ONE", 1),
		turn("Reply with exactly: TWO", "TWO", 2),
		compaction("## Goal\nReplied ONE and TWO."),
		turn("Reply with exactly: THREE", "THREE", 3),
		turn("Reply with exactly: FOUR", "FOUR", 4),
		compaction("## Goal\nReplied ONE, TWO, THREE, FOUR."),
	} {
		ingestAll(p, batch)
	}
	got := p.Messages()

	// 4 turns x (user + assistant) + 2 compaction events.
	if len(got) != 10 {
		var shape []string
		for _, m := range got {
			shape = append(shape, fmt.Sprintf("%s/%s", m.Role, m.EventKind))
		}
		t.Fatalf("got %d messages, want 10 (4 user + 4 assistant + 2 compaction): %v",
			len(got), shape)
	}

	// EVERY original turn survives both compactions. This is the assertion the
	// design's risk table inverted.
	for _, wantText := range []string{"ONE", "TWO", "THREE", "FOUR"} {
		found := false
		for _, m := range got {
			if m.Role == vibekit.RoleAssistant && m.Content == wantText {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("assistant turn %q is absent: a compaction collapsed it", wantText)
		}
	}

	// Both compaction events are present, in order, at their separators.
	var events []int
	for i, m := range got {
		if m.EventKind == vibekit.EventCompacted {
			events = append(events, i)
		}
	}
	if len(events) != 2 {
		t.Fatalf("got %d compaction events at %v, want 2", len(events), events)
	}
	if events[0] != 4 {
		t.Errorf("first compaction event at index %d, want 4 (after turns 1-2)", events[0])
	}
	if events[1] != 9 {
		t.Errorf("second compaction event at index %d, want 9 (after turns 3-4)", events[1])
	}

	// The watermark tracks the LATEST compaction, so context-ui.ts's
	// summarizedCount counts everything summarised so far rather than only the
	// first segment.
	if p.Watermark != got[events[1]].ID {
		t.Errorf("watermark = %q, want the SECOND compaction event's id %q",
			p.Watermark, got[events[1]].ID)
	}

	// And the count that motivated keeping them is non-degenerate: 9 messages
	// precede the watermark. Under collapse it would be 1.
	summarized := 0
	for _, m := range got {
		summarized++
		if m.ID == p.Watermark {
			break
		}
	}
	if summarized != 10 {
		t.Errorf("summarizedCount would be %d, want 10 (all messages up to and including the watermark)", summarized)
	}
}

// TestProjection_WorkflowProgressIsNotUserProse: KAS's persistWorkflowEvent (2.16.0 bundle)
// writes a run's progress onto the LAUNCHING chat's transcript as `{type:"user",
// source:"steer", content: JSON.stringify(...)}` with id `wf-progress-<uuid>` and
// `_meta.kiro.notification.kind: "workflow-progress"`, so it replays as a user_message_chunk
// whose content is a JSON blob. Both discriminators are covered because only one is verified
// to reach the wire: messageId is measured on every content frame, the nested block is not.
func TestProjection_WorkflowProgressIsNotUserProse(t *testing.T) {
	blob := `{"method":"workflow/nodeCompleted","workflowId":"wf-1"}`

	cases := map[string]map[string]any{
		"by id prefix": {
			"messageId": "wf-progress-3f2b1a04-0000-4000-8000-000000000000",
			"timestamp": "2026-08-02T20:01:00.000Z",
		},
		"by notification kind": {
			"messageId":    "some-other-id",
			"timestamp":    "2026-08-02T20:01:00.000Z",
			"notification": map[string]any{"kind": "workflow-progress", "workflowId": "wf-1"},
		},
	}

	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			k, raw := replayFrame(t, replayUserChunkKind, blob, "", meta)
			p.Ingest(k, raw)
			// A real prompt after it, so the test cannot pass merely because
			// the projection produced nothing at all.
			k2, raw2 := replayFrame(t, replayUserChunkKind, "a real question", "", map[string]any{
				"messageId": "m-real",
				"timestamp": "2026-08-02T20:02:00.000Z",
			})
			p.Ingest(k2, raw2)

			got := p.Messages()
			if len(got) != 1 {
				var contents []string
				for _, m := range got {
					contents = append(contents, m.Content)
				}
				t.Fatalf("got %d messages %q, want 1 (the real prompt only)", len(got), contents)
			}
			if got[0].Content != "a real question" {
				t.Errorf("content = %q, want the real prompt", got[0].Content)
			}
			if strings.Contains(got[0].Content, "workflowId") {
				t.Error("the workflow JSON leaked into a user message")
			}
		})
	}
}

// TestProjection_StepNoticeIsNotUserProse: KAS's deliverSendMessage writes a step's own
// message onto the LAUNCHING chat's transcript as `{type:"user", source:"steer"}` with id
// `notify-<uuid>` and kind `system-notification`, so it replays on exactly the frame a real
// prompt does and neither workflow-progress discriminator matches it. KEPT rather than
// dropped, unlike a workflow-progress row: this is prose a step addressed to a person and the
// only durable copy of a question the ask registry holds in memory, so the assertion is on
// the ROLE rather than on absence.
func TestProjection_StepNoticeIsNotUserProse(t *testing.T) {
	const question = "Which branch should I target?"

	cases := map[string]map[string]any{
		"by id prefix": {
			"messageId": "notify-3f2b1a04-0000-4000-8000-000000000000",
			"timestamp": "2026-08-02T20:01:00.000Z",
		},
		"by notification kind": {
			"messageId":    "some-other-id",
			"timestamp":    "2026-08-02T20:01:00.000Z",
			"notification": map[string]any{"kind": "system-notification", "status": "warning"},
		},
	}

	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			k, raw := replayFrame(t, replayUserChunkKind, question, "", meta)
			p.Ingest(k, raw)
			// A real prompt after it, so the two cannot be conflated and the test
			// cannot pass by the projection producing nothing at all.
			k2, raw2 := replayFrame(t, replayUserChunkKind, "a real question", "", map[string]any{
				"messageId": "m-real",
				"timestamp": "2026-08-02T20:02:00.000Z",
			})
			p.Ingest(k2, raw2)

			got := p.Messages()
			if len(got) != 2 {
				t.Fatalf("got %d messages, want 2 (the step's note and the real prompt)", len(got))
			}
			if got[0].Role != vibekit.RoleEvent {
				t.Errorf("the step's note has role %q, want %q", got[0].Role, vibekit.RoleEvent)
			}
			if got[0].EventKind != vibekit.EventStepNotice {
				t.Errorf("event_kind = %q, want %q", got[0].EventKind, vibekit.EventStepNotice)
			}
			if got[0].Content != question {
				t.Errorf("content = %q, want the question verbatim", got[0].Content)
			}
			// The real prompt is untouched: a note arriving mid-accumulation must
			// not splice itself into a user message's text.
			if got[1].Role != vibekit.RoleUser || got[1].Content != "a real question" {
				t.Errorf("second message = %q/%q, want a user prompt", got[1].Role, got[1].Content)
			}
		})
	}
}

// A compaction that lands before any message still records its summary. The
// separator arrives at position 0 on a session whose whole history was
// compacted, which is the one boundary where "no pending compaction" and
// "compaction pending at the start" are the same number if the guard is off by
// one — and where reaching back for the previous message's timestamp has
// nothing to reach.
func TestProjection_CompactionAtTheStartOfTheTranscript(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	sep, sepRaw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
		map[string]any{"summarizationSeparator": true})
	p.Ingest(sep, sepRaw)
	sum, sumRaw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message",
		map[string]any{"summaryMessage": map[string]any{"content": "## Goal\nPLUM."}})
	p.Ingest(sum, sumRaw)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1 compaction event:\n%s", len(got), dumpMessages(got))
	}
	if got[0].EventKind != vibekit.EventCompacted {
		t.Errorf("message 0 kind = %q, want %q", got[0].EventKind, vibekit.EventCompacted)
	}
	if got[0].Content != "## Goal\nPLUM." {
		t.Errorf("message 0 content = %q, want the summary text", got[0].Content)
	}
}

// One separator entitles the replay to one summary. A second summary_message
// with no separator of its own has no boundary to sit at, so it is dropped
// rather than inserted at whatever position the last one happened to leave
// behind.
func TestProjection_SecondSummaryWithoutItsOwnSeparatorIsDropped(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, probe23Turn(t))
	sep, sepRaw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
		map[string]any{"summarizationSeparator": true})
	p.Ingest(sep, sepRaw)
	for _, text := range []string{"## Goal\nFIRST.", "## Goal\nSECOND."} {
		sum, sumRaw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message",
			map[string]any{"summaryMessage": map[string]any{"content": text}})
		p.Ingest(sum, sumRaw)
	}

	compactions := 0
	for _, m := range p.Messages() {
		if m.EventKind == vibekit.EventCompacted {
			compactions++
		}
	}
	if compactions != 1 {
		t.Errorf("projected %d compaction events, want 1:\n%s", compactions, dumpMessages(p.Messages()))
	}
}

// A replayed tool_call_update refines the card the tool_call opened: a field it
// carries is applied, and a field it omits leaves what is already there. KAS
// sends title, kind and locations nullish on an update, so treating absence as
// an instruction empties a complete card mid-replay.
func TestProjection_ToolUpdateAppliesPresentFieldsAndKeepsAbsentOnes(t *testing.T) {
	tests := []struct {
		name          string
		update        map[string]any
		wantTitle     string
		wantKind      vibekit.ToolKind
		wantLocations int
	}{
		{
			name:          "the_update_refines_every_field",
			update:        map[string]any{"title": "Read config.yaml", "kind": "edit", "locations": []map[string]any{{"path": "b.go"}, {"path": "c.go"}}},
			wantTitle:     "Read config.yaml",
			wantKind:      vibekit.ToolKind("edit"),
			wantLocations: 2,
		},
		{
			name:          "the_update_carries_only_a_status",
			update:        map[string]any{},
			wantTitle:     "Read File",
			wantKind:      vibekit.ToolKind("read"),
			wantLocations: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
			p.Ingest(vibekit.ACPUpdateSessionInfo, start)
			p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
				"sessionUpdate": string(vibekit.ACPUpdateToolCall),
				"toolCallId":    "tc-1",
				"title":         "Read File",
				"kind":          "read",
				"status":        "in_progress",
				"locations":     []map[string]any{{"path": "a.go"}},
				"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
			}))
			update := map[string]any{
				"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
				"toolCallId":    "tc-1",
				"status":        "completed",
				"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
			}
			maps.Copy(update, tc.update)
			p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, update))

			got := p.Messages()
			if len(got) != 1 || len(got[0].ToolCalls) != 1 {
				t.Fatalf("projected %d messages, want 1 with 1 tool call:\n%s", len(got), dumpMessages(got))
			}
			card := got[0].ToolCalls[0]
			if card.Title != tc.wantTitle {
				t.Errorf("tool title after the update = %q, want %q", card.Title, tc.wantTitle)
			}
			if card.Kind != tc.wantKind {
				t.Errorf("tool kind after the update = %q, want %q", card.Kind, tc.wantKind)
			}
			if len(card.Locations) != tc.wantLocations {
				t.Errorf("tool locations after the update = %+v, want %d of them", card.Locations, tc.wantLocations)
			}
		})
	}
}

// A tool card is stamped with its OWN frame's time, falling back to the turn's
// start only when the frame carries none. A turn spanning minutes of tool work
// otherwise collapses to a single instant, and the transcript loses the order
// the cards happened in.
func TestProjection_ToolCallCarriesItsOwnTimestamp(t *testing.T) {
	const (
		turnStamp = "2026-08-21T10:00:00.000Z"
		toolStamp = "2026-08-21T10:04:30.000Z"
		turnMilli = int64(1787306400000)
		toolMilli = int64(1787306670000)
	)
	tests := []struct {
		name      string
		toolStamp string
		wantTs    int64
	}{
		{name: "the_frame_carries_its_own_time", toolStamp: toolStamp, wantTs: toolMilli},
		{name: "the_frame_carries_none", toolStamp: "", wantTs: turnMilli},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
			p.Ingest(vibekit.ACPUpdateSessionInfo, start)
			// The turn's own start comes from its FIRST content frame, so the
			// say below is what makes the tool frame's stamp a distinguishable
			// second value rather than the same one.
			_, say := replayFrame(t, vibekit.ACPUpdateAgentChunk, "working", "",
				map[string]any{"timestamp": turnStamp})
			p.Ingest(vibekit.ACPUpdateAgentChunk, say)
			p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
				"sessionUpdate": string(vibekit.ACPUpdateToolCall),
				"toolCallId":    "tc-1",
				"title":         "Read File",
				"kind":          "read",
				"status":        "completed",
				"_meta": map[string]any{"kiro": map[string]any{
					"replay": true, "timestamp": tc.toolStamp,
				}},
			}))

			got := p.Messages()
			if len(got) != 1 {
				t.Fatalf("projected %d messages, want 1 assistant turn:\n%s", len(got), dumpMessages(got))
			}
			if len(got[0].ToolCalls) != 1 {
				t.Fatalf("projected %d tool calls, want 1", len(got[0].ToolCalls))
			}
			if got[0].ToolCalls[0].Ts != tc.wantTs {
				t.Errorf("ToolCall.Ts for a frame stamped %q = %d, want %d",
					tc.toolStamp, got[0].ToolCalls[0].Ts, tc.wantTs)
			}
			if got[0].Ts != turnMilli {
				t.Errorf("turn Ts = %d, want %d (the first content frame's time)", got[0].Ts, turnMilli)
			}
		})
	}
}

// TestProjection_SecondTurnStartClosesTheFirstTurn is the missing-turn_end case, and what it
// pins is the operation ORDER in ingestInfo. Opening without closing throws the first turn's
// whole reply away, because openTurn assigns a fresh buffer and nothing reads the old one.
// Flushing the pending user text BEFORE closing gets it wrong the other way: the orphaned
// reply lands after the next prompt's user message, attributing it to the turn that follows.
func TestProjection_SecondTurnStartClosesTheFirstTurn(t *testing.T) {
	f := func(kind vibekit.ACPUpdateKind, text, sub string) [2]any {
		k, raw := replayFrame(t, kind, text, sub, nil)
		return [2]any{k, raw}
	}
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		f(replayUserChunkKind, "first prompt", ""),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start"),
		f(vibekit.ACPUpdateAgentChunk, "ONE", ""),
		// No turn_end: the first turn's bracket never closed.
		f(replayUserChunkKind, "second prompt", ""),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start"),
		f(vibekit.ACPUpdateAgentChunk, "TWO", ""),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end"),
	})
	got := p.Messages()

	want := []struct {
		role    vibekit.Role
		content string
	}{
		{vibekit.RoleUser, "first prompt"},
		{vibekit.RoleAssistant, "ONE"},
		{vibekit.RoleUser, "second prompt"},
		{vibekit.RoleAssistant, "TWO"},
	}
	if len(got) != len(want) {
		t.Fatalf("projected %d messages, want %d:\n%s", len(got), len(want), dumpMessages(got))
	}
	for i := range want {
		if got[i].Role != want[i].role || got[i].Content != want[i].content {
			t.Errorf("message %d = %s %q, want %s %q",
				i, got[i].Role, got[i].Content, want[i].role, want[i].content)
		}
	}
}

// TestProjection_InternalToolIsDropped pins the replay half of the
// internal-tool suppression: KAS's log stores the session-boot cloud-config
// fetch it announced, so without the gate a resumed chat regains the card the
// live stream dropped — stuck at whatever status the log recorded.
func TestProjection_InternalToolIsDropped(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)

	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "cc-1",
		"title":         "Fetching your cloud config",
		"kind":          "other",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true, "toolId": "fetch_cloud_config"}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "cc-1",
		"status":        "completed",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	// The real reply follows, so the turn itself still projects.
	_, chunk := replayFrame(t, vibekit.ACPUpdateAgentChunk, "hello", "", nil)
	p.Ingest(vibekit.ACPUpdateAgentChunk, chunk)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	if n := len(got[0].ToolCalls); n != 0 {
		t.Errorf("projected %d tool calls, want 0 (internal tool must not survive a replay)", n)
	}
	for _, b := range got[0].Blocks {
		if b.Type == vibekit.BlockToolUse {
			t.Errorf("a tool_use block anchors the suppressed internal tool; blocks = %+v", got[0].Blocks)
		}
	}
}

// replaySteerFrame builds a replayed user_message_chunk on KAS's steering channel: the
// `source` discriminator sits at `_meta.kiro.source`, never on the update object.
func replaySteerFrame(t *testing.T, id, text string) (vibekit.ACPUpdateKind, json.RawMessage) {
	t.Helper()
	return replayFrame(t, replayUserChunkKind, text, "", map[string]any{
		"messageId": id,
		"timestamp": "2026-09-08T20:01:00.000Z",
		"source":    "steer",
	})
}

// TestProjection_SteerJoinsTheTurnItWasReadIn: a `steer-` row is a user message the
// reader sent mid-turn, so it is projected as a user row carrying UserKindSteer —
// which is what makes both turn projections join it to the turn already running
// instead of opening one.
func TestProjection_SteerJoinsTheTurnItWasReadIn(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	k, raw := replaySteerFrame(t, "steer-m-1", "use tabs")
	p.Ingest(k, raw)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	if got[0].Role != vibekit.RoleUser {
		t.Errorf("role = %q, want %q", got[0].Role, vibekit.RoleUser)
	}
	if got[0].UserKind != vibekit.UserKindSteer {
		t.Errorf("user_kind = %q, want %q", got[0].UserKind, vibekit.UserKindSteer)
	}
	if got[0].Content != "use tabs" {
		t.Errorf("content = %q, want the steer verbatim", got[0].Content)
	}
	if got[0].TurnOutcome != "" {
		t.Errorf("turn_outcome = %q, want empty — a steer must not be able to open a headerless turn", got[0].TurnOutcome)
	}
}

// TestProjection_EmptyBoundaryRowDoesNotHijackTheNextPrompt is the INVERSION the
// id-change flush exists to prevent, and the reason the field cannot land without it.
//
// KAS's steering-boundary row is `{id:"steering_boundary_<uuid>", content:"",
// source:"steer"}`, on the same frame type a prompt uses. Without the flush the two
// rows merge into one message carrying the FIRST row's identity, so the reader's own
// prompt is persisted as a steer and stops opening its turn — the rail gets WORSE
// than before the field existed.
func TestProjection_EmptyBoundaryRowDoesNotHijackTheNextPrompt(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	k, raw := replaySteerFrame(t, "steering_boundary_3f2b1a04-0000-4000-8000-000000000000", "")
	p.Ingest(k, raw)
	k2, raw2 := replayFrame(t, replayUserChunkKind, "a real question", "", map[string]any{
		"messageId": "m-real",
		"timestamp": "2026-09-08T20:02:00.000Z",
	})
	p.Ingest(k2, raw2)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1 (the prompt only):\n%s", len(got), dumpMessages(got))
	}
	if got[0].ID != "m-real" {
		t.Errorf("id = %q, want m-real — the boundary row must not lend the prompt its identity", got[0].ID)
	}
	if got[0].UserKind != "" {
		t.Errorf("user_kind = %q, want empty — the prompt is not a steer", got[0].UserKind)
	}
	if got[0].Content != "a real question" {
		t.Errorf("content = %q, want the prompt verbatim", got[0].Content)
	}
}

// TestProjection_TwoSteersWithDifferentIDsAreTwoMessages: KAS records one row per
// steer, so two rows must not concatenate into one. Measured on the live volume,
// where a single `steer-` row held two steers run together with no separator.
func TestProjection_TwoSteersWithDifferentIDsAreTwoMessages(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	for _, s := range []struct{ id, text string }{
		{"steer-m-1", "use tabs"},
		{"steer-m-2", "and sort them"},
	} {
		k, raw := replaySteerFrame(t, s.id, s.text)
		p.Ingest(k, raw)
	}

	got := p.Messages()
	if len(got) != 2 {
		t.Fatalf("projected %d messages, want 2:\n%s", len(got), dumpMessages(got))
	}
	for i, want := range []struct{ id, text string }{
		{"steer-m-1", "use tabs"},
		{"steer-m-2", "and sort them"},
	} {
		if got[i].ID != want.id {
			t.Errorf("message %d id = %q, want %q", i, got[i].ID, want.id)
		}
		if got[i].Content != want.text {
			t.Errorf("message %d content = %q, want %q", i, got[i].Content, want.text)
		}
		if got[i].UserKind != vibekit.UserKindSteer {
			t.Errorf("message %d user_kind = %q, want %q", i, got[i].UserKind, vibekit.UserKindSteer)
		}
	}
}

// replayUserRow is one replayed user record: the fields a resend rule reads, named
// rather than positional so two adjacent strings cannot be transposed.
type replayUserRow struct {
	id    string
	ts    string
	text  string
	tag   string
	steer bool
}

func (r replayUserRow) frame(t *testing.T) [2]any {
	t.Helper()
	extra := map[string]any{"messageId": r.id, "timestamp": r.ts}
	if r.steer {
		extra["source"] = "steer"
	}
	if r.tag != "" {
		extra["userMessageTag"] = r.tag
	}
	return pair(replayFrame(t, replayUserChunkKind, r.text, "", extra))
}

func turnStartFrame(t *testing.T) [2]any {
	t.Helper()
	return pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}))
}

func turnEndFrame(t *testing.T, stop string) [2]any {
	t.Helper()
	return pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
		"turnEnd":    map[string]any{"stopReason": stop},
		"stopReason": stop,
	}))
}

func agentChunkFrame(t *testing.T, text string) [2]any {
	t.Helper()
	return pair(replayFrame(t, vibekit.ACPUpdateAgentChunk, text, "", nil))
}

// The specimen resend, record indices 2087-2095 of sess_0fd788b2: the reader typed into a
// running turn, the boundary cleared the steer unread, and the client sent the same text as
// the next turn's prompt. Every id, timestamp and text below is that record's.
const (
	specimenSteerID   = "steer-m-mtugo59a-105a2y83x1vq5k3l"
	specimenSteerTs   = "2026-09-09T18:58:03.590Z"
	specimenSteerText = "failed:\n\nAttached file: report.md"

	specimenBoundaryID = "steering_boundary_65cbb64e-0000-4000-8000-000000000000"
	specimenBoundaryTs = "2026-09-09T18:58:07.492Z"

	specimenPromptID  = "35a4027b-29fa-46c0-92b0-1749cddd1c81"
	specimenPromptTs  = "2026-09-09T18:58:10.943Z"
	specimenPromptTag = "prompt_746336f7-0000-4000-8000-000000000000"
)

// specimenResend is that sequence, with the prompt's text as a parameter so the
// one-byte-different case shares the fixture rather than restating it.
func specimenResend(t *testing.T, promptText string) [][2]any {
	t.Helper()
	return [][2]any{
		turnStartFrame(t),
		replayUserRow{id: specimenSteerID, ts: specimenSteerTs, text: specimenSteerText, steer: true}.frame(t),
		replayUserRow{id: specimenBoundaryID, ts: specimenBoundaryTs, steer: true}.frame(t),
		turnEndFrame(t, "cancelled"),
		replayUserRow{id: specimenPromptID, ts: specimenPromptTs, text: promptText, tag: specimenPromptTag}.frame(t),
		turnStartFrame(t),
	}
}

// TestProjection_AResentSteerIsMarkedNotDelivered is the defect this rule exists for: the
// client reads the note's label off SteerState, so a projected steer with none renders under
// a DELIVERED steer's label beside the identical prompt below it.
func TestProjection_AResentSteerIsMarkedNotDelivered(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, specimenResend(t, specimenSteerText))

	got := p.Messages()
	if len(got) != 2 {
		t.Fatalf("projected %d messages, want 2 (the steer and the prompt):\n%s", len(got), dumpMessages(got))
	}
	if got[0].SteerState != vibekit.SteerStateDropped {
		t.Errorf("steer row steer_state = %q, want %q", got[0].SteerState, vibekit.SteerStateDropped)
	}
	if got[1].SteerState != "" || got[1].UserKind != "" {
		t.Errorf("prompt row = {steer_state:%q, user_kind:%q}, want both empty — the prompt is not a steer",
			got[1].SteerState, got[1].UserKind)
	}
}

// TestProjection_TwoIdenticalSteersAreBothMarked: two steers with byte-identical content
// share one prompt on the measured volume, so the rule filters the whole window rather than
// resolving a nearest pairing.
func TestProjection_TwoIdenticalSteersAreBothMarked(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		turnStartFrame(t),
		replayUserRow{id: "steer-m-first", ts: "2026-09-09T13:22:38.188Z", text: "retry it", steer: true}.frame(t),
		replayUserRow{id: "steer-m-second", ts: "2026-09-09T13:25:43.685Z", text: "retry it", steer: true}.frame(t),
		turnEndFrame(t, "cancelled"),
		replayUserRow{id: "prompt-row", ts: "2026-09-09T13:25:47.920Z", text: "retry it", tag: "prompt_x"}.frame(t),
		turnStartFrame(t),
	})

	got := p.Messages()
	if len(got) != 3 {
		t.Fatalf("projected %d messages, want 3:\n%s", len(got), dumpMessages(got))
	}
	for i := range 2 {
		if got[i].SteerState != vibekit.SteerStateDropped {
			t.Errorf("steer row %d (%s) steer_state = %q, want %q — a single pairing leaves one behind",
				i, got[i].ID, got[i].SteerState, vibekit.SteerStateDropped)
		}
	}
}

// TestProjection_ASteerRetypedInALaterTurnIsNotMarked: the window closes when a turn OPENS,
// which is the structural bound the resend cannot cross — its own prompt opens the next turn.
// A reader retyping the same text a turn later is a new prompt, not a resend.
func TestProjection_ASteerRetypedInALaterTurnIsNotMarked(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		replayUserRow{id: "steer-m-1", ts: "2026-09-09T10:00:00.000Z", text: "run the tests", steer: true}.frame(t),
		turnStartFrame(t),
		agentChunkFrame(t, "done"),
		turnEndFrame(t, "end_turn"),
		replayUserRow{id: "prompt-row", ts: "2026-09-09T10:05:00.000Z", text: "run the tests", tag: "prompt_x"}.frame(t),
		turnStartFrame(t),
	})

	got := p.Messages()
	if len(got) != 3 {
		t.Fatalf("projected %d messages, want 3:\n%s", len(got), dumpMessages(got))
	}
	if got[0].SteerState != "" {
		t.Errorf("steer row steer_state = %q, want empty — a turn opened between the two rows", got[0].SteerState)
	}
}

// TestProjection_AnAckedSteerIsNotMarked: the acknowledgement marker is the one shape in
// which the record can prove the model READ a steer, so marking it unread would assert the
// opposite of what the transcript holds.
func TestProjection_AnAckedSteerIsNotMarked(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		turnStartFrame(t),
		replayUserRow{id: "steer-m-ack", ts: "2026-09-09T22:17:11.000Z", text: "dont commit", steer: true}.frame(t),
		replayUserRow{id: "steering_boundary_1", ts: "2026-09-09T22:17:16.000Z", steer: true}.frame(t),
		agentChunkFrame(t, "Stopped. [STEERING steer-m-ack: left it uncommitted]"),
		turnEndFrame(t, "end_turn"),
		replayUserRow{id: "prompt-row", ts: "2026-09-09T22:17:30.000Z", text: "dont commit", tag: "prompt_x"}.frame(t),
		turnStartFrame(t),
	})

	got := p.Messages()
	if got[0].SteerState != "" {
		t.Errorf("steer row steer_state = %q, want empty — the agent acknowledged it before the prompt arrived", got[0].SteerState)
	}
}

// TestProjection_AnAckAfterThePromptDoesNotUnmark is the measured sess_a83a6598 order
// (indices 579-595): the boundary cleared the steer unread at 22:17:16, the resend carried it
// at 22:17:30, and the ack arrived 43 s LATER inside the turn the resend opened. So the ack
// set is read SYNCHRONOUSLY at flush time; a whole-replay veto would refuse a real resend.
func TestProjection_AnAckAfterThePromptDoesNotUnmark(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		turnStartFrame(t),
		replayUserRow{id: "steer-m-mtw382vo", ts: "2026-09-09T22:17:11.000Z", text: "dont commit, leave it be", steer: true}.frame(t),
		replayUserRow{id: "steering_boundary_b308df53", ts: "2026-09-09T22:17:16.000Z", steer: true}.frame(t),
		turnEndFrame(t, "cancelled"),
		replayUserRow{id: "fed1605b", ts: "2026-09-09T22:17:30.000Z", text: "dont commit, leave it be", tag: "prompt_x"}.frame(t),
		turnStartFrame(t),
		agentChunkFrame(t, "Stopped. [STEERING steer-m-mtw382vo: stopped before committing]"),
		turnEndFrame(t, "end_turn"),
	})

	got := p.Messages()
	if got[0].SteerState != vibekit.SteerStateDropped {
		t.Errorf("steer row steer_state = %q, want %q — an ack arriving after the prompt cannot unmark it",
			got[0].SteerState, vibekit.SteerStateDropped)
	}
}

// TestProjection_AnAckOnlyChunkStillVetoes: a chunk whose whole content was the marker emits
// nothing, so recording the acks after ingestAgentText's empty-text return would lose exactly
// the chunks the conjunct exists for.
func TestProjection_AnAckOnlyChunkStillVetoes(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		turnStartFrame(t),
		replayUserRow{id: "steer-m-only", ts: "2026-09-09T09:00:00.000Z", text: "skip the lint", steer: true}.frame(t),
		replayUserRow{id: "steering_boundary_2", ts: "2026-09-09T09:00:05.000Z", steer: true}.frame(t),
		agentChunkFrame(t, "[STEERING steer-m-only: skipped it]"),
		turnEndFrame(t, "end_turn"),
		replayUserRow{id: "prompt-row", ts: "2026-09-09T09:00:20.000Z", text: "skip the lint", tag: "prompt_x"}.frame(t),
		turnStartFrame(t),
	})

	got := p.Messages()
	if got[0].SteerState != "" {
		t.Errorf("steer row steer_state = %q, want empty — the ack rode a chunk that emitted nothing", got[0].SteerState)
	}
}

// TestProjection_AnUntaggedPromptMarksNothing: the conjunct is stated positively because
// absence of a tag does not mean steering — 705 non-steer user records carry none, and they
// are the step and subagent prompts a resend rule must not reach.
func TestProjection_AnUntaggedPromptMarksNothing(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		turnStartFrame(t),
		replayUserRow{id: "steer-m-untagged", ts: "2026-09-09T11:00:00.000Z", text: "use the cache", steer: true}.frame(t),
		replayUserRow{id: "steering_boundary_3", ts: "2026-09-09T11:00:05.000Z", steer: true}.frame(t),
		turnEndFrame(t, "cancelled"),
		replayUserRow{id: "step-prompt", ts: "2026-09-09T11:00:09.000Z", text: "use the cache"}.frame(t),
		turnStartFrame(t),
	})

	got := p.Messages()
	if got[0].SteerState != "" {
		t.Errorf("steer row steer_state = %q, want empty — an untagged row is not a prompt", got[0].SteerState)
	}
}

// TestProjection_DifferentTextMarksNothing: one byte. The comparison is byte equality
// because a prefix or substring rule would mark a steer the reader then rewrote.
func TestProjection_DifferentTextMarksNothing(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, specimenResend(t, specimenSteerText+"!"))

	got := p.Messages()
	if got[0].SteerState != "" {
		t.Errorf("steer row steer_state = %q, want empty — the prompt is not carrying that steer", got[0].SteerState)
	}
}

// TestProjection_AMarkedSteerKeepsItsIDAndContent: the rule STAMPS. The row count and every
// other field are what a drop would move, and the live store holds both rows too.
func TestProjection_AMarkedSteerKeepsItsIDAndContent(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, specimenResend(t, specimenSteerText))

	got := p.Messages()
	if len(got) != 2 {
		t.Fatalf("projected %d messages, want 2:\n%s", len(got), dumpMessages(got))
	}
	want := vibekit.Message{
		ID:           specimenSteerID,
		Role:         vibekit.RoleUser,
		UserKind:     vibekit.UserKindSteer,
		KASMessageID: specimenSteerID,
		Content:      specimenSteerText,
		SteerState:   vibekit.SteerStateDropped,
		Ts:           replayTS(specimenSteerTs),
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("marked steer row = %+v, want %+v", got[0], want)
	}
}

// TestProjection_UnsettledToolCallIsAborted pins the case a turn close cannot reach:
// the process holding the buffer died mid-call, so no tool_call_update was ever
// persisted and nothing settles the call. Measured on the live volume, 2 of 22
// non-terminal persisted tool calls sat in turns whose outcome was never stamped,
// and each rendered a delegate card spinning for that chat's whole life.
//
// Settling it here is a statement of fact rather than a guess: KAS refuses
// session/load on a busy session, so everything a projection sees is history and
// the process that owned the call is gone.
func TestProjection_UnsettledToolCallIsAborted(t *testing.T) {
	for _, status := range []string{"in_progress", "pending"} {
		t.Run(status, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
			p.Ingest(vibekit.ACPUpdateSessionInfo, start)

			p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
				"sessionUpdate": string(vibekit.ACPUpdateToolCall),
				"toolCallId":    "tc-dead",
				"title":         "Invoke Sub-agent",
				"kind":          "other",
				"status":        status,
				"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
			}))

			got := p.Messages()
			if len(got) != 1 || len(got[0].ToolCalls) != 1 {
				t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
					len(got), dumpMessages(got))
			}
			if tc := got[0].ToolCalls[0]; tc.Status != vibekit.ToolAborted {
				t.Errorf("tool status = %q, want %q: nothing can still settle a replayed call",
					tc.Status, vibekit.ToolAborted)
			}
		})
	}
}

// A replayed user chunk's `_meta.kiro.messageId` is KAS's own RECORD id — the frame is
// built from the record, not from anything the client sent — so it is the id
// revertMultiple accepts as well as the id this projection publishes. Recording it under
// KASMessageID too is what lets rewind read ONE field for a replayed row and a live one.
func TestProjection_AReplayedUserRowCarriesTheKASIDTwice(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	k, raw := replayFrame(t, replayUserChunkKind, "resume", "", map[string]any{
		"messageId": "38572497-a17f-4172-bdfb-7eb82919a378",
		"timestamp": "2026-09-12T10:56:24.374Z",
	})
	p.Ingest(k, raw)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	if got[0].ID != "38572497-a17f-4172-bdfb-7eb82919a378" {
		t.Errorf("id = %q, want the wire's own record id", got[0].ID)
	}
	if got[0].KASMessageID != got[0].ID {
		t.Errorf("kas_message_id = %q, want it to equal the id %q", got[0].KASMessageID, got[0].ID)
	}
}

// TestProjection_TurnFactsFromTheWire pins that the wire's own turn frames carry the
// turn's metering and its outcome, not only their kind.
//
// The payloads are verbatim from a replayed load of a real session (kiro-cli 2.21.4,
// 2026-09-12; the capture is wire-diff-load.jsonl beside the plan).
func TestProjection_TurnFactsFromTheWire(t *testing.T) {
	cases := []struct {
		name        string
		stopReason  string
		wantOutcome vibekit.TurnOutcome
		wantTrunc   bool
	}{
		{
			name: "error is a failed turn", stopReason: "error",
			wantOutcome: vibekit.TurnOutcomeFailed,
		},
		{
			// The one mapping a reader would guess wrong: the model finished the work it
			// was ALLOWED to do, so the turn completed with its answer cut off. Grading
			// it failed would report a bounded turn as broken.
			name: "max_tokens completes and is truncated", stopReason: "max_tokens",
			wantOutcome: vibekit.TurnOutcomeCompleted, wantTrunc: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			ingestAll(p, oneMeteredTurn(t, tc.stopReason))
			got := p.Messages()
			if len(got) != 1 {
				t.Fatalf("projected %d messages, want 1 assistant turn:\n%s",
					len(got), dumpMessages(got))
			}
			m := got[0]
			if m.TurnCredits != 0.115 {
				t.Errorf("TurnCredits = %v, want 0.115 from the turn_completion frame", m.TurnCredits)
			}
			if m.TurnElapsedMs != 1683 {
				t.Errorf("TurnElapsedMs = %v, want 1683 from the turn_completion frame", m.TurnElapsedMs)
			}
			if m.TurnOutcome != tc.wantOutcome {
				t.Errorf("TurnOutcome = %q, want %q for stopReason %q",
					m.TurnOutcome, tc.wantOutcome, tc.stopReason)
			}
			if string(m.TurnStopReasonRaw) != tc.stopReason {
				t.Errorf("TurnStopReasonRaw = %q, want the wire's own %q",
					m.TurnStopReasonRaw, tc.stopReason)
			}
			if m.TurnTruncated != tc.wantTrunc {
				t.Errorf("TurnTruncated = %v, want %v", m.TurnTruncated, tc.wantTrunc)
			}
		})
	}
}

// oneMeteredTurn is a single replayed turn carrying both turn frames, in the order the
// wire sends them: turn_completion, then turn_end. That order is what makes holding the
// facts until the close the only shape that works.
func oneMeteredTurn(t *testing.T, stopReason string) [][2]any {
	t.Helper()
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	return [][2]any{
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(vibekit.ACPUpdateAgentChunk, "answered", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_completion", map[string]any{
			"promptTurnSummaries": []map[string]any{
				{"unit": "credit", "unitPlural": "credits", "usage": 0.115},
			},
			"elapsedTime": 1683,
			"status":      "success",
		}),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
			"turnEnd":    map[string]any{"stopReason": stopReason},
			"stopReason": stopReason,
		}),
	}
}

// TestProjection_TurnEndStopDetailsBecomeTheFailureReason pins the stopDetails decode,
// the one conclusion field no other case in this file reaches.
//
// HAND-BUILT, and that is the honest state: turn_end.stopDetails is 0 of 1,472 persisted
// records, so this proves the DECODER against a wire that sends nothing today. It earns
// its place because the union merge's conclusion unit READS TurnFailureReason, keeping
// the record's only while the merged outcome is non-clean, so a reason the projection
// can never produce leaves half that rule untestable.
func TestProjection_TurnEndStopDetailsBecomeTheFailureReason(t *testing.T) {
	const want = "the model provider refused the request"
	p := NewProjection(seqIDs(), "")
	f := func(sub string, extra map[string]any) {
		k, raw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", sub, extra)
		p.Ingest(k, raw)
	}
	f("turn_start", map[string]any{"turnStart": true})
	_, chunk := replayFrame(t, vibekit.ACPUpdateAgentChunk, "partial answer", "", nil)
	p.Ingest(vibekit.ACPUpdateAgentChunk, chunk)
	f("turn_end", map[string]any{"turnEnd": map[string]any{
		"stopReason":  "error",
		"stopDetails": map[string]any{"message": want},
	}})

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1 assistant turn:\n%s", len(got), dumpMessages(got))
	}
	m := got[0]
	if m.TurnFailureReason != want {
		t.Errorf("TurnFailureReason = %q, want %q from turn_end.stopDetails.message",
			m.TurnFailureReason, want)
	}
	if m.TurnOutcome != vibekit.TurnOutcomeFailed {
		t.Errorf("TurnOutcome = %q, want %q for stopReason error",
			m.TurnOutcome, vibekit.TurnOutcomeFailed)
	}
	if string(m.TurnStopReasonRaw) != "error" {
		t.Errorf("TurnStopReasonRaw = %q, want the wire's own %q", m.TurnStopReasonRaw, "error")
	}
	if m.TurnTruncated {
		t.Error("TurnTruncated = true, want false: an error stop truncates nothing")
	}
}

// A turn_end carrying NO payload must still close the turn: the close keys on the KIND,
// so the projection's own turn separation cannot depend on a field the wire may omit.
//
// The second turn is opened by CONTENT rather than by a turn_start, and that is the whole
// falsifiability of the case: a turn_start closes any open turn first, so with one here a
// close skipped at the turn_end still yields two messages and the assertion passes.
func TestProjection_PayloadlessTurnEndStillClosesTheTurn(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	f := func(sub string, extra map[string]any) {
		k, raw := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", sub, extra)
		p.Ingest(k, raw)
	}
	f("turn_start", map[string]any{"turnStart": true})
	_, chunk := replayFrame(t, vibekit.ACPUpdateAgentChunk, "one", "", nil)
	p.Ingest(vibekit.ACPUpdateAgentChunk, chunk)
	f("turn_end", nil)
	_, chunk2 := replayFrame(t, vibekit.ACPUpdateAgentChunk, "two", "", nil)
	p.Ingest(vibekit.ACPUpdateAgentChunk, chunk2)

	got := p.Messages()
	if len(got) != 2 {
		t.Fatalf("projected %d messages, want 2 turns:\n%s", len(got), dumpMessages(got))
	}
	if got[0].TurnOutcome != "" {
		t.Errorf("first turn outcome = %q, want empty: the frame carried no payload", got[0].TurnOutcome)
	}
}

// A turn's metering must not leak onto the turn AFTER it. The wire sends one
// turn_completion per turn, so a projection that never cleared its accumulator would
// report every later turn's credits as the running total of the session.
func TestProjection_TurnFactsDoNotLeakToTheNextTurn(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	ingestAll(p, oneMeteredTurn(t, "end_turn"))
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) {
		k, raw := replayFrame(t, kind, text, sub, extra)
		p.Ingest(k, raw)
	}
	f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true})
	f(vibekit.ACPUpdateAgentChunk, "second", "", nil)
	f(vibekit.ACPUpdateSessionInfo, "", "turn_end", nil)

	got := p.Messages()
	if len(got) != 2 {
		t.Fatalf("projected %d messages, want 2 turns:\n%s", len(got), dumpMessages(got))
	}
	if got[1].TurnCredits != 0 {
		t.Errorf("second turn TurnCredits = %v, want 0: it had no turn_completion of its own",
			got[1].TurnCredits)
	}
	if got[1].TurnElapsedMs != 0 {
		t.Errorf("second turn TurnElapsedMs = %v, want 0", got[1].TurnElapsedMs)
	}
	if got[1].TurnStopReasonRaw != "" {
		t.Errorf("second turn TurnStopReasonRaw = %q, want empty", got[1].TurnStopReasonRaw)
	}
}

// TestProjection_ToolCallCarriesItsWorkflowID pins the TWO-CHANNEL fact the fixture is
// built from: on a REPLAY `rawOutput` is a bare STRING carrying the tool_result's prose
// sentence, so the id is at `_meta.kiro.workflowId` and nowhere else. With the `_meta`
// read gone and only the rawOutput read left, it goes red.
func TestProjection_ToolCallCarriesItsWorkflowID(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-run",
		"title":         "Run Workflow",
		"kind":          "other",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-run",
		"status":        "completed",
		"rawOutput": "Workflow 'wf_e873501773f0da94' started successfully. Status: running. " +
			"Progress notifications will arrive via send_message.",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay":     true,
			"workflowId": "wf_e873501773f0da94",
		}},
	}))

	got := p.Messages()
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
			len(got), dumpMessages(got))
	}
	if id := got[0].ToolCalls[0].WorkflowID; id != "wf_e873501773f0da94" {
		t.Errorf("WorkflowID = %q, want %q from _meta.kiro.workflowId",
			id, "wf_e873501773f0da94")
	}
}

// The rawOutput channel is the live path's own spelling and must keep working: a client
// reading a chat whose record was written by the live path relies on it, and a KAS build
// that puts the object back on a replayed update should be decoded too.
func TestProjection_WorkflowIDAlsoComesFromRawOutputObject(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-run2",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-run2",
		"status":        "completed",
		"rawOutput":     map[string]any{"workflowId": "wf_fromrawoutput"},
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))

	got := p.Messages()
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
			len(got), dumpMessages(got))
	}
	if id := got[0].ToolCalls[0].WorkflowID; id != "wf_fromrawoutput" {
		t.Errorf("WorkflowID = %q, want %q from rawOutput.workflowId", id, "wf_fromrawoutput")
	}
}

// TestProjection_ToolCallContentBlocks pins the four fields a tool call's content blocks
// carry: a diff, a terminal id, a disclosed-context flag and a policy denial.
//
// The frames are HAND-BUILT, so this proves the DECODER and not the wire: over 797 live
// session logs KAS persists a `type:"diff"` block 0 times, a `type:"terminal"` block 0
// times and a `policyDenial` 0 times.
func TestProjection_ToolCallContentBlocks(t *testing.T) {
	const workDir = "/workspace"
	p := NewProjection(seqIDs(), workDir)
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-edit",
		"title":         "Replace in File",
		"kind":          "edit",
		"status":        "in_progress",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay": true,
			"disclosedContext": map[string]any{
				"type": "skill", "displayName": "app-review",
				"uri": "file:///workspace/.kiro/skills/app-review/SKILL.md",
			},
		}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-edit",
		"status":        "completed",
		"content": []map[string]any{
			// An ABSOLUTE path, which is what pins the normalizer: an unnormalized path
			// keys ChangedFiles differently from a live row's and leaks the workspace
			// root to the client.
			{
				"type": "diff", "path": workDir + "/internal/pkg/file.go",
				"oldText": "before\n", "newText": "after\nmore\n",
			},
			{"type": "terminal", "terminalId": "term-7"},
		},
		"_meta": map[string]any{"kiro": map[string]any{"replay": true}},
	}))

	got := p.Messages()
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
			len(got), dumpMessages(got))
	}
	tc := got[0].ToolCalls[0]
	if tc.Disclosed == nil || tc.Disclosed.DisplayName != "app-review" {
		t.Errorf("Disclosed = %+v, want the skill the call loaded", tc.Disclosed)
	}
	if len(tc.Diffs) != 1 {
		t.Fatalf("got %d diffs, want 1 from the type:\"diff\" content block", len(tc.Diffs))
	}
	if tc.Diffs[0].Path != "internal/pkg/file.go" {
		t.Errorf("diff path = %q, want the workspace-RELATIVE form", tc.Diffs[0].Path)
	}
	if tc.Diffs[0].NewText != "after\nmore\n" {
		t.Errorf("diff NewText = %q, want the block's own newText", tc.Diffs[0].NewText)
	}
	if tc.TerminalID != "term-7" {
		t.Errorf("TerminalID = %q, want %q from the type:\"terminal\" block", tc.TerminalID, "term-7")
	}
}

// A denial arrives when the call is ATTEMPTED, so it can ride the update rather than
// the create. Its own case because the fold site is different from the disclosure's.
func TestProjection_ToolCallCarriesItsPolicyDenial(t *testing.T) {
	p := NewProjection(seqIDs(), "/workspace")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-denied",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-denied",
		"status":        "failed",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay": true,
			"policyDenial": map[string]any{
				"capability": "shell", "resource": "rm -rf /", "scope": "workspace",
				"source": "preset:guarded",
				"matchedRule": map[string]any{
					"capability": "shell", "effect": "ask", "match": []string{"*"},
				},
			},
		}},
	}))

	got := p.Messages()
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
			len(got), dumpMessages(got))
	}
	d := got[0].ToolCalls[0].Denial
	if d == nil {
		t.Fatal("Denial is nil; a refusal must read as a refusal rather than a tool failure")
	}
	if d.Capability != "shell" || d.Rule == nil || d.Rule.Effect != "ask" {
		t.Errorf("Denial = %+v (rule %+v), want the capability and the matched rule",
			d, d.Rule)
	}
}

// TestProjection_RefusalSurvivesAReplay pins a three-line decode gap: `_meta.kiro.refusal`
// is a declared field on the block the projection already unmarshals, and it read only
// the text. Without it a resumed transcript renders a refusal as ordinary prose — no
// category chip, no Rewind or Switch-model CTA.
//
// Recovers no field against today's KAS: 0 of 41,508 persisted assistant records carry
// the block. The decode still closes the gap and the thinking case below pins the gate.
func TestProjection_RefusalSurvivesAReplay(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	_, chunk := replayFrame(t, vibekit.ACPUpdateAgentChunk, "I cannot continue.", "",
		map[string]any{"refusal": map[string]any{
			"category": "harmful_content", "recommendedModel": "claude-sonnet-5",
		}})
	p.Ingest(vibekit.ACPUpdateAgentChunk, chunk)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	if got[0].Refusal == nil {
		t.Fatal("Refusal is nil; the callout cannot render without it")
	}
	if got[0].Refusal.Category != "harmful_content" {
		t.Errorf("Refusal.Category = %q, want %q", got[0].Refusal.Category, "harmful_content")
	}
	if got[0].Refusal.RecommendedModel != "claude-sonnet-5" {
		t.Errorf("Refusal.RecommendedModel = %q, want %q",
			got[0].Refusal.RecommendedModel, "claude-sonnet-5")
	}
}

// The same block on a THINKING chunk must not mark the turn — the live path's gate,
// mirrored, so a stray tagged thought cannot make a normal turn render a refusal.
func TestProjection_ARefusalTaggedThoughtDoesNotMarkTheTurn(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	_, th := replayFrame(t, vibekit.ACPUpdateThoughtChunk, "weighing a refusal", "",
		map[string]any{"refusal": map[string]any{"category": "harmful_content"}})
	p.Ingest(vibekit.ACPUpdateThoughtChunk, th)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	if got[0].Refusal != nil {
		t.Errorf("Refusal = %+v on a thinking-only turn, want nil", got[0].Refusal)
	}
}

// TestProjection_ChangedFilesReachTheTurn pins the turn footer's file ledger: the diffs
// a completed write reports have to reach the closing message through the buffer, or the
// footer loses its per-file rows and its "Review changes (N files)" seam.
//
// Untested against a real wire frame, deliberately: no replayed diff block exists (see
// TestProjection_ToolCallContentBlocks), so this is the decoder end to end.
func TestProjection_ChangedFilesReachTheTurn(t *testing.T) {
	const workDir = "/workspace"
	p := NewProjection(seqIDs(), workDir)
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-w",
		"kind":          "edit",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-w",
		"status":        "completed",
		"content": []map[string]any{
			{
				"type": "diff", "path": workDir + "/a.txt",
				"oldText": "one\ntwo\n", "newText": "one\ntwo\nthree\n",
			},
		},
		"_meta": map[string]any{"kiro": map[string]any{"replay": true}},
	}))

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	fc := got[0].ChangedFiles
	if len(fc) != 1 {
		t.Fatalf("ChangedFiles = %+v, want one entry", fc)
	}
	entry, ok := fc["a.txt"]
	if !ok {
		t.Fatalf("ChangedFiles keyed %v, want the workspace-RELATIVE path %q",
			mapKeys(fc), "a.txt")
	}
	if entry.LinesAdded != 1 || entry.LinesRemoved != 0 {
		t.Errorf("a.txt = +%d/-%d, want +1/-0", entry.LinesAdded, entry.LinesRemoved)
	}
}

// A write that did NOT complete must not enter the ledger: KAS repeats a write's diff
// block on every streaming frame, so counting each arrival would claim a file changed
// when the write failed.
func TestProjection_AFailedWriteDoesNotEnterTheLedger(t *testing.T) {
	const workDir = "/workspace"
	p := NewProjection(seqIDs(), workDir)
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-fail",
		"kind":          "edit",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-fail",
		"status":        "failed",
		"content": []map[string]any{
			{"type": "diff", "path": workDir + "/b.txt", "oldText": "", "newText": "x\n"},
		},
		"_meta": map[string]any{"kiro": map[string]any{"replay": true}},
	}))

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1:\n%s", len(got), dumpMessages(got))
	}
	if len(got[0].ChangedFiles) != 0 {
		t.Errorf("ChangedFiles = %+v on a failed write, want none", got[0].ChangedFiles)
	}
	// The CARD still keeps every diff regardless of status, matching the live path.
	if len(got[0].ToolCalls) != 1 || len(got[0].ToolCalls[0].Diffs) != 1 {
		t.Errorf("the card must still carry the diff; tool calls = %+v", got[0].ToolCalls)
	}
}

// mapKeys is for a failure message naming what a map was keyed by.
func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestProjection_ToolDurationFromFrameTimestamps pins that a replayed tool call gets its
// duration from the two frames' own `_meta.kiro.timestamp` values — 99% of live calls
// carry one and 0% of projected ones did, so every tool row in a resumed transcript was
// untimed.
//
// The load's clock is never an input: a wall-clock reading would report how long the
// resume took, on a call that ran days ago.
func TestProjection_ToolDurationFromFrameTimestamps(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		want       int
	}{
		{
			name:  "the gap between the two frames",
			start: "2026-09-12T10:00:00.000Z", end: "2026-09-12T10:00:01.674Z", want: 1674,
		},
		{
			// Measured on wire-A.jsonl: a fast call's two records share the millisecond,
			// so 0 is the wire's own resolution rather than a decode miss.
			name:  "one millisecond for both frames yields zero",
			start: "2026-09-12T10:00:00.000Z", end: "2026-09-12T10:00:00.000Z", want: 0,
		},
		{
			// An END BEFORE the start is a clock nobody should trust, so nothing is
			// stamped rather than a negative duration reaching the card.
			name:  "an inverted pair yields zero",
			start: "2026-09-12T10:00:01.000Z", end: "2026-09-12T10:00:00.000Z", want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjection(seqIDs(), "")
			_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
			p.Ingest(vibekit.ACPUpdateSessionInfo, start)
			p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
				"sessionUpdate": string(vibekit.ACPUpdateToolCall),
				"toolCallId":    "tc-t",
				"status":        "in_progress",
				"_meta": map[string]any{"kiro": map[string]any{
					"replay": true, "timestamp": tc.start,
				}},
			}))
			p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
				"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
				"toolCallId":    "tc-t",
				"status":        "completed",
				"_meta": map[string]any{"kiro": map[string]any{
					"replay": true, "timestamp": tc.end,
				}},
			}))

			got := p.Messages()
			if len(got) != 1 || len(got[0].ToolCalls) != 1 {
				t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
					len(got), dumpMessages(got))
			}
			if d := got[0].ToolCalls[0].DurationMs; d != tc.want {
				t.Errorf("DurationMs = %d, want %d (start %s, end %s)",
					d, tc.want, tc.start, tc.end)
			}
		})
	}
}

// TestProjection_ToolDurationSkipsACreateCarryingNoTimestamp pins the one case the table
// above cannot express, because every row there gives the create a timestamp of its own.
//
// frameTS falls back to turnStart for a frame carrying none, and turnStart is adopted from
// the FIRST in-turn frame — so a later timestamp-less create takes the text chunk's instant
// as its Ts, and a duration read off that is time-since-turn-start wearing a duration's
// name. Reading the create's raw timestamp instead is what separates the two.
func TestProjection_ToolDurationSkipsACreateCarryingNoTimestamp(t *testing.T) {
	p := NewProjection(seqIDs(), "")
	_, start := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(vibekit.ACPUpdateSessionInfo, start)

	// A text chunk first, so turnStart is a real instant this tool call did not supply.
	p.Ingest(replayFrame(t, vibekit.ACPUpdateAgentChunk, "thinking", "", map[string]any{
		"timestamp": "2026-09-12T10:00:00.000Z", "messageId": "m-1",
	}))
	p.Ingest(vibekit.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolCall),
		"toolCallId":    "tc-nots",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(vibekit.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateToolUpdate),
		"toolCallId":    "tc-nots",
		"status":        "completed",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay": true, "timestamp": "2026-09-12T10:00:09.000Z",
		}},
	}))

	got := p.Messages()
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("projected %d messages, want 1 turn carrying 1 tool call:\n%s",
			len(got), dumpMessages(got))
	}
	if d := got[0].ToolCalls[0].DurationMs; d != 0 {
		t.Errorf("DurationMs = %d, want 0: the create carried no timestamp, so 9000 here is "+
			"the gap since the turn's first chunk rather than anything this call took", d)
	}
}

// TestProjection_CompactionIDIsDerived pins the compaction event's id as DERIVED rather
// than minted, so two loads of one session agree on it.
//
// The derived id is the PREDECESSOR's plus a suffix, so it is exactly as deterministic as
// the row it marks: a boundary following a turn that fell back to a generated id inherits
// that, and nothing can rescue it, since the turn itself is renumbered per load.
func TestProjection_CompactionIDIsDerived(t *testing.T) {
	prefixIDs := func(p string) func() string {
		n := 0
		return func() string {
			n++
			return fmt.Sprintf("%s%d", p, n)
		}
	}
	first := NewProjection(prefixIDs("load1-"), "")
	ingestAll(first, compactedTurnWithWireIDs(t))
	a := first.Messages()

	second := NewProjection(prefixIDs("load2-"), "")
	ingestAll(second, compactedTurnWithWireIDs(t))
	b := second.Messages()

	if len(a) != len(b) {
		t.Fatalf("load 1 produced %d messages, load 2 produced %d", len(a), len(b))
	}
	var sawCompaction bool
	for i := range a {
		if a[i].EventKind != vibekit.EventCompacted {
			continue
		}
		sawCompaction = true
		if a[i].ID != b[i].ID {
			t.Errorf("compaction id = %q on load 1, %q on load 2: a generated id makes "+
				"every load produce a row the record does not hold", a[i].ID, b[i].ID)
		}
		if !strings.HasSuffix(a[i].ID, compactedIDSuffix) {
			t.Errorf("compaction id = %q, want the derived %q suffix", a[i].ID, compactedIDSuffix)
		}
	}
	if !sawCompaction {
		t.Fatalf("no compaction event in the projection:\n%s", dumpMessages(a))
	}
	// The watermark is the same id, so sameWatermark holds on load N+1 too — which is
	// what restores idempotence for a compacted chat.
	if first.Watermark != second.Watermark {
		t.Errorf("Watermark = %q on load 1, %q on load 2", first.Watermark, second.Watermark)
	}
}

// compactedTurnWithWireIDs is one turn followed by a compaction boundary, with the wire's
// own messageId on every content frame — the shape production always sends.
func compactedTurnWithWireIDs(t *testing.T) [][2]any {
	t.Helper()
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	return [][2]any{
		f(replayUserChunkKind, "summarise this", "", map[string]any{
			"messageId": "ca4b4050-d45b-44d9-8a99-f72e79cc2767",
			"timestamp": "2026-09-12T10:00:00.000Z",
		}),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(vibekit.ACPUpdateAgentChunk, "done", "", map[string]any{
			"messageId": "2f5d57c4-152e-4825-8dcf-fda9668b4693-say",
			"timestamp": "2026-09-12T10:00:02.000Z",
		}),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
			"turnEnd": map[string]any{"stopReason": "end_turn"},
		}),
		f(vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
			map[string]any{"summarizationSeparator": true}),
		f(vibekit.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
			"summaryMessage": map[string]any{"content": "## Goal\nSummarised."},
		}),
	}
}

// A compaction at position 0 has no predecessor to derive an id from, so it falls back to
// a counter — which must still agree across two loads, and must give two such boundaries
// two different ids.
func TestProjection_CompactionAtPositionZeroIsStillDeterministic(t *testing.T) {
	build := func() *Projection {
		p := NewProjection(seqIDs(), "")
		for range 2 {
			_, sep := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summarization_separator",
				map[string]any{"summarizationSeparator": true})
			p.Ingest(vibekit.ACPUpdateSessionInfo, sep)
			_, sum := replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "summary_message",
				map[string]any{"summaryMessage": map[string]any{"content": "s"}})
			p.Ingest(vibekit.ACPUpdateSessionInfo, sum)
		}
		return p
	}
	a, b := build().Messages(), build().Messages()
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("projected %d and %d messages, want 2 each:\n%s", len(a), len(b), dumpMessages(a))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("message %d id = %q on load 1, %q on load 2", i, a[i].ID, b[i].ID)
		}
	}
	if a[0].ID == a[1].ID {
		t.Errorf("two boundaries share the id %q; each needs its own", a[0].ID)
	}
}

// The live path and the replay path name one record by one id: the chunk handler
// latches `_meta.kiro.replayId` off a LIVE frame, and a later session/load reports
// that same id as `messageId`. Both halves are asserted against ONE constant, so a
// spelling drift on either side fails here.
//
// The replay frames come from replayFrame, the builder the measured fixture uses;
// `messageId` is the one member the 2.16.0 capture omits, so that much is hand-built
// and proves the DECODE rather than that KAS sends it — measured separately.
func TestAgentSideID_TheLiveLatchAndTheReplayKeyAreOneID(t *testing.T) {
	const wantID = "3f9c1e7a-say"

	deps, _ := newEventCaptureDeps()
	const chatID vibekit.ChatID = "c1"
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "live-1" }))
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": vibekit.ContentTypeText, "text": "ONE"},
		"_meta":   map[string]any{"kiro": map[string]any{"replayId": wantID}},
	}), false)

	if got := deps.bufStore.GetOrInit(chatID).TakeTurn().KASMessageID; got != wantID {
		t.Errorf("the live chunk latched KASMessageID = %q, want %q; the replay's row could not be paired with it",
			got, wantID)
	}

	p := NewProjection(seqIDs(), "")
	ingestAll(p, [][2]any{
		pair(replayFrame(t, replayUserChunkKind, "Reply with exactly: ONE", "", nil)),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true})),
		pair(replayFrame(t, vibekit.ACPUpdateAgentChunk, "ONE", "", map[string]any{"messageId": wantID})),
		pair(replayFrame(t, vibekit.ACPUpdateSessionInfo, "", "turn_end",
			map[string]any{"turnEnd": map[string]any{"stopReason": "end_turn"}})),
	})
	got := p.Messages()
	idx := -1
	for i := range got {
		if got[i].Role == vibekit.RoleAssistant {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("the replay projected no assistant row, so there is nothing to pair:\n%s", dumpMessages(got))
	}
	if agentSide := got[idx].AgentSideID(); agentSide != wantID {
		t.Errorf("the replayed row's AgentSideID() = %q, want %q", agentSide, wantID)
	}
}

// A workflow step's id is never latched onto the launching chat. The step's record
// lives in the STEP's own session log, so this chat's session/load never reports it
// and a row keyed on it would be permanently unpairable.
func TestAgentSideID_AWorkflowStepFrameLatchesNothing(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	const chatID vibekit.ChatID = "c1"
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "live-1" }))
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": vibekit.ContentTypeText, "text": "step prose"},
		"_meta": map[string]any{"kiro": map[string]any{
			"replayId": "step-say",
			"workflow": map[string]any{
				"workflowId": "wf_1",
				"nodePath":   []string{"wf_1", "review"},
				"type":       "step",
			},
		}},
	}), false)

	if got := deps.bufStore.GetOrInit(chatID).TakeTurn().KASMessageID; got != "" {
		t.Errorf("a step frame latched KASMessageID = %q, want it empty; that id names a record this chat's own replay never reports",
			got)
	}
}
