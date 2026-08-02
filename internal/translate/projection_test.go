package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
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
func replayFrame(t *testing.T, kind api.ACPUpdateKind, text, sub string, extra map[string]any) (api.ACPUpdateKind, json.RawMessage) {
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

// ingestAll feeds a sequence of (kind, raw) pairs into a fresh Projection.
func ingestAll(p *Projection, frames [][2]any) {
	for _, f := range frames {
		p.Ingest(f[0].(api.ACPUpdateKind), f[1].(json.RawMessage))
	}
}

// measuredCompactedReplay is the EXACT frame sequence a session/load returns
// for a two-turn session that was then compacted, captured from kiro-cli
// 2.16.0 on 2026-08-02. The trailing untagged catalog frames are omitted:
// hub.handleSessionUpdate routes only replay-tagged frames here.
func measuredCompactedReplay(t *testing.T) [][2]any {
	t.Helper()
	f := func(kind api.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	return [][2]any{
		f(replayUserChunkKind, "Reply with exactly: ONE", "", nil),
		f(api.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(api.ACPUpdateAgentChunk, "ONE", "", nil),
		f(api.ACPUpdateSessionInfo, "", "context_usage", nil),
		f(api.ACPUpdateSessionInfo, "", "turn_completion", nil),
		f(api.ACPUpdateSessionInfo, "", "turn_end", nil),
		f(replayUserChunkKind, "Reply with exactly: TWO", "", nil),
		f(api.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(api.ACPUpdateAgentChunk, "TWO", "", nil),
		f(api.ACPUpdateSessionInfo, "", "context_usage", nil),
		f(api.ACPUpdateSessionInfo, "", "turn_completion", nil),
		f(api.ACPUpdateSessionInfo, "", "turn_end", nil),
		f(api.ACPUpdateSessionInfo, "", "summarization_separator", map[string]any{"summarizationSeparator": true}),
		f(api.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
			"summaryMessage": map[string]any{"content": "## Goal\nRespond exactly."},
		}),
	}
}

// TestProjection_MeasuredCompactedReplay drives the projection with the real
// captured frame sequence and pins the whole resulting transcript.
//
// This is the test that matters: it is the wire, verbatim, rather than a shape
// inferred from prose. The design's own account of this sequence was wrong
// once (an earlier draft had resolveForUIReplay collapsing the summary
// server-side, which it does not), so the fixture is a measurement.
func TestProjection_MeasuredCompactedReplay(t *testing.T) {
	p := NewProjection(seqIDs())
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()

	type want struct {
		role    api.Role
		content string
		kind    api.EventKind
	}
	expect := []want{
		{role: api.RoleUser, content: "Reply with exactly: ONE"},
		{role: api.RoleAssistant, content: "ONE"},
		{role: api.RoleUser, content: "Reply with exactly: TWO"},
		{role: api.RoleAssistant, content: "TWO"},
		{role: api.RoleEvent, content: "## Goal\nRespond exactly.", kind: api.EventCompacted},
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
	if p.Watermark == "" {
		t.Error("Watermark is empty; a replayed compaction must give the caller an id to stamp")
	}
	if p.Watermark != got[4].ID {
		t.Errorf("Watermark = %q, want the compaction event's id %q", p.Watermark, got[4].ID)
	}
}

// TestProjection_TurnBracketsSeparateTurns is the failure this whole file
// exists to prevent.
//
// Live, a turn opens on its first chunk and is finalised from the
// session/prompt response's stopReason. A whole-session replay has no such
// response, so WITHOUT honouring turn_start/turn_end every replayed turn
// merges into a single assistant message with one id — the transcript comes
// back as one giant bubble.
func TestProjection_TurnBracketsSeparateTurns(t *testing.T) {
	p := NewProjection(seqIDs())
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()

	var assistants []api.Message
	for _, m := range got {
		if m.Role == api.RoleAssistant {
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

// TestProjection_UserMessagePrecedesTheBracket pins the ordering the wire
// actually uses: user_message_chunk arrives BEFORE turn_start, so the user
// message is not inside the assistant turn's bracket.
//
// What this catches is flushing the user message at turn CLOSE rather than at
// turn open — that emits the pair backwards, because the assistant message is
// appended on close. It is deliberately NOT sensitive to whether flushUser
// runs just before or just after openTurn: openTurn only allocates a buffer,
// so that ordering cannot affect the output and pretending otherwise would be
// a test asserting an implementation detail.
func TestProjection_UserMessagePrecedesTheBracket(t *testing.T) {
	p := NewProjection(seqIDs())
	ingestAll(p, measuredCompactedReplay(t))
	got := p.Messages()
	if len(got) < 2 {
		t.Fatalf("projected %d messages, want at least 2", len(got))
	}
	if got[0].Role != api.RoleUser || got[1].Role != api.RoleAssistant {
		t.Errorf("first two roles = %s, %s; want user then assistant", got[0].Role, got[1].Role)
	}
}

// TestProjection_CompactionKeepsTheOriginals pins that a replayed compaction
// produces a watermark rather than a deletion — the same model the LIVE path
// uses (handleCompactionCompleted appends an event and stamps
// Chat.CompactionWatermark). Two representations of a compacted transcript
// would be a second source of truth.
func TestProjection_CompactionKeepsTheOriginals(t *testing.T) {
	p := NewProjection(seqIDs())
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

// TestProjection_ToolCallLandsComplete pins that a replayed tool call arrives
// finished rather than stuck in_progress.
//
// A replay always sends the pair: the persisted status is `approved`, which
// KAS maps to in_progress on the way out, and the following update carries the
// terminal status plus the output. Consuming only the tool_call would render a
// permanently-spinning card in every restored transcript.
func TestProjection_ToolCallLandsComplete(t *testing.T) {
	p := NewProjection(seqIDs())
	_, start := replayFrame(t, api.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(api.ACPUpdateSessionInfo, start)

	p.Ingest(api.ACPUpdateToolCall, mustJSON(t, map[string]any{
		"sessionUpdate": string(api.ACPUpdateToolCall),
		"toolCallId":    "tc-1",
		"title":         "Read File",
		"kind":          "read",
		"status":        "in_progress",
		"_meta":         map[string]any{"kiro": map[string]any{"replay": true}},
	}))
	p.Ingest(api.ACPUpdateToolUpdate, mustJSON(t, map[string]any{
		"sessionUpdate": string(api.ACPUpdateToolUpdate),
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
	if tc.Status != api.ToolCompleted {
		t.Errorf("tool status = %q, want %q (the update's terminal status must be folded in)",
			tc.Status, api.ToolCompleted)
	}
	if !strings.Contains(tc.Output, "file body") {
		t.Errorf("tool output = %q, want the update's content", tc.Output)
	}
	// The block array must anchor the tool so the client renders a card
	// rather than dropping it.
	var sawToolBlock bool
	for _, b := range got[0].Blocks {
		if b.Type == api.BlockToolUse && b.ToolCallID == "tc-1" {
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
	p := NewProjection(seqIDs())
	_, start := replayFrame(t, api.ACPUpdateSessionInfo, "", "turn_start", nil)
	p.Ingest(api.ACPUpdateSessionInfo, start)
	_, th := replayFrame(t, api.ACPUpdateThoughtChunk, "weighing options", "", nil)
	p.Ingest(api.ACPUpdateThoughtChunk, th)

	got := p.Messages()
	if len(got) != 1 {
		t.Fatalf("projected %d messages, want 1", len(got))
	}
	if got[0].Reasoning != "weighing options" {
		t.Errorf("Reasoning = %q, want the thought text", got[0].Reasoning)
	}
	var sawThinking bool
	for _, b := range got[0].Blocks {
		if b.Type == api.BlockThinking && b.Thinking == "weighing options" {
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
	p := NewProjection(seqIDs())
	for _, sub := range []string{"turn_start", "turn_end"} {
		_, raw := replayFrame(t, api.ACPUpdateSessionInfo, "", sub, nil)
		p.Ingest(api.ACPUpdateSessionInfo, raw)
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
	p := NewProjection(seqIDs())
	_, raw := replayFrame(t, api.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
		"summaryMessage": map[string]any{"content": "orphan summary"},
	})
	p.Ingest(api.ACPUpdateSessionInfo, raw)

	if got := p.Messages(); len(got) != 0 {
		t.Errorf("projected %d messages, want 0:\n%s", len(got), dumpMessages(got))
	}
	if p.Watermark != "" {
		t.Errorf("Watermark = %q, want empty (no separator preceded the summary)", p.Watermark)
	}
}

func dumpMessages(ms []api.Message) string {
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
