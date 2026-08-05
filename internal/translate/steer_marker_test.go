package translate

import (
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
		emit, carry = stripSteerAcks(carry, c)
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
		emit, carry := stripSteerAcks("", tt.in)
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

// An unclosed marker cannot be allowed to swallow the rest of the turn. Past the
// bound the carry is released as ordinary text, which is the lesser wrong: some
// machinery shows, rather than the response disappearing.
func TestStripSteerAcks_ReleasesAnOverlongCandidate(t *testing.T) {
	open := "[STEERING steer-1: " + strings.Repeat("x", maxSteerCarry+1)
	emit, carry := stripSteerAcks("", open)
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
