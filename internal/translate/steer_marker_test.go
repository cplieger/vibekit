package translate

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
)

const ack = "[STEERING steer-abc123: adjusted the approach]"

// feed streams s through the filter one rune-safe slice at a time using the
// given split points, returning everything emitted plus the final carry. This
// is the shape the real caller has: one filter call per chunk, carry threaded
// between them.
func feed(chunks []string) (emitted, carry string) {
	var out strings.Builder
	for _, c := range chunks {
		var emit string
		emit, carry, _ = stripSteerAcks(carry, c)
		out.WriteString(emit)
	}
	return out.String(), carry
}

func TestStripSteerAcks_RemovesAWholeMarker(t *testing.T) {
	got, carry := feed([]string{"Done. " + ack})
	if got != "Done. " {
		t.Errorf("emitted %q, want %q", got, "Done. ")
	}
	if carry != "" {
		t.Errorf("carry = %q, want empty", carry)
	}
}

// The marker arrives inside ordinary text deltas, so it can be cut at ANY byte.
// Every split has to produce the same answer or the filter is only accidentally
// correct — this is the case that makes the carry necessary at all.
func TestStripSteerAcks_SurvivesEverySplitPoint(t *testing.T) {
	full := "Here is the work. " + ack + " And a closing line."
	want := "Here is the work.  And a closing line."
	for i := 1; i < len(full); i++ {
		got, carry := feed([]string{full[:i], full[i:]})
		if carry != "" {
			t.Fatalf("split at %d: carry left over: %q", i, carry)
		}
		if got != want {
			t.Fatalf("split at %d: emitted %q, want %q", i, got, want)
		}
	}
}

// The pathological streaming case: one byte per chunk. If the carry ever
// releases early, the marker leaks a character at a time.
func TestStripSteerAcks_SurvivesByteAtATime(t *testing.T) {
	full := "ok " + ack + " done"
	chunks := make([]string, 0, len(full))
	for i := range len(full) {
		chunks = append(chunks, full[i:i+1])
	}
	got, carry := feed(chunks)
	if carry != "" {
		t.Errorf("carry = %q, want empty", carry)
	}
	if want := "ok  done"; got != want {
		t.Errorf("emitted %q, want %q", got, want)
	}
}

// Prose is allowed to open with the marker's own words. Holding this back until
// the carry bound released it would stall a real sentence for 8 KiB, so the
// filter must commit only once it sees `[STEERING steer-`.
func TestStripSteerAcks_DoesNotHoldProseHostage(t *testing.T) {
	cases := []string{
		"[STEERING is the name of the feature]",
		"see [docs](https://example.com) for more",
		"an array index like a[STEERING] is fine",
		"[STEER] is not it either",
	}
	for _, in := range cases {
		got, carry := feed([]string{in})
		if got != in || carry != "" {
			t.Errorf("%q: emitted %q carry %q, want the input emitted whole", in, got, carry)
		}
	}
}

// A chunk ending mid-candidate must withhold exactly the candidate and emit
// everything in front of it, so the visible text is never delayed as a whole.
func TestStripSteerAcks_WithholdsOnlyTheCandidate(t *testing.T) {
	tests := []struct {
		in        string
		wantEmit  string
		wantCarry string
	}{
		{in: "text so far [", wantEmit: "text so far ", wantCarry: "["},
		{in: "text [STE", wantEmit: "text ", wantCarry: "[STE"},
		{in: "text [STEERING steer-1: partial", wantEmit: "text ", wantCarry: "[STEERING steer-1: partial"},
		{in: "no candidate at all", wantEmit: "no candidate at all", wantCarry: ""},
	}
	for _, tt := range tests {
		emit, carry, _ := stripSteerAcks("", tt.in)
		if emit != tt.wantEmit || carry != tt.wantCarry {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", tt.in, emit, carry, tt.wantEmit, tt.wantCarry)
		}
	}
}

