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

	msg, _, truncated, ok := buf.SnapshotCapped(liveTurnGETCaps)
	if !ok {
		t.Fatal("snapshot reported no content for a maximal turn")
	}

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

// TestLiveTurnGETCaps_AreNotTheConnectCaps fails on the shape being overturned: the two
// cap sets were once one alias, on the premise that both channels want the reply's TAIL.
// They do not — the GET wants the whole turn — so re-aliasing them has to fail here.
//
// The connect caps' own ceiling is asserted beside it, because the per-connect budget's
// arithmetic depends on it and the two must move independently.
func TestLiveTurnGETCaps_AreNotTheConnectCaps(t *testing.T) {
	if liveTurnGETCaps == connectSnapshotCaps {
		t.Error("liveTurnGETCaps == connectSnapshotCaps: the GET carries the newest turn WHOLE " +
			"while a connect frame carries a tail across up to eight chats inside one budget, so " +
			"one literal cannot answer both")
	}
	if got, want := connectSnapshotCaps.MaxTextBytes(), 52<<10; got != want {
		t.Errorf("connectSnapshotCaps.MaxTextBytes() = %d, want %d: the per-connect budget divides "+
			"this number across the chats that fit, so it may not drift with the GET's caps", got, want)
	}
	// The GET's CEILING is above the connect caps', which is the direction the whole-turn
	// need implies. Stated over the ceiling rather than per dimension on purpose: on
	// ToolOutputTotalBytes the connect caps hold 0, this field's unbounded sentinel, so a
	// per-dimension reading would call the GET's 8 MiB the wider of the two when as a BOUND
	// it is the stricter one.
	if liveTurnGETCaps.MaxTextBytes() <= connectSnapshotCaps.MaxTextBytes() {
		t.Errorf("liveTurnGETCaps.MaxTextBytes() = %d, want more than the connect caps' %d",
			liveTurnGETCaps.MaxTextBytes(), connectSnapshotCaps.MaxTextBytes())
	}
}

// TestLiveTurnGETCaps_StateTheirRunawayCeiling pins the number the caps' doc comment
// publishes, plus the two dimensions that number cannot speak for.
//
// MaxTextBytes' unbounded guard covers the five TEXT dimensions only — dropping any one of
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
	if got, want := liveTurnGETCaps.MaxTextBytes(), liveTurnGETMaxTextBytes; got != want {
		t.Errorf("liveTurnGETCaps.MaxTextBytes() = %d, want %d: the guarantee is unbounded without "+
			"a stated ceiling, and a zero here means a text dimension was left unbounded", got, want)
	}
	if liveTurnGETCaps.ToolOutputTotalBytes <= 0 {
		t.Error("ToolOutputTotalBytes is unbounded: the per-call cap times ToolCalls is a 268 MiB " +
			"arithmetic ceiling, so the aggregate is what makes the ceiling above statable")
	}
	if liveTurnGETCaps.Blocks <= 0 {
		t.Error("Blocks is unbounded: capBlocks with a zero count cap admits any number of blocks " +
			"under BlockTextBytes, so the payload's per-block envelope cost stops being bounded " +
			"while MaxTextBytes keeps reporting the same ceiling")
	}
}

// TestNarrowedConnectCaps_LeavesTheAggregateUnbounded pins the scale trap. `scale` floors
// at 1, so scaling an unbounded 0 would set a 1-BYTE aggregate on the connect path and
// drop every tool output from every snapshot.
func TestNarrowedConnectCaps_LeavesTheAggregateUnbounded(t *testing.T) {
	for _, remaining := range []int{1, 1 << 10, connectSnapshotBudget} {
		caps := narrowedConnectCaps(remaining)
		if caps.ToolOutputTotalBytes != 0 {
			t.Errorf("narrowedConnectCaps(%d).ToolOutputTotalBytes = %d, want 0 (unbounded): the "+
				"connect caps leave it unset, and scaling it would floor at 1 byte and cut every "+
				"tool output", remaining, caps.ToolOutputTotalBytes)
		}
	}
}
