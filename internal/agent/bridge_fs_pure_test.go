package agent

// Pure-function tests extracted from bridge_fs.go. These exercise the
// slicing, truncation, and tool-call-id helpers that
// bridge_fs uses on every agent fs/* round trip — and that the batch-15
// review flagged as undertested.

import (
	"context"
	"math"
	"strings"
	"testing"
)

// TestTruncateForStaging_Table is GONE with truncateForStaging. It capped the
// old/new text a staged write carried in its SSE payload, and there is no staged
// write: KAS holds the content and vibekit's fs handler writes through.

// TestCurrentMessageCount is GONE with currentMessageCount. It counted a chat's
// persisted messages as the "restore watermark on every snapshot", and snapshots
// are KAS's now — the same deletion that orphaned internal/checkpoint. Nothing in
// production asked the question, so there is no behaviour left to pin.

// TestSliceByLines_Table consolidates the pure-function edge cases for
// sliceByLines into a single table-driven test.
func TestSliceByLines_Table(t *testing.T) {
	t.Parallel()
	line2 := 2
	line99 := 99
	line0 := 0
	limitMax := math.MaxInt
	limit100 := 100
	limit0 := 0
	limit2 := 2

	cases := []struct {
		name    string
		content string
		line    *int
		limit   *int
		want    string
	}{
		{"nil line and limit returns whole", "a\nb\n", nil, nil, "a\nb\n"},
		{"start beyond end returns empty", "a\nb\n", &line99, nil, ""},
		{"limit larger than remaining returns tail", "a\nb\nc\n", &line2, &limit100, "b\nc\n"},
		{"integer overflow does not panic", "a\nb\nc\n", &line2, &limitMax, "b\nc\n"},
		{"line zero keeps full content", "a\nb\nc\n", &line0, nil, "a\nb\nc\n"},
		{"limit zero does not narrow", "a\nb\nc\n", nil, &limit0, "a\nb\nc\n"},
		{"limit without line narrows from start", "a\nb\nc\nd\n", nil, &limit2, "a\nb\n"},
		{"midrange narrow within window", "a\nb\nc\nd\n", &line2, &limit2, "b\nc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sliceByLines(tc.content, tc.line, tc.limit)
			if got != tc.want {
				t.Errorf("sliceByLines = %q, want %q", got, tc.want)
			}
		})
	}
}

// FuzzSliceByLines exercises the line-slicing parser with arbitrary inputs.
// Invariant: for any (content, line, limit) where line >= 1 and limit >= 1,
// the output is a substring of content (or empty).
//
// Since the strings.Lines rewrite the substring half is true by construction
// (the result is one slice expression over content), so what this target now
// guards is that the two offset walks cannot produce lo > hi or an out-of-range
// index — which would panic rather than fail an assertion.
func FuzzSliceByLines(f *testing.F) {
	f.Add("a\nb\nc\n", 2, 2)
	f.Add("a\nb\n", 99, 1)
	f.Add("a\nb\nc\n", 2, math.MaxInt)
	f.Add("", 1, 1)
	f.Add("single", 1, 1)
	// Terminator shapes strings.Lines and strings.SplitAfter disagree about:
	// a bare '\r', a '\r\n' pair, and a body with no trailing newline. The
	// differential sweep behind the rewrite found no divergence on any of
	// them, and these seeds are what keep that true.
	f.Add("a\r\nb\r\n", 1, 1)
	f.Add("a\rb\rc", 2, 1)
	f.Add("no trailing newline", 1, 2)
	f.Add("\n\n\n", 2, 1)

	f.Fuzz(func(t *testing.T, content string, line, limit int) {
		if line < 1 || limit < 1 {
			return
		}
		lp := &line
		limp := &limit
		got := sliceByLines(content, lp, limp)
		if len(got) > len(content) {
			t.Errorf("output longer than input: len(got)=%d > len(content)=%d", len(got), len(content))
		}
		if got != "" && !strings.Contains(content, got) {
			t.Errorf("output %q is not a substring of content %q", got, content)
		}
	})
}

// TestFsErrorIsRoutine pins the routine-vs-real classification used to
// decide whether an fs error is worth logging: only the sentinel
// errIgnored is routine; nil and real errors are not.
func TestFsErrorIsRoutine(t *testing.T) {
	t.Parallel()
	if got := fsErrorIsRoutine(errIgnored); !got {
		t.Errorf("fsErrorIsRoutine(errIgnored) = %v, want true", got)
	}
	if got := fsErrorIsRoutine(nil); got {
		t.Errorf("fsErrorIsRoutine(nil) = %v, want false", got)
	}
	if got := fsErrorIsRoutine(context.Canceled); got {
		t.Errorf("fsErrorIsRoutine(context.Canceled) = %v, want false", got)
	}
}

// TestSliceByLines_offsetLimitOvershootReturnsTail pins the read-window
// offset arithmetic. When the window starts past line 1 (start > 0) and the
// requested limit is larger than the number of lines that remain, the result
// must be the clamped tail of the file, never an over-read. The window end is
// narrowed to start+limit only while limit is smaller than the remaining-line
// count (end-start); flipping that subtraction to addition (end+start) pushes
// end past the slice length and panics instead of returning the tail. The
// existing table covers limit==remaining and limit>>remaining; this pins the
// just-past-remaining point where the sign of the offset math is observable.
func TestSliceByLines_offsetLimitOvershootReturnsTail(t *testing.T) {
	t.Parallel()
	line, limit := 2, 4 // start at line 2; only 2 lines remain in a 3-line file
	got := sliceByLines("a\nb\nc\n", &line, &limit)
	if got != "b\nc\n" {
		t.Errorf("sliceByLines(\"a\\nb\\nc\\n\", line=2, limit=4) = %q, want %q (tail, no over-read)",
			got, "b\nc\n")
	}
}
