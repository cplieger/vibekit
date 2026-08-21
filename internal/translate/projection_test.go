package translate

import (
	"encoding/json"
	"fmt"
	"maps"
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

// ingestAll feeds a sequence of (kind, raw) pairs into a fresh Projection.
func ingestAll(p *Projection, frames [][2]any) {
	for _, f := range frames {
		p.Ingest(f[0].(vibekit.ACPUpdateKind), f[1].(json.RawMessage))
	}
}

// measuredCompactedReplay is the EXACT frame sequence a session/load returns
// for a two-turn session that was then compacted, captured from kiro-cli
// 2.16.0 on 2026-08-02. The trailing untagged catalog frames are omitted:
// agent.handleSessionUpdate routes only replay-tagged frames here.
func measuredCompactedReplay(t *testing.T) [][2]any {
	t.Helper()
	f := func(kind vibekit.ACPUpdateKind, text, sub string, extra map[string]any) [2]any {
		k, raw := replayFrame(t, kind, text, sub, extra)
		return [2]any{k, raw}
	}
	return [][2]any{
		f(replayUserChunkKind, "Reply with exactly: ONE", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(vibekit.ACPUpdateAgentChunk, "ONE", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "context_usage", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_completion", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", nil),
		f(replayUserChunkKind, "Reply with exactly: TWO", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}),
		f(vibekit.ACPUpdateAgentChunk, "TWO", "", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "context_usage", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_completion", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "turn_end", nil),
		f(vibekit.ACPUpdateSessionInfo, "", "summarization_separator", map[string]any{"summarizationSeparator": true}),
		f(vibekit.ACPUpdateSessionInfo, "", "summary_message", map[string]any{
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
		role    vibekit.Role
		content string
		kind    vibekit.EventKind
	}
	expect := []want{
		{role: vibekit.RoleUser, content: "Reply with exactly: ONE"},
		{role: vibekit.RoleAssistant, content: "ONE"},
		{role: vibekit.RoleUser, content: "Reply with exactly: TWO"},
		{role: vibekit.RoleAssistant, content: "TWO"},
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
	p := NewProjection(seqIDs())
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
	p := NewProjection(seqIDs())
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
	p := NewProjection(seqIDs())
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

// probe23Turn is one turn of the probe-23 capture (kiro-cli 2.16.0,
// 2026-08-01) with its REAL messageId and timestamp values: a user message, a
// bracketed turn whose first content frame is a tool call, then agent text.
//
// The ids are the measured shapes — a bare uuid for the user message,
// `<toolCallId>-call` / `-result` for the tool pair, `<uuid>-say` for agent
// text — because the projection's identity rules key on which frame arrives
// first, and a synthetic id would not exercise that.
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

// TestProjection_AdoptsWireIdentity pins that the projection takes its message
// ids and timestamps FROM THE WIRE rather than inventing them.
//
// Both halves are load-bearing. A fabricated id makes the projection
// non-deterministic, so the same stored session projects differently on every
// load and task 12's "resume addresses a message id" has nothing to address.
// A wall-clock timestamp makes a resumed transcript claim all of its history
// happened at the moment of the resume.
func TestProjection_AdoptsWireIdentity(t *testing.T) {
	p := NewProjection(seqIDs())
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

	first := NewProjection(prefixIDs("load1-"))
	ingestAll(first, probe23Turn(t))
	a := first.Messages()

	second := NewProjection(prefixIDs("load2-"))
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
	p := NewProjection(seqIDs())
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

// TestProjection_TwiceCompactedKeepsEveryTurn is the "twice-compacted" fixture
// the design's risk table asked for, asserting the OPPOSITE of what that table
// specified — and the measurement is why.
//
// Probe (kiro-cli 2.16.0, 2026-08-02): a session of four turns compacted after
// turn 2 and again after turn 4 replays 28 frames — all four original turns in
// full, plus TWO separator/summary pairs at positions 13/14 and 27/28. The
// separators carry `{summarizationSeparator: true, kind, replay}` and nothing
// else; explicit checks for `effectiveFromMessageId`, `truncatedMessageCount`,
// `visibleFrom` and `hidden` all came back absent from every frame on the wire.
// On disk the tombstones DO carry both fields, and the second one's
// effectiveFromMessageId points at the FIRST SUMMARY
// (`summary_0745abfe-...`) — the segments NEST, each summary subsuming the one
// before it.
//
// That is what rules collapse out. Applied positionally twice, separator 1
// replaces turns 1-2 with summary 1 and separator 2 replaces
// [summary 1 + turns 3-4] with summary 2, so the whole transcript becomes one
// summary paragraph. Compaction fires automatically at 80% context, so a
// long-lived chat would collapse to a single blob on EVERY resume — deleting
// the content transcript search and the timeline rail exist to navigate, which
// the design's own risk table classes as "a data-loss bug, not a polish item".
// And the wire cannot even do it faithfully: with no id and no count, position
// is all there is.
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

	p := NewProjection(seqIDs())
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

// TestProjection_WorkflowProgressIsNotUserProse pins that a workflow-progress
// row does not become a user bubble.
//
// KAS's persistWorkflowEvent (2.16.0 bundle) writes a run's progress onto the
// LAUNCHING chat's transcript as `{type: "user", source: "steer", content:
// JSON.stringify({method, ...payload})}` with id `wf-progress-<uuid>` and
// `_meta.kiro.notification.kind: "workflow-progress"`. It therefore replays as
// a user_message_chunk whose content is a JSON blob — rendered as prose it puts
// raw JSON in the transcript attributed to the user.
//
// Both discriminators are covered because only one is verified to reach the
// wire: messageId is measured on every content frame, while the nested
// notification block's survival through KAS's replay mapping is not.
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
			p := NewProjection(seqIDs())
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
