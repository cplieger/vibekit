package buffer

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestBufferSnapshot covers the read that replaced agent/turn_mirror.go's parallel
// replica of the in-flight turn.
//
// The mirror re-folded every broadcast event into its own vibekit.Message — a second
// implementation of the block assembly this package already does, free to drift
// from it. These cases pin that the buffer's own snapshot carries what the transcript
// GET's live_turn needs.
func TestBufferSnapshot(t *testing.T) {
	t.Run("an unstarted turn has no snapshot", func(t *testing.T) {
		var buf Buffer
		if _, ok := buf.SnapshotCapped(SnapshotCaps{}); ok {
			t.Error("snapshot reported content for a buffer with no message id")
		}
	})

	t.Run("a started but silent turn is a bare busy signal", func(t *testing.T) {
		buf := Buffer{MessageID: "m1"}
		snap, ok := buf.SnapshotCapped(SnapshotCaps{})
		if ok {
			t.Errorf("snapshot reported content for an empty turn: %+v", snap.Message)
		}
		if snap.ChunkSeq != 0 {
			t.Errorf("chunk seq = %d, want 0", snap.ChunkSeq)
		}
	})

	t.Run("text, thinking and tools all reach the snapshot", func(t *testing.T) {
		// Mirrors translate/streaming_content.go: one Append*Delta call builds
		// the Blocks array AND accumulates the content/reasoning builder the
		// turn-commit path and the 32 MiB cap both read, so a snapshot has to
		// carry both representations from the one call.
		buf := Buffer{MessageID: "m1"}
		buf.AppendTextDelta("hello ", "")
		buf.AppendThinkingDelta("pondering", "")
		buf.AppendToolUseBlock("tool-1", "")
		buf.AppendToolCall(&vibekit.ToolCall{ID: "tool-1", Title: "Read File"})
		buf.AppendTextDelta("world", "")

		snap, ok := buf.SnapshotCapped(SnapshotCaps{})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		msg := snap.Message
		if msg.ID != "m1" {
			t.Errorf("id = %q, want m1", msg.ID)
		}
		if msg.Role != vibekit.RoleAssistant {
			t.Errorf("role = %q, want assistant", msg.Role)
		}
		if msg.Content != "hello world" {
			t.Errorf("content = %q, want %q", msg.Content, "hello world")
		}
		if msg.Reasoning != "pondering" {
			t.Errorf("reasoning = %q, want %q", msg.Reasoning, "pondering")
		}
		if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "tool-1" {
			t.Errorf("tool calls = %+v, want one tool-1", msg.ToolCalls)
		}
		// Blocks are the chronological order the client renders from: text,
		// thinking, tool_use, then text again as a NEW block (the tool broke
		// the run).
		if len(msg.Blocks) != 4 {
			kinds := make([]string, 0, len(msg.Blocks))
			for _, b := range msg.Blocks {
				kinds = append(kinds, string(b.Type))
			}
			t.Errorf("blocks = %v, want 4 (text, thinking, tool_use, text)", kinds)
		}
		// Two text deltas were counted; the thinking delta counts too.
		if snap.ChunkSeq == 0 {
			t.Error("chunk seq = 0, want the delta count so a client can drop folded-in chunks")
		}
	})

	t.Run("slices are copied, not aliased", func(t *testing.T) {
		// The caller reads the snapshot off the SSE goroutine while the
		// dispatch loop keeps appending. Handing out the live backing array
		// would be a data race the -race gate cannot always catch.
		buf := Buffer{MessageID: "m1"}
		buf.AppendTextDelta("hi", "")
		buf.AppendToolCall(&vibekit.ToolCall{ID: "tool-1"})

		snap, ok := buf.SnapshotCapped(SnapshotCaps{})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		msg := snap.Message
		// Mutate IN PLACE first. Appending would reallocate (len == cap here)
		// and leave the aliased array untouched, so an append-then-check makes
		// this test vacuous — it passed against a deliberately aliasing
		// implementation before this was fixed.
		buf.ToolCalls[0].Title = "mutated"
		if msg.ToolCalls[0].Title == "mutated" {
			t.Error("snapshot aliases the buffer's tool-call array")
		}
		buf.Blocks[0].Text = "mutated"
		if msg.Blocks[0].Text == "mutated" {
			t.Error("snapshot aliases the buffer's block array")
		}
	})
}

