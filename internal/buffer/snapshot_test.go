package buffer

import (
	"bytes"
	"encoding/json"
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
// from it. These cases pin that the buffer's own snapshot carries what the
// connect-time turn_state needs.
func TestBufferSnapshot(t *testing.T) {
	t.Run("an unstarted turn has no snapshot", func(t *testing.T) {
		var buf Buffer
		if _, _, _, ok := buf.SnapshotCapped(SnapshotCaps{}); ok {
			t.Error("snapshot reported content for a buffer with no message id")
		}
	})

	t.Run("a started but silent turn is a bare busy signal", func(t *testing.T) {
		buf := Buffer{MessageID: "m1"}
		msg, seq, _, ok := buf.SnapshotCapped(SnapshotCaps{})
		if ok {
			t.Errorf("snapshot reported content for an empty turn: %+v", msg)
		}
		if seq != 0 {
			t.Errorf("chunk seq = %d, want 0", seq)
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

		msg, seq, _, ok := buf.SnapshotCapped(SnapshotCaps{})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
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
		if seq == 0 {
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

		msg, _, _, ok := buf.SnapshotCapped(SnapshotCaps{})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
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

// TestSnapshotCapped_UnboundedMatchesSnapshot is the ONE-implementation guard.
// Snapshot delegates to SnapshotCapped(SnapshotCaps{}), so the two can only
// diverge by someone reintroducing a second read — and the comparison is on
// MARSHALED BYTES rather than field by field, so a field added to one path and
// not the other fails here instead of shipping.
//
// Ts is normalized because it is stamped from time.Now() per call and the two
// calls can straddle a millisecond; every other field is compared verbatim.
func TestSnapshotCapped_UnboundedMatchesSnapshot(t *testing.T) {
	fx := newCapFixture(t)

	plain, plainSeq, _, plainOK := fx.buf.SnapshotCapped(SnapshotCaps{})
	capped, cappedSeq, truncated, cappedOK := fx.buf.SnapshotCapped(SnapshotCaps{})
	if !plainOK || !cappedOK {
		t.Fatalf("ok = %v / %v, want both true", plainOK, cappedOK)
	}
	if truncated {
		t.Error("SnapshotCaps{} reported truncated; a zero in every field means unbounded")
	}
	if plainSeq != cappedSeq {
		t.Errorf("chunk seq = %d (Snapshot) vs %d (SnapshotCapped), want equal", plainSeq, cappedSeq)
	}
	plain.Ts, capped.Ts = 0, 0
	wantJSON, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal Snapshot: %v", err)
	}
	gotJSON, err := json.Marshal(capped)
	if err != nil {
		t.Fatalf("marshal SnapshotCapped: %v", err)
	}
	if len(wantJSON) < 4<<20 {
		t.Errorf("fixture marshaled to %d bytes, want at least 4 MiB; the guard has to run over a real turn", len(wantJSON))
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("SnapshotCapped(SnapshotCaps{}) is not byte-identical to Snapshot (%d vs %d bytes); "+
			"there are two implementations of the read again", len(gotJSON), len(wantJSON))
	}
}

func TestSnapshotCapped_KeepsTheTailAndMarksTruncated(t *testing.T) {
	buf := New()
	buf.StartTurn("m1")
	buf.AppendThinkingDelta("OLD-reasoning"+strings.Repeat("r", 4096)+"NEW-reasoning", "")
	buf.AppendTextDelta("OLD-content"+strings.Repeat("c", 4096)+"NEW-content", "")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t1", Output: "OLD-out" + strings.Repeat("o", 4096) + "NEW-out"})

	msg, _, truncated, ok := buf.SnapshotCapped(SnapshotCaps{
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
	if !truncated {
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

	msg, _, truncated, ok := buf.SnapshotCapped(connectCapsForTest())
	if !ok {
		t.Fatal("snapshot reported no content")
	}
	if truncated {
		t.Error("truncated = true for a turn well inside every cap; the client would show a note for nothing")
	}
	if msg.Content != "hello world" || msg.Reasoning != "pondering" {
		t.Errorf("content/reasoning = %q / %q, want them untouched", msg.Content, msg.Reasoning)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Output != "ok" {
		t.Errorf("tool calls = %+v, want one uncut call", msg.ToolCalls)
	}
}

// connectCapsForTest mirrors internal/agent's connectSnapshotCaps. A copy rather
// than an import because internal/agent imports THIS package, so reading the real
// value here would be a cycle; the numbers are policy the production path owns.
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
			msg, _, truncated, ok := fx.buf.SnapshotCapped(tc.caps)
			if !ok {
				t.Fatal("snapshot reported no content")
			}
			if !truncated {
				t.Errorf("SnapshotCapped(%+v) reported truncated = false over a 4 MiB turn", tc.caps)
			}
			tc.check(t, fx, msg)
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
			msg, _, truncated, ok := buf.SnapshotCapped(SnapshotCaps{
				ReasoningBytes:  n,
				ContentBytes:    n,
				BlockTextBytes:  n,
				ToolCalls:       4,
				ToolOutputBytes: n,
				Blocks:          64,
			})
			if !ok || !truncated {
				t.Fatalf("ok = %v, truncated = %v, want true / true", ok, truncated)
			}
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

// TestSnapshotCaps_MaxTextBytesMatchesTheWorstCasePayload is the arithmetic
// the connect replay's per-connect budget depends on: a maximally-full capped snapshot's
// REAL marshaled length has to sit inside MaxTextBytes plus an envelope, or a
// budget that subtracts MaxTextBytes per snapshot under-counts and the cold
// connect exceeds its own gate.
func TestSnapshotCaps_MaxTextBytesMatchesTheWorstCasePayload(t *testing.T) {
	caps := connectCapsForTest()
	if got, want := caps.MaxTextBytes(), 52<<10; got != want {
		t.Errorf("MaxTextBytes() = %d, want %d; the connect caps and the budget arithmetic disagree", got, want)
	}
	if got := (SnapshotCaps{ReasoningBytes: 1}).MaxTextBytes(); got != 0 {
		t.Errorf("MaxTextBytes() with unbounded dimensions = %d, want 0 (unbounded); a partial sum "+
			"reads as a real ceiling and understates the payload", got)
	}

	fx := newCapFixture(t)
	msg, _, truncated, ok := fx.buf.SnapshotCapped(caps)
	if !ok || !truncated {
		t.Fatalf("ok = %v, truncated = %v, want true / true", ok, truncated)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The envelope allowance, stated: JSON structure around the text — field
	// names, quotes, braces, the per-tool-call metadata (id, title, kind,
	// status, ts) and the per-block type/subtask fields. 8 KiB is generous for
	// 8 tool calls and 64 blocks and is what the budget subtracts alongside
	// MaxTextBytes.
	const envelopeAllowance = 8 << 10
	if limit := caps.MaxTextBytes() + envelopeAllowance; len(raw) > limit {
		t.Errorf("capped snapshot marshaled to %d bytes, want <= %d (MaxTextBytes %d + %d envelope); "+
			"a per-connect budget built on MaxTextBytes would under-count",
			len(raw), limit, caps.MaxTextBytes(), envelopeAllowance)
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
		msg, _, truncated, ok := fx.buf.SnapshotCapped(SnapshotCaps{ToolOutputTotalBytes: total})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		if !truncated {
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
		msg, _, truncated, ok := fx.buf.SnapshotCapped(SnapshotCaps{ToolOutputTotalBytes: 4 << 20})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		if truncated {
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
		// every tool call for connectSnapshotCaps and for SnapshotCaps{} alike.
		fx := newCapFixture(t)
		msg, _, truncated, ok := fx.buf.SnapshotCapped(SnapshotCaps{})
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		if truncated {
			t.Error("truncated = true, want false: SnapshotCaps{} is the unbounded snapshot")
		}
		if got, want := len(msg.ToolCalls), fx.toolCalls; got != want {
			t.Errorf("carried %d tool calls, want all %d: a zero aggregate must not cut", got, want)
		}
	})

	t.Run("MaxTextBytes takes the min of the product and the aggregate", func(t *testing.T) {
		base := SnapshotCaps{
			ReasoningBytes:  1000,
			ContentBytes:    2000,
			BlockTextBytes:  3000,
			ToolCalls:       10,
			ToolOutputBytes: 500,
			Blocks:          8,
		}
		// Product is 10 x 500 = 5000; the flat share is 6000.
		if got, want := base.MaxTextBytes(), 11000; got != want {
			t.Fatalf("MaxTextBytes() with no aggregate = %d, want %d (the product)", got, want)
		}
		tighter := base
		tighter.ToolOutputTotalBytes = 1500
		if got, want := tighter.MaxTextBytes(), 7500; got != want {
			t.Errorf("MaxTextBytes() with a smaller aggregate = %d, want %d: the aggregate is what "+
				"the payload can actually reach, so the product would overstate the ceiling", got, want)
		}
		looser := base
		looser.ToolOutputTotalBytes = 99000
		if got, want := looser.MaxTextBytes(), 11000; got != want {
			t.Errorf("MaxTextBytes() with a larger aggregate = %d, want %d: the product still binds", got, want)
		}
	})
}