func TestStripSteerAcks_RemovesEveryMarkerInAChunk(t *testing.T) {
	in := "a " + ack + " b [STEERING steer-x: second] c"
	got, carry := feed([]string{in})
	if want := "a  b  c"; got != want || carry != "" {
		t.Errorf("emitted %q carry %q, want %q and no carry", got, carry, want)
	}
}

// Text in front of a complete marker is finished, so it is emitted rather than
// withheld. Two things go wrong when it is held instead, and the second is the
// serious one: the carry stops being a suffix of the input (found by
// FuzzStripSteerAcks_AccountsForEveryByte, seed f5c17fda28252ee5's sibling
// 04153f86c7909901), and a candidate held from before a removed marker splices
// onto the NEXT chunk across text that is already gone.
//
// The splice case is the second row. `[STEERING stee` is followed in the stream
// by a complete marker, so KAS's own reader sees it as prose; joining it to a
// later `r-9: y]` would strip it as an acknowledgement of a steer the agent
// never answered, with an id the model does not control either.
func TestStripSteerAcks_DoesNotHoldTextInFrontOfAMarker(t *testing.T) {
	tests := map[string]struct {
		chunks   []string
		wantEmit string
		wantIDs  []string
	}{
		"bracket before a marker": {
			chunks:   []string{"[[STEERING steer-0: 0]"},
			wantEmit: "[",
			wantIDs:  []string{"steer-0"},
		},
		"candidate before a marker cannot splice": {
			chunks:   []string{"[STEERING stee[STEERING steer-1: x]", "r-9: y]"},
			wantEmit: "[STEERING steer-9: y]",
			wantIDs:  []string{"steer-1"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			emit, carry, acks := feedAcks(tt.chunks)
			if emit != tt.wantEmit {
				t.Errorf("emitted %q, want %q", emit, tt.wantEmit)
			}
			if carry != "" {
				t.Errorf("carry = %q, want empty", carry)
			}
			var ids []string
			for _, a := range acks {
				ids = append(ids, a.SteerID)
			}
			if !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("ack ids = %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

// An unclosed marker cannot be allowed to swallow the rest of the turn. Past the
// bound the carry is released as ordinary text, which is the lesser wrong: some
// machinery shows, rather than the response disappearing.
func TestStripSteerAcks_ReleasesAnOverlongCandidate(t *testing.T) {
	open := "[STEERING steer-1: " + strings.Repeat("x", maxSteerCarry+1)
	emit, carry, _ := stripSteerAcks("", open)
	if carry != "" {
		t.Errorf("carry = %d bytes, want it released at the bound", len(carry))
	}
	if emit != open {
		t.Error("the over-bound candidate was not emitted; text would be lost")
	}
}

// FlushSteerCarry settles the turn. The two outcomes are both deliberate: an
// unclosed marker is machinery and goes away, a short bracket is prose and comes
// back.
func TestFlushSteerCarry_DropsMachineryAndKeepsProse(t *testing.T) {
	tests := []struct {
		name  string
		carry string
		want  string
	}{
		{name: "an unclosed marker is dropped", carry: "[STEERING steer-1: was cut off", want: ""},
		{name: "a bare bracket is prose", carry: "[", want: "["},
		{name: "a partial of the literal is prose", carry: "[STEER", want: "[STEER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &buffer.Buffer{}
			buf.SetSteerCarry(tt.carry, "")
			FlushSteerCarry(buf)

			if got := buf.Content.String(); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
			// The block array is a second, independent reader of the turn; text
			// released into one and not the other renders differently from what
			// gets persisted.
			var blocks strings.Builder
			for _, b := range buf.Blocks {
				blocks.WriteString(b.Text)
			}
			if got := blocks.String(); got != tt.want {
				t.Errorf("blocks = %q, want %q", got, tt.want)
			}
			if c, _ := buf.SteerCarry(); c != "" {
				t.Errorf("carry still %q after flush", c)
			}
		})
	}
}

func TestFlushSteerCarry_EmptyCarryTouchesNothing(t *testing.T) {
	buf := &buffer.Buffer{}
	buf.Content.WriteString("existing")
	FlushSteerCarry(buf)
	if got := buf.Content.String(); got != "existing" {
		t.Errorf("content = %q, want it untouched", got)
	}
	if len(buf.Blocks) != 0 {
		t.Errorf("flush of an empty carry created %d blocks", len(buf.Blocks))
	}
}

// --- The marker's CONTENT, which used to be thrown away ---

// feedAcks is feed's sibling for the extraction half: it accumulates the
// acknowledgements across chunks, which is what the live caller does one
// broadcast at a time.
func feedAcks(chunks []string) (emitted, carry string, acks []steerAck) {
	var out strings.Builder
	for _, c := range chunks {
		var emit string
		var got []steerAck
		emit, carry, got = stripSteerAcks(carry, c)
		out.WriteString(emit)
		acks = append(acks, got...)
	}
	return out.String(), carry, acks
}

func TestStripSteerAcks_LiftsTheIDAndTheAgentsStatement(t *testing.T) {
	_, _, acks := feedAcks([]string{"Done. " + ack})
	if len(acks) != 1 {
		t.Fatalf("got %d acks, want 1: %+v", len(acks), acks)
	}
	if acks[0].SteerID != "steer-abc123" {
		t.Errorf("SteerID = %q, want steer-abc123", acks[0].SteerID)
	}
	if acks[0].Text != "adjusted the approach" {
		t.Errorf("Text = %q, want the agent's own sentence", acks[0].Text)
	}
}

// One ack per marker, in order, so two steers answered in one response do not
// collapse onto one chip or land on each other's.
func TestStripSteerAcks_LiftsEveryMarkerInOrder(t *testing.T) {
	in := "a [STEERING steer-1: rebased onto main instead] b [STEERING steer-2: skipped the rename] c"
	emit, carry, acks := feedAcks([]string{in})
	if want := "a  b  c"; emit != want || carry != "" {
		t.Errorf("emitted %q carry %q, want %q and no carry", emit, carry, want)
	}
	if len(acks) != 2 {
		t.Fatalf("got %d acks, want 2: %+v", len(acks), acks)
	}
	if acks[0].SteerID != "steer-1" || acks[0].Text != "rebased onto main instead" {
		t.Errorf("first ack = %+v", acks[0])
	}
	if acks[1].SteerID != "steer-2" || acks[1].Text != "skipped the rename" {
		t.Errorf("second ack = %+v", acks[1])
	}
}

// The marker can be cut at any byte, so the ack must be reported exactly once
// and only when the marker CLOSES. Reporting on a partial would put a truncated
// sentence on the chip; reporting twice would fire the broadcast twice.
func TestStripSteerAcks_ReportsAnAckOnceAcrossEverySplit(t *testing.T) {
	full := "Here is the work. " + ack + " And a closing line."
	for i := 1; i < len(full); i++ {
		_, carry, acks := feedAcks([]string{full[:i], full[i:]})
		if carry != "" {
			t.Fatalf("split at %d: carry left over: %q", i, carry)
		}
		if len(acks) != 1 {
			t.Fatalf("split at %d: got %d acks, want exactly 1", i, len(acks))
		}
		if acks[0].SteerID != "steer-abc123" || acks[0].Text != "adjusted the approach" {
			t.Fatalf("split at %d: ack = %+v", i, acks[0])
		}
	}
}

// A marker the model opens and never closes yields NO ack. The over-bound
// release emits the text as prose, and inventing a verdict from a half-written
// sentence would be worse than showing nothing.
func TestStripSteerAcks_UnclosedMarkerYieldsNoAck(t *testing.T) {
	cases := map[string][]string{
		"still open":     {"[STEERING steer-1: I was about to"},
		"over bound":     {"[STEERING steer-1: " + strings.Repeat("x", maxSteerCarry+1)},
		"never a marker": {"[STEERING is the feature name]"},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, acks := feedAcks(chunks); len(acks) != 0 {
				t.Errorf("got %d acks, want none: %+v", len(acks), acks)
			}
		})
	}
}

// The body is rendered as a label, and KAS's own shape leaves it with the
// separator's space in front. Trimming here rather than at the renderer keeps
// every consumer honest about what the agent said.
func TestStripSteerAcks_TrimsTheAgentsStatement(t *testing.T) {
	_, _, acks := feedAcks([]string{"[STEERING steer-1:   \n padded answer \t ]"})
	if len(acks) != 1 {
		t.Fatalf("got %d acks, want 1", len(acks))
	}
	if acks[0].Text != "padded answer" {
		t.Errorf("Text = %q, want it trimmed", acks[0].Text)
	}
}

// The invariant the original doc asserts and a third return value must not
// break: the emitted text plus the carry account for every byte that was not
// part of a matched marker.
func FuzzStripSteerAcks_AccountsForEveryByte(f *testing.F) {
	f.Add("Done. " + ack)
	f.Add("[STEERING steer-1: a][STEERING steer-2: b]")
	f.Add("prose [STEERING with no id]")
	f.Add("[STEERING steer-1: unclosed")
	f.Fuzz(func(t *testing.T, in string) {
		emit, carry, acks := stripSteerAcks("", in)
		if !strings.HasSuffix(in, carry) {
			t.Fatalf("carry %q is not a suffix of the input", carry)
		}
		consumed := len(emit) + len(carry)
		for _, a := range acks {
			// A matched marker is `[STEERING ` + id + `: ` + body + `]`, so its
			// span is at least the two captured groups plus the fixed literals.
			if a.SteerID == "" {
				t.Errorf("ack with an empty id: %+v", a)
			}
			if consumed+len(a.SteerID)+len(a.Text) > len(in) {
				t.Fatalf("acks claim more bytes than the input holds: emit=%d carry=%d acks=%+v in=%d",
					len(emit), len(carry), acks, len(in))
			}
		}
		if consumed > len(in) {
			t.Fatalf("emit+carry = %d bytes from a %d byte input", consumed, len(in))
		}
	})
}

// The carry bound measures the CANDIDATE, and its edge belongs to the candidate
// too. Two ways to get it wrong, and both surface as text vanishing: measuring
// the whole reply instead of the withheld tail releases machinery the moment a
// turn grows past 8 KiB, and releasing at the edge rather than past it hands the
// client a marker that was still one byte inside the budget.
func TestStripSteerAcks_TheBoundMeasuresTheCandidate(t *testing.T) {
	atBound := steerAckPrefix + strings.Repeat("x", maxSteerCarry-len(steerAckPrefix))
	longReply := strings.Repeat("y", maxSteerCarry)
	tests := []struct {
		name      string
		in        string
		wantEmit  string
		wantCarry string
	}{
		{
			name:      "a_candidate_exactly_at_the_bound_is_still_held",
			in:        "prose " + atBound,
			wantEmit:  "prose ",
			wantCarry: atBound,
		},
		{
			name:      "a_long_reply_in_front_of_a_short_candidate_holds_it",
			in:        longReply + "[STE",
			wantEmit:  longReply,
			wantCarry: "[STE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emit, carry, acks := stripSteerAcks("", tt.in)
			if emit != tt.wantEmit {
				t.Errorf("stripSteerAcks(\"\", %d bytes) emitted %d bytes, want %d",
					len(tt.in), len(emit), len(tt.wantEmit))
			}
			if carry != tt.wantCarry {
				t.Errorf("stripSteerAcks(\"\", %d bytes) carry = %d bytes, want %d",
					len(tt.in), len(carry), len(tt.wantCarry))
			}
			if len(acks) != 0 {
				t.Errorf("acks = %+v, want none (no complete marker in the input)", acks)
			}
		})
	}
}