// The capped snapshot's fixture: a turn shaped like the real thing rather than
// hand-built, because the DUPLICATION is the point. Every Append*Delta writes
// both the flat builder AND a block, so a cap that reaches only one carrier
// halves the payload where it should divide it — and a hand-built Buffer would
// let that defect pass.
//
// Returns the buffer plus the per-dimension sizes a caller asserts against.
type capFixture struct {
	buf            *Buffer
	reasoningBytes int
	contentBytes   int
	blockTextBytes int
	blocks         int
	toolCalls      int
	toolOutputEach int
}

func newCapFixture(tb testing.TB) capFixture {
	tb.Helper()
	// Sized so the marshaled turn clears 4 MiB, which is what makes the
	// one-implementation guard below a real comparison rather than one over a
	// toy: 16 x chunk of text (each stream's delta lands in the flat builder AND
	// in its own block) plus 20 x toolOutput.
	const (
		chunk      = 128 << 10
		streams    = 4
		toolCalls  = 20
		toolOutput = 128 << 10
	)
	buf := New()
	buf.StartTurn("m1")
	// Distinct subtask ids per stream so each delta opens its OWN block: a
	// same-subtask thinking delta extends the newest thinking block, which would
	// leave one block holding everything and make the block-count cap untestable.
	for i := range streams {
		sub := "sub-" + strconv.Itoa(i)
		buf.AppendThinkingDelta(strings.Repeat("r", chunk), sub)
		buf.AppendTextDelta(strings.Repeat("c", chunk), sub)
	}
	for i := range toolCalls {
		buf.AppendToolCall(&vibekit.ToolCall{
			ID:     "tool-" + strconv.Itoa(i),
			Title:  "Run Command",
			Output: strings.Repeat("o", toolOutput),
		})
	}
	return capFixture{
		buf:            buf,
		reasoningBytes: chunk * streams,
		contentBytes:   chunk * streams,
		blockTextBytes: chunk * streams * 2,
		blocks:         streams * 2,
		toolCalls:      toolCalls,
		toolOutputEach: toolOutput,
	}
}

// blockTextLen is the dimension BlockTextBytes bounds: Text+Thinking summed over
// every block, which is where the second copy of the turn's text lives.
func blockTextLen(blocks []vibekit.Block) int {
	n := 0
	for _, b := range blocks {
		n += len(b.Text) + len(b.Thinking)
	}
	return n
}

// TestSnapshotCapped_UnboundedCutsNothing pins the uncut path over the 4 MiB fixture:
// SnapshotCaps{} is the unbounded read, so it withholds nothing, carries every block, and
// reports a base of 0.
//
// The BlockBase leg is a REGRESSION PIN rather than fail-first evidence, and the exemption
// is stated because no implementation of the field can make it red — 0 is both the correct
// answer on the uncut path and the answer a stub gives. What it DOES defend is a keepFrom
// hoisted to function scope: SnapshotCaps{} never enters the text-cap block, so a base read
// from a keepFrom initialised to len(out) outside it answers 4,000-odd rather than 0.
//
// The >= 4 MiB marshal assertion stays, because it is what keeps this case running over a
// real turn rather than a toy. What went with the rename is the SECOND call and the byte
// comparison against it: the value was compared to itself, while the name, the doc comment
// and three failure strings all referenced a Buffer.Snapshot method that has never existed
// and would now read as a claim about the Snapshot struct.
func TestSnapshotCapped_UnboundedCutsNothing(t *testing.T) {
	fx := newCapFixture(t)

	snap, ok := fx.buf.SnapshotCapped(SnapshotCaps{})
	if !ok {
		t.Fatal("snapshot reported no content over the 4 MiB fixture")
	}
	if snap.Truncated {
		t.Error("SnapshotCaps{} reported truncated; a zero in every field means unbounded")
	}
	if snap.BlockBase != 0 {
		t.Errorf("BlockBase = %d, want 0: an unbounded read cuts no block, so the window starts "+
			"where the array does", snap.BlockBase)
	}
	if got, want := len(snap.Message.Blocks), len(fx.buf.Blocks); got != want {
		t.Errorf("carried %d blocks, want all %d", got, want)
	}
	raw, err := json.Marshal(snap.Message)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if len(raw) < 4<<20 {
		t.Errorf("fixture marshaled to %d bytes, want at least 4 MiB; the case has to run over a "+
			"real turn rather than a toy", len(raw))
	}
}

