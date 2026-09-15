package agent

import (
	"strconv"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The measured maxima, one per dimension, taken over the live chat volume. They are the
// whole reason liveTurnGETCaps is sized the way it is: the transcript GET is the one
// channel that carries the newest turn while it is in flight, the newest turn is served
// whole, and a cap that cuts the ordinary turn would withhold the reply a reader came for.
//
// They are NOT mutually consistent — each is the maximum over a different chat — so the
// fixture below satisfies each one as a FLOOR rather than reproducing one real turn.
const (
	maxTurnReasoningBytes   = 774_867
	maxTurnContentBytes     = 71_191
	maxTurnBlockTextBytes   = 804_520
	maxTurnBlocks           = 3_548
	maxTurnToolCalls        = 2_804
	maxTurnToolOutputOne    = 65_536
	maxTurnToolOutputTotal  = 3_467_593
	liveTurnGETMaxTextBytes = 10_616_832
)

// splitEvenly divides total across n parts, putting the remainder on the first one, so the
// parts sum to EXACTLY total. A fixture that merely approximates the measured maximum
// cannot say whether a cap sits above it or one byte below.
func splitEvenly(total, n int) []int {
	parts := make([]int, n)
	base := total / n
	for i := range parts {
		parts[i] = base
	}
	parts[0] += total - base*n
	return parts
}

// fill returns n bytes of c. strings.Repeat with a one-byte string, spelled here so the
// fixture's byte counts are obviously the byte counts asserted on.
func fill(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// maximalTurn builds a buffer that meets or exceeds the measured maximum of EVERY
// dimension at once, through the buffer's own Append* methods so the block assembly is
// the production one rather than a hand-built Blocks array.
//
// A distinct subtask id per iteration is load-bearing: a same-subtask thinking delta
// EXTENDS the newest thinking block of that subtask, so one id would leave two blocks
// holding everything and the block-count dimension would never be exercised.
func maximalTurn(tb testing.TB) *buffer.Buffer {
	tb.Helper()

	// The text/thinking half. Every delta lands in the flat builder AND in its own block,
	// so block text comes out at reasoning+content = 846,058, which clears the measured
	// 804,520 while staying under BlockTextBytes.
	const textStreams = 372
	reasoning := splitEvenly(maxTurnReasoningBytes, textStreams)
	content := splitEvenly(maxTurnContentBytes, textStreams)

	// The tool half: one call at exactly the per-call cap, the rest sharing the remainder
	// of the measured total. 2 x 372 text blocks + one block per tool call = 3,548 blocks,
	// which is the measured maximum exactly.
	rest := splitEvenly(maxTurnToolOutputTotal-maxTurnToolOutputOne, maxTurnToolCalls-1)

	buf := buffer.New()
	buf.StartTurn("m-max")
	for i := range textStreams {
		sub := "sub-" + strconv.Itoa(i)
		buf.AppendThinkingDelta(fill('r', reasoning[i]), sub)
		buf.AppendTextDelta(fill('c', content[i]), sub)
	}
	for i := range maxTurnToolCalls {
		id := "tool-" + strconv.Itoa(i)
		out := maxTurnToolOutputOne
		if i > 0 {
			out = rest[i-1]
		}
		buf.AppendToolUseBlock(id, "")
		buf.AppendToolCall(&vibekit.ToolCall{
			ID:     id,
			Title:  "Run Command",
			Output: fill('o', out),
		})
	}
	return buf
}

// TestLiveTurnGETCaps_CutNothingAtTheMeasuredMaxima is the test the whole cap change
// exists for: a turn at the measured maximum of every dimension AT ONCE has to snapshot
// with truncated == false, or the transcript GET withholds part of the newest turn and the
// "one whole turn" guarantee is a claim the caps contradict.
func TestLiveTurnGETCaps_CutNothingAtTheMeasuredMaxima(t *testing.T) {
	buf := maximalTurn(t)

	snap, ok := buf.SnapshotCapped(liveTurnGETCaps)
	if !ok {
		t.Fatal("snapshot reported no content for a maximal turn")
	}
	msg, truncated := snap.Message, snap.Truncated

	// The fixture's own preconditions, asserted so a shrunken fixture cannot make the
	// truncation check below pass vacuously.
	if got := len(msg.Reasoning); got < maxTurnReasoningBytes {
		t.Errorf("fixture reasoning = %d bytes, want at least the measured max %d", got, maxTurnReasoningBytes)
	}
	if got := len(msg.Content); got < maxTurnContentBytes {
		t.Errorf("fixture content = %d bytes, want at least the measured max %d", got, maxTurnContentBytes)
	}
	blockText := 0
	for _, b := range msg.Blocks {
		blockText += len(b.Text) + len(b.Thinking)
	}
	if blockText < maxTurnBlockTextBytes {
		t.Errorf("fixture block text = %d bytes, want at least the measured max %d", blockText, maxTurnBlockTextBytes)
	}
	if got := len(msg.Blocks); got < maxTurnBlocks {
		t.Errorf("fixture carried %d blocks, want at least the measured max %d", got, maxTurnBlocks)
	}
	if got := len(msg.ToolCalls); got != maxTurnToolCalls {
		t.Errorf("fixture carried %d tool calls, want the measured max %d", got, maxTurnToolCalls)
	}
	toolTotal := 0
	widest := 0
	for _, c := range msg.ToolCalls {
		toolTotal += len(c.Output)
		widest = max(widest, len(c.Output))
	}
	if toolTotal < maxTurnToolOutputTotal {
		t.Errorf("fixture tool output = %d bytes total, want at least the measured max %d",
			toolTotal, maxTurnToolOutputTotal)
	}
	if widest < maxTurnToolOutputOne {
		t.Errorf("fixture widest tool output = %d bytes, want at least the measured max %d",
			widest, maxTurnToolOutputOne)
	}

	if truncated {
		t.Error("truncated = true at the measured maxima: the transcript GET is the one channel " +
			"carrying the newest turn while it is in flight, and that turn is served WHOLE, so a " +
			"cap that cuts an ordinary turn withholds the reply the reader came for")
	}
}

// textCeiling is the worst-case TEXT a caps value admits: the two flat fields, the block
// array's share, and the tool-output product bounded by the aggregate. Zero when any of
// the five text dimensions is unbounded — a partial sum would read as a real ceiling.
func textCeiling(c buffer.SnapshotCaps) int {
	if c.ReasoningBytes <= 0 || c.ContentBytes <= 0 || c.BlockTextBytes <= 0 || c.ToolCalls <= 0 || c.ToolOutputBytes <= 0 {
		return 0
	}
	tools := c.ToolCalls * c.ToolOutputBytes
	if c.ToolOutputTotalBytes > 0 {
		tools = min(tools, c.ToolOutputTotalBytes)
	}
	return c.ReasoningBytes + c.ContentBytes + c.BlockTextBytes + tools
}

// TestLiveTurnGETCaps_StateTheirRunawayCeiling pins the number the caps' doc comment
// publishes, plus the two dimensions that number cannot speak for.
//
// textCeiling's unbounded guard covers the five TEXT dimensions only — dropping any one of
// them makes it report 0, so a silently unbounded reasoning, content, block-text,
// tool-call or per-call-output cap fails the first check. It does READ
// ToolOutputTotalBytes, as the min() that bounds the tool product, so unbounding that one
// fails the first check too by taking the ceiling to the 268 MiB product; the direct check
// below is kept for its failure message rather than because the ceiling cannot see it.
//
// Blocks is the one dimension the ceiling genuinely cannot speak for: it bounds a COUNT
// rather than text, so unbounding it leaves the ceiling at exactly its stated value while
// the per-block envelope cost the caps' own figures rest on becomes unbounded. That is why
// it is asserted directly.
func TestLiveTurnGETCaps_StateTheirRunawayCeiling(t *testing.T) {
	if got, want := textCeiling(liveTurnGETCaps), liveTurnGETMaxTextBytes; got != want {
		t.Errorf("textCeiling(liveTurnGETCaps) = %d, want %d: the guarantee is unbounded without "+
			"a stated ceiling, and a zero here means a text dimension was left unbounded", got, want)
	}
	if liveTurnGETCaps.ToolOutputTotalBytes <= 0 {
		t.Error("ToolOutputTotalBytes is unbounded: the per-call cap times ToolCalls is a 268 MiB " +
			"arithmetic ceiling, so the aggregate is what makes the ceiling above statable")
	}
	if liveTurnGETCaps.Blocks <= 0 {
		t.Error("Blocks is unbounded: capBlocks with a zero count cap admits any number of blocks " +
			"under BlockTextBytes, so the payload's per-block envelope cost stops being bounded " +
			"while the text ceiling keeps reporting the same number")
	}
}

// The GET channel's OWN cutting fixture, sized so its block array is cut on
// BlockTextBytes (1 MiB) and never on Blocks: that dimension is 8192, so cutting it would
// need an 8,193-block fixture built for no other property.
//
// The arithmetic, walked the way capBlocks walks it: blocks 3 and 2 fit whole (remaining
// 638,976 then 229,376), block 1 does not and is KEPT tail-truncated because the remainder
// is non-zero, block 0 is dropped — a base of 1. Four EQUAL deltas summing to exactly the
// cap would fit and cut nothing, which is why the size is not the cap over the count.
const (
	cuttingStreams     = 4
	cuttingStreamBytes = 409_600
)

// fillCuttingTurn writes that fixture into an already-started buffer, so the caps test and
// the payload-copy test in sse_reconnect_replay_test.go share ONE sizing rather than two
// that can drift apart.
//
// Four DISTINCT subtask ids, because a same-subtask delta EXTENDS that subtask's newest
// block: one id would build one block and the cut would have nothing to walk. Text only,
// so the boundary block's Thinking is empty and tailBytes' unbounded-at-n<=0 behaviour
// cannot smuggle a second field past the budget.
//
// One property of the fixture stated so it is not mistaken for a defect: 1.6 MB of block
// text is also 1.6 MB of flat Content, so ContentBytes (128 KiB) cuts too. Harmless — the
// base is a function of the BLOCK array alone, and what is asserted is the base and the
// identity rather than `truncated`.
func fillCuttingTurn(tb testing.TB, buf *buffer.Buffer) {
	tb.Helper()
	for i := range cuttingStreams {
		buf.AppendTextDelta(fill('c', cuttingStreamBytes), "sub-"+strconv.Itoa(i))
	}
	if len(buf.Blocks) != cuttingStreams {
		tb.Fatalf("fixture built %d blocks, want %d: a same-subtask delta extends a block, so the "+
			"cut would have nothing to walk", len(buf.Blocks), cuttingStreams)
	}
	if got := cuttingStreams * cuttingStreamBytes; got <= liveTurnGETCaps.BlockTextBytes {
		tb.Fatalf("fixture block text = %d bytes against a %d-byte cap, so it cuts nothing",
			got, liveTurnGETCaps.BlockTextBytes)
	}
}

// cuttingTurn is neither existing fixture, deliberately. newCapFixture lives in package
// buffer and is unreachable from here; maximalTurn is built for the OPPOSITE property —
// its block text sums to 846,058 bytes, UNDER this cap, so it cuts nothing on this
// dimension.
func cuttingTurn(tb testing.TB) *buffer.Buffer {
	tb.Helper()
	buf := buffer.New()
	buf.StartTurn("m-cut")
	fillCuttingTurn(tb, buf)
	return buf
}

// TestLiveTurnGETCaps_ReportsABaseWhenItCuts is the arithmetic on THIS channel's own
// caps. It is the third fact about the GET channel and it is nobody else's: the base's
// VALUE here comes from liveTurnGETCaps, the one capped snapshot left on the wire, so a base
// computed only under the narrower set would pass every other test and answer 0 here.
//
// Asserted as "> 0 plus the identity leg" rather than as a literal, because a literal
// would be a second copy of the cap arithmetic and would agree with a wrong
// implementation of it; the identity is what pins the value against the ARRAY.
func TestLiveTurnGETCaps_ReportsABaseWhenItCuts(t *testing.T) {
	buf := cuttingTurn(t)

	snap, ok := buf.SnapshotCapped(liveTurnGETCaps)
	if !ok {
		t.Fatal("snapshot reported no content for a cutting turn")
	}
	if !snap.Truncated {
		t.Fatal("truncated = false over a fixture built to exceed the block-text cap, so nothing " +
			"below is measuring a cut")
	}
	if snap.BlockBase <= 0 {
		t.Fatalf("BlockBase = %d over a cut front, want > 0: the transcript GET's cap keeps the "+
			"TAIL of the block array, so a zero base tells the client the window starts where the "+
			"array does and every later message_chunk lands short", snap.BlockBase)
	}
	if len(snap.Message.Blocks) == 0 {
		t.Fatal("the snapshot carried no blocks")
	}
	if got, want := snap.Message.Blocks[0].AgentSubtaskID, buf.Blocks[snap.BlockBase].AgentSubtaskID; got != want {
		t.Errorf("Blocks[0].AgentSubtaskID = %q, want %q (buf.Blocks[%d]): the base does not name "+
			"the block this window starts at", got, want, snap.BlockBase)
	}
}