func TestSnapshotCapped_KeepsTheTailAndMarksTruncated(t *testing.T) {
	buf := New()
	buf.StartTurn("m1")
	buf.AppendThinkingDelta("OLD-reasoning"+strings.Repeat("r", 4096)+"NEW-reasoning", "")
	buf.AppendTextDelta("OLD-content"+strings.Repeat("c", 4096)+"NEW-content", "")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t1", Output: "OLD-out" + strings.Repeat("o", 4096) + "NEW-out"})

	snap, ok := buf.SnapshotCapped(SnapshotCaps{
		ReasoningBytes:  64,
		ContentBytes:    64,
		BlockTextBytes:  128,
		ToolCalls:       4,
		ToolOutputBytes: 64,
		Blocks:          8,
	})
	if !ok {
		t.Fatal("snapshot reported no content")
	}
	msg := snap.Message
	if !snap.Truncated {
		t.Error("truncated = false after cutting reasoning, content, blocks and tool output")
	}
	// The TAIL, because a mid-turn reconnect wants the reply being written now.
	if !strings.HasSuffix(msg.Reasoning, "NEW-reasoning") || strings.Contains(msg.Reasoning, "OLD-reasoning") {
		t.Errorf("reasoning kept the wrong end: %q…%q", msg.Reasoning[:8], msg.Reasoning[len(msg.Reasoning)-16:])
	}
	if !strings.HasSuffix(msg.Content, "NEW-content") || strings.Contains(msg.Content, "OLD-content") {
		t.Errorf("content kept the wrong end: %q…%q", msg.Content[:8], msg.Content[len(msg.Content)-16:])
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(msg.ToolCalls))
	}
	if !strings.HasSuffix(msg.ToolCalls[0].Output, "NEW-out") || strings.Contains(msg.ToolCalls[0].Output, "OLD-out") {
		t.Errorf("tool output kept the wrong end: %q", msg.ToolCalls[0].Output)
	}
	// BOTH carriers, which is the whole reason BlockTextBytes exists: the text is
	// stored twice and a cap reaching one halves the payload instead of dividing it.
	if got := blockTextLen(msg.Blocks); got > 128 {
		t.Errorf("block text = %d bytes, want <= 128; the block copy of the turn is uncapped", got)
	}
	for _, b := range msg.Blocks {
		if strings.Contains(b.Text, "OLD-content") || strings.Contains(b.Thinking, "OLD-reasoning") {
			t.Errorf("a block kept its HEAD: %+v", b)
		}
	}
}

func TestSnapshotCapped_ASmallTurnIsNotMarkedTruncated(t *testing.T) {
	buf := New()
	buf.StartTurn("m1")
	buf.AppendThinkingDelta("pondering", "")
	buf.AppendTextDelta("hello world", "")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t1", Output: "ok"})

	snap, ok := buf.SnapshotCapped(connectCapsForTest())
	if !ok {
		t.Fatal("snapshot reported no content")
	}
	msg := snap.Message
	if snap.Truncated {
		t.Error("truncated = true for a turn well inside every cap; the client would show a note for nothing")
	}
	if msg.Content != "hello world" || msg.Reasoning != "pondering" {
		t.Errorf("content/reasoning = %q / %q, want them untouched", msg.Content, msg.Reasoning)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Output != "ok" {
		t.Errorf("tool calls = %+v, want one uncut call", msg.ToolCalls)
	}
}

// textCeiling is the worst-case TEXT a caps value admits: the two flat fields, the block
// array's share, and every carried tool call's output (bounded by the aggregate when one
// is set). It is the arithmetic the marshaled-size assertion below reads.
func textCeiling(c SnapshotCaps) int {
	tools := c.ToolCalls * c.ToolOutputBytes
	if c.ToolOutputTotalBytes > 0 {
		tools = min(tools, c.ToolOutputTotalBytes)
	}
	return c.ReasoningBytes + c.ContentBytes + c.BlockTextBytes + tools
}

// connectCapsForTest is a SMALL caps value, sized so the fixtures above cut on every
// dimension and textCeiling stays a hand-checkable 52 KiB. It is this file's own: the
// production caps (internal/agent's liveTurnGETCaps) are sized in the megabytes so an
// ordinary turn is never cut, which is exactly what a truncation test cannot use.
func connectCapsForTest() SnapshotCaps {
	return SnapshotCaps{
		ReasoningBytes:  4 << 10,
		ContentBytes:    16 << 10,
		BlockTextBytes:  16 << 10,
		ToolCalls:       8,
		ToolOutputBytes: 2 << 10,
		Blocks:          64,
	}
}

// TestSnapshotCapped_HonoursEveryCapDimension sets ONE dimension per row and
// asserts the other five are untouched. A cap that quietly reached a sibling
// dimension would otherwise pass every whole-payload assertion.
func TestSnapshotCapped_HonoursEveryCapDimension(t *testing.T) {
	tests := []struct {
		name  string
		caps  SnapshotCaps
		check func(t *testing.T, fx capFixture, msg vibekit.Message)
	}{
		{
			name: "reasoning bytes",
			caps: SnapshotCaps{ReasoningBytes: 512},
			check: func(t *testing.T, fx capFixture, msg vibekit.Message) {
				if len(msg.Reasoning) > 512 {
					t.Errorf("reasoning = %d bytes, want <= 512", len(msg.Reasoning))
				}
				if len(msg.Content) != fx.contentBytes {
					t.Errorf("content = %d bytes, want the full %d", len(msg.Content), fx.contentBytes)
				}
				if got := blockTextLen(msg.Blocks); got != fx.blockTextBytes {
					t.Errorf("block text = %d bytes, want the full %d", got, fx.blockTextBytes)
				}
			},
		},
		{
			name: "content bytes",
			caps: SnapshotCaps{ContentBytes: 512},
			check: func(t *testing.T, fx capFixture, msg vibekit.Message) {
				if len(msg.Content) > 512 {
					t.Errorf("content = %d bytes, want <= 512", len(msg.Content))
				}
				if len(msg.Reasoning) != fx.reasoningBytes {
					t.Errorf("reasoning = %d bytes, want the full %d", len(msg.Reasoning), fx.reasoningBytes)
				}
				if got := blockTextLen(msg.Blocks); got != fx.blockTextBytes {
					t.Errorf("block text = %d bytes, want the full %d", got, fx.blockTextBytes)
				}
			},
		},
		{
			name: "block text bytes",
			caps: SnapshotCaps{BlockTextBytes: 512},
			check: func(t *testing.T, fx capFixture, msg vibekit.Message) {
				if got := blockTextLen(msg.Blocks); got > 512 {
					t.Errorf("block text = %d bytes, want <= 512", got)
				}
				if len(msg.Content) != fx.contentBytes || len(msg.Reasoning) != fx.reasoningBytes {
					t.Errorf("flat fields = %d / %d bytes, want the full %d / %d",
						len(msg.Content), len(msg.Reasoning), fx.contentBytes, fx.reasoningBytes)
				}
				if len(msg.ToolCalls) != fx.toolCalls {
					t.Errorf("tool calls = %d, want the full %d", len(msg.ToolCalls), fx.toolCalls)
				}
			},
		},
		{
			name: "block count",
			caps: SnapshotCaps{Blocks: 3},
			check: func(t *testing.T, fx capFixture, msg vibekit.Message) {
				if len(msg.Blocks) != 3 {
					t.Errorf("blocks = %d, want 3", len(msg.Blocks))
				}
				// The NEWEST blocks, so the tail of the turn survives.
				if got := msg.Blocks[len(msg.Blocks)-1]; got.Text == "" {
					t.Errorf("last block = %+v, want the fixture's newest (a text block)", got)
				}
				if len(msg.Content) != fx.contentBytes || len(msg.Reasoning) != fx.reasoningBytes {
					t.Errorf("flat fields = %d / %d bytes, want them untouched",
						len(msg.Content), len(msg.Reasoning))
				}
			},
		},
		{
			name: "tool call count",
			caps: SnapshotCaps{ToolCalls: 5},
			check: func(t *testing.T, fx capFixture, msg vibekit.Message) {
				if len(msg.ToolCalls) != 5 {
					t.Fatalf("tool calls = %d, want 5", len(msg.ToolCalls))
				}
				if got := msg.ToolCalls[len(msg.ToolCalls)-1].ID; got != "tool-19" {
					t.Errorf("newest carried call = %q, want tool-19; the cap kept the wrong end", got)
				}
				if got := len(msg.ToolCalls[0].Output); got != fx.toolOutputEach {
					t.Errorf("tool output = %d bytes, want the full %d", got, fx.toolOutputEach)
				}
				if got := blockTextLen(msg.Blocks); got != fx.blockTextBytes {
					t.Errorf("block text = %d bytes, want the full %d", got, fx.blockTextBytes)
				}
			},
		},
		{
			name: "tool output bytes",
			caps: SnapshotCaps{ToolOutputBytes: 256},
			check: func(t *testing.T, fx capFixture, msg vibekit.Message) {
				if len(msg.ToolCalls) != fx.toolCalls {
					t.Fatalf("tool calls = %d, want the full %d", len(msg.ToolCalls), fx.toolCalls)
				}
				for _, tc := range msg.ToolCalls {
					if len(tc.Output) > 256 {
						t.Errorf("%s output = %d bytes, want <= 256", tc.ID, len(tc.Output))
					}
				}
				if len(msg.Content) != fx.contentBytes || len(msg.Reasoning) != fx.reasoningBytes {
					t.Errorf("flat fields = %d / %d bytes, want them untouched",
						len(msg.Content), len(msg.Reasoning))
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCapFixture(t)
			snap, ok := fx.buf.SnapshotCapped(tc.caps)
			if !ok {
				t.Fatal("snapshot reported no content")
			}
			if !snap.Truncated {
				t.Errorf("SnapshotCapped(%+v) reported truncated = false over a 4 MiB turn", tc.caps)
			}
			tc.check(t, fx, snap.Message)
		})
	}
}

// TestSnapshotCapped_CutsOnARuneBoundary is the reason tailBytes advances to the
// next utf8.RuneStart. A byte-boundary cut leaves an invalid leading fragment,
// and encoding/json substitutes U+FFFD for it on marshal — so the client renders
// a replacement character at the top of every capped field.
func TestSnapshotCapped_CutsOnARuneBoundary(t *testing.T) {
	// A 3-byte rune, so a cap that is not a multiple of 3 lands mid-rune.
	const glyph = "日"
	buf := New()
	buf.StartTurn("m1")
	buf.AppendThinkingDelta(strings.Repeat(glyph, 400), "")
	buf.AppendTextDelta(strings.Repeat(glyph, 400), "")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t1", Output: strings.Repeat(glyph, 400)})

	for _, n := range []int{100, 101, 102} {
		t.Run("cap "+strconv.Itoa(n), func(t *testing.T) {
			snap, ok := buf.SnapshotCapped(SnapshotCaps{
				ReasoningBytes:  n,
				ContentBytes:    n,
				BlockTextBytes:  n,
				ToolCalls:       4,
				ToolOutputBytes: n,
				Blocks:          64,
			})
			if !ok || !snap.Truncated {
				t.Fatalf("ok = %v, truncated = %v, want true / true", ok, snap.Truncated)
			}
			msg := snap.Message
			for name, s := range map[string]string{"reasoning": msg.Reasoning, "content": msg.Content} {
				if !utf8.ValidString(s) {
					t.Errorf("%s is not valid UTF-8 after a %d-byte cap: %q", name, n, s)
				}
				if len(s) > n {
					t.Errorf("%s = %d bytes, want <= %d", name, len(s), n)
				}
			}
			for _, b := range msg.Blocks {
				if !utf8.ValidString(b.Text) || !utf8.ValidString(b.Thinking) {
					t.Errorf("a block is not valid UTF-8 after a %d-byte cap: %+v", n, b)
				}
			}
			for _, tc := range msg.ToolCalls {
				if !utf8.ValidString(tc.Output) {
					t.Errorf("%s output is not valid UTF-8 after a %d-byte cap", tc.ID, n)
				}
			}
			// The marshal is the failure this guards: a split rune survives the
			// ValidString checks above only if they are wrong, so assert the
			// round trip too.
			raw, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.ContainsRune(raw, utf8.RuneError) {
				t.Errorf("marshaled snapshot carries U+FFFD, so a cap split a rune at n=%d", n)
			}
		})
	}
}

// TestSnapshotCapped_MarshalsInsideTheTextCeiling is the arithmetic a byte ceiling over
// a capped snapshot depends on: a maximally-full capped snapshot's REAL marshaled length
// has to sit inside the caps' text ceiling plus an envelope, or a ceiling stated from the
// caps (internal/agent's liveTurnGETCaps publishes one) under-counts.
func TestSnapshotCapped_MarshalsInsideTheTextCeiling(t *testing.T) {
	caps := connectCapsForTest()
	if got, want := textCeiling(caps), 52<<10; got != want {
		t.Errorf("textCeiling() = %d, want %d; the test caps and the ceiling arithmetic disagree", got, want)
	}

	fx := newCapFixture(t)
	snap, ok := fx.buf.SnapshotCapped(caps)
	if !ok || !snap.Truncated {
		t.Fatalf("ok = %v, truncated = %v, want true / true", ok, snap.Truncated)
	}
	raw, err := json.Marshal(snap.Message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The envelope allowance, stated: JSON structure around the text — field
	// names, quotes, braces, the per-tool-call metadata (id, title, kind,
	// status, ts) and the per-block type/subtask fields. 8 KiB is generous for
	// 8 tool calls and 64 blocks and is what a ceiling adds beside the text.
	const envelopeAllowance = 8 << 10
	if limit := textCeiling(caps) + envelopeAllowance; len(raw) > limit {
		t.Errorf("capped snapshot marshaled to %d bytes, want <= %d (text ceiling %d + %d envelope); "+
			"a ceiling built on the caps would under-count",
			len(raw), limit, textCeiling(caps), envelopeAllowance)
	}
}

// toolOutputLen is the dimension ToolOutputTotalBytes bounds: Output summed over every
// carried call, which is the product the per-call cap alone cannot bound.
func toolOutputLen(calls []vibekit.ToolCall) int {
	n := 0
	for _, c := range calls {
		n += len(c.Output)
	}
	return n
}

// TestSnapshotCaps_ToolOutputTotalBytes pins the aggregate dimension. It exists because a
// per-call cap times a call count is an arithmetic ceiling nobody would accept as a bound
// (64 KiB x 4096 = 268 MiB), so a caller that wants the terminal ring buffer's own 64 KiB
// per-call tail AND a statable total needs this field to be the thing that states it.
func TestSnapshotCaps_ToolOutputTotalBytes(t *testing.T) {
	t.Run("the aggregate cuts newest-first with the boundary call tail-capped", func(t *testing.T) {
		fx := newCapFixture(t)
		// 300 KiB over 128 KiB calls: two survive whole, the third keeps a 44 KiB tail,
		// everything older is dropped. Every OTHER dimension is left unbounded so a
		// failure here can only be this one.
		const total = 300 << 10
		snap, ok := fx.buf.SnapshotCapped(SnapshotCaps{ToolOutputTotalBytes: total})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		msg := snap.Message
		if !snap.Truncated {
			t.Error("truncated = false, want true: the aggregate dropped calls, and a client " +
				"reading a bounded payload as a complete one is what the flag exists to prevent")
		}
		if got := toolOutputLen(msg.ToolCalls); got > total {
			t.Errorf("carried output = %d bytes, want <= %d", got, total)
		}
		if got, want := len(msg.ToolCalls), 3; got != want {
			t.Fatalf("carried %d tool calls, want %d (two whole plus the tail-capped boundary)", got, want)
		}
		// NEWEST kept: the fixture numbers its calls in order, so the survivors are the
		// last three. A drop from the wrong end would carry tool-0 instead.
		wantIDs := []string{"tool-17", "tool-18", "tool-19"}
		for i, want := range wantIDs {
			if got := msg.ToolCalls[i].ID; got != want {
				t.Errorf("tool call %d = %q, want %q: the aggregate must keep the NEWEST", i, got, want)
			}
		}
		// The boundary call is the OLDEST survivor, cut to the remainder; the two after
		// it are whole.
		if got, want := len(msg.ToolCalls[0].Output), total-2*fx.toolOutputEach; got != want {
			t.Errorf("boundary call output = %d bytes, want %d (the remaining budget)", got, want)
		}
		for _, c := range msg.ToolCalls[1:] {
			if got := len(c.Output); got != fx.toolOutputEach {
				t.Errorf("%s output = %d bytes, want the full %d", c.ID, got, fx.toolOutputEach)
			}
		}
	})

	t.Run("an aggregate the sum fits under cuts nothing", func(t *testing.T) {
		fx := newCapFixture(t)
		snap, ok := fx.buf.SnapshotCapped(SnapshotCaps{ToolOutputTotalBytes: 4 << 20})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		msg := snap.Message
		if snap.Truncated {
			t.Error("truncated = true, want false: the whole payload fit, so nothing was withheld")
		}
		if got, want := len(msg.ToolCalls), fx.toolCalls; got != want {
			t.Errorf("carried %d tool calls, want all %d", got, want)
		}
		if got, want := toolOutputLen(msg.ToolCalls), fx.toolCalls*fx.toolOutputEach; got != want {
			t.Errorf("carried output = %d bytes, want the full %d", got, want)
		}
	})

	t.Run("a zero aggregate is UNBOUNDED, not cut-everything", func(t *testing.T) {
		// The trap this pins: every other dimension in SnapshotCaps reads zero as
		// unbounded, so an aggregate that read it as a zero BUDGET would silently drop
		// every tool call for any caps value leaving it unset, SnapshotCaps{} included.
		fx := newCapFixture(t)
		snap, ok := fx.buf.SnapshotCapped(SnapshotCaps{})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		msg := snap.Message
		if snap.Truncated {
			t.Error("truncated = true, want false: SnapshotCaps{} is the unbounded snapshot")
		}
		if got, want := len(msg.ToolCalls), fx.toolCalls; got != want {
			t.Errorf("carried %d tool calls, want all %d: a zero aggregate must not cut", got, want)
		}
	})
}

// baseFixtureBlocks is how many blocks newBaseFixture builds, and the two per-block
// sizes. Small on purpose: the property under test needs bytes in the tens, and
// newCapFixture's 4 MiB would buy nothing here.
const (
	baseFixtureBlocks   = 12
	baseFixtureThinking = 30
	baseFixtureText     = 20
)

// newBaseFixture builds a buffer of baseFixtureBlocks blocks and stamps a DISTINCT
// AgentSubtaskID on each one, which is what makes the identity assertion below
// meaningful.
//
// Its own fixture rather than newCapFixture, for two reasons. That one uses
// AgentSubtaskID to FORCE the block boundaries — four streams under one id apiece —
// so its ids are not distinct per block and identity there would be ambiguous. And
// the ids are overwritten AFTER the builder has split the blocks, so the overwrite
// cannot change the shape it is describing.
func newBaseFixture(tb testing.TB) *Buffer {
	tb.Helper()
	buf := New()
	buf.StartTurn("m1")
	for i := range baseFixtureBlocks / 2 {
		sub := "stream-" + strconv.Itoa(i)
		buf.AppendThinkingDelta(strings.Repeat("r", baseFixtureThinking), sub)
		buf.AppendTextDelta(strings.Repeat("c", baseFixtureText), sub)
	}
	if len(buf.Blocks) != baseFixtureBlocks {
		tb.Fatalf("fixture built %d blocks, want %d; the per-stream split changed and every "+
			"expected base below is derived from that shape", len(buf.Blocks), baseFixtureBlocks)
	}
	for i := range buf.Blocks {
		buf.Blocks[i].AgentSubtaskID = "blk-" + strconv.Itoa(i)
	}
	return buf
}

// TestSnapshotCapped_ReportsTheBlockBaseItDropped is the field's reason for existing:
// both block caps keep the TAIL of the array and re-index it from zero, while a live
// message_chunk keeps naming the ABSOLUTE index, so a client that adopted a capped
// snapshot addressed every later chunk against the wrong array.
//
// The assertion is a THREE-PART property against the buffer's OWN array plus the
// base's value, never byte-equality on Blocks[0] and never a second copy of the cap
// arithmetic: the text cap REWRITES the boundary block in place, so the plain oracle
// is false rather than merely fragile, and a base recomputed from the caps would
// agree with a wrong implementation.
func TestSnapshotCapped_ReportsTheBlockBaseItDropped(t *testing.T) {
	tests := []struct {
		name     string
		caps     SnapshotCaps
		wantBase int
	}{
		{
			// 125 bytes admits blocks 11..7 whole (100 bytes) and leaves 5 for block 6,
			// which is KEPT tail-truncated because the remainder is non-zero.
			name:     "the text cap alone cuts",
			caps:     SnapshotCaps{BlockTextBytes: 125},
			wantBase: 6,
		},
		{
			// The newest 4 of 12.
			name:     "the count cap alone cuts",
			caps:     SnapshotCaps{Blocks: 4},
			wantBase: 8,
		},
		{
			// The two dimensions SUM: the text cap leaves 6 blocks starting at 6, then
			// the count cap drops 3 more off the front.
			name:     "both cut, and the base is their sum",
			caps:     SnapshotCaps{BlockTextBytes: 125, Blocks: 3},
			wantBase: 9,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := newBaseFixture(t)
			snap, ok := buf.SnapshotCapped(tc.caps)
			if !ok {
				t.Fatal("snapshot reported no content")
			}
			if !snap.Truncated {
				t.Errorf("truncated = false while blocks were dropped; a reader takes the window "+
					"for the whole turn (caps %+v)", tc.caps)
			}
			if got := snap.BlockBase; got != tc.wantBase {
				t.Fatalf("BlockBase = %d, want %d: the base names the position in the buffer's own "+
					"array that Blocks[0] came from, so a wrong one puts every rebased chunk at the "+
					"wrong index", got, tc.wantBase)
			}
			got := snap.Message.Blocks
			if len(got) == 0 {
				t.Fatal("the snapshot carried no blocks, so nothing below is measuring the base")
			}
			base := snap.BlockBase
			// IDENTITY: the base names a real position, checked against a field the caps
			// cannot touch (capBlocks writes only Text and Thinking).
			if got, want := got[0].AgentSubtaskID, buf.Blocks[base].AgentSubtaskID; got != want {
				t.Errorf("Blocks[0].AgentSubtaskID = %q, want %q (buf.Blocks[%d]): the base does not "+
					"name the block the window starts at", got, want, base)
			}
			// The BOUNDARY block is a TAIL of the buffer's own. Both fields, because
			// tailBytes treats a spent budget as UNBOUNDED, so a block whose budget went
			// on Text keeps its Thinking verbatim — a suffix holds either way where an
			// equality would not.
			if !strings.HasSuffix(buf.Blocks[base].Text, got[0].Text) {
				t.Errorf("Blocks[0].Text is not a tail of buf.Blocks[%d].Text (%d vs %d bytes)",
					base, len(got[0].Text), len(buf.Blocks[base].Text))
			}
			if !strings.HasSuffix(buf.Blocks[base].Thinking, got[0].Thinking) {
				t.Errorf("Blocks[0].Thinking is not a tail of buf.Blocks[%d].Thinking (%d vs %d bytes)",
					base, len(got[0].Thinking), len(buf.Blocks[base].Thinking))
			}
			// The REMAINDER is untouched, so the window really is a contiguous slice of
			// the array starting at the base.
			if want := buf.Blocks[base+1 : base+len(got)]; !slices.Equal(got[1:], want) {
				t.Errorf("blocks after the boundary = %+v, want buf.Blocks[%d:%d] = %+v",
					got[1:], base+1, base+len(got), want)
			}
		})
	}
}
