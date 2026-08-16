package bridge

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// newTestFrameReader wraps s with a SMALL ReadSlice window so the
// multi-chunk ErrBufferFull path is exercised without allocating megabytes.
// The frame cap is separate from the window; see stdoutBufSize.
func newTestFrameReader(s string, window int) *frameReader {
	return newFrameReader(bufio.NewReaderSize(strings.NewReader(s), window))
}

// drainFrames reads until a terminal error and reports the frames it got, the
// per-frame dropped byte counts, and the error that ended the loop.
func drainFrames(t *testing.T, fr *frameReader) (frames []string, drops []int, err error) {
	t.Helper()
	for range 100 {
		line, dropped, rerr := fr.readFrame()
		if dropped > 0 {
			drops = append(drops, dropped)
		}
		if rerr != nil {
			return frames, drops, rerr
		}
		if dropped == 0 {
			frames = append(frames, string(line))
		}
	}
	t.Fatal("frame reader did not terminate within 100 frames")
	return nil, nil, nil
}

func TestFrameReader_SplitsOnNewlines(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "one terminated frame", input: "{\"a\":1}\n", want: []string{`{"a":1}`}},
		{
			name:  "several frames",
			input: "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n",
			want:  []string{`{"a":1}`, `{"b":2}`, `{"c":3}`},
		},
		{
			// bufio.Scanner returned a final unterminated token; the reader keeps
			// that so a process that died mid-write reports the same way.
			name:  "final frame with no terminator is still returned",
			input: "{\"a\":1}\n{\"partial\"",
			want:  []string{`{"a":1}`, `{"partial"`},
		},
		{
			// A blank line reaches json.Unmarshal and fails there, which keeps it
			// inside the parse-error circuit breaker rather than spinning silently.
			name:  "an empty frame is passed through, not skipped",
			input: "\n{\"a\":1}\n",
			want:  []string{"", `{"a":1}`},
		},
		{
			name:  "a CR is content, not a delimiter",
			input: "{\"a\":1}\r\n",
			want:  []string{"{\"a\":1}\r"},
		},
		{name: "empty stream yields nothing", input: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// window 8 forces multi-chunk assembly on every case above.
			frames, drops, err := drainFrames(t, newTestFrameReader(tc.input, 8))
			if !errors.Is(err, io.EOF) {
				t.Errorf("terminal error = %v, want io.EOF", err)
			}
			if len(drops) != 0 {
				t.Errorf("dropped %v bytes, want none", drops)
			}
			if len(frames) != len(tc.want) {
				t.Fatalf("frames = %q, want %q", frames, tc.want)
			}
			for i := range tc.want {
				if frames[i] != tc.want[i] {
					t.Errorf("frame %d = %q, want %q", i, frames[i], tc.want[i])
				}
			}
		})
	}
}

// The whole point of D24b: a frame past the cap is drained to its terminator and
// the stream RESYNCHRONISES, so the frames after it still arrive. Under the
// bufio.Scanner this replaced, ErrTooLong ended the scan and everything after the
// oversize frame was lost with the bridge.
func TestFrameReader_SurvivesAnOversizeFrame(t *testing.T) {
	huge := strings.Repeat("x", scannerLineCap+64)
	input := "{\"first\":1}\n" + huge + "\n{\"after\":2}\n"

	frames, drops, err := drainFrames(t, newTestFrameReader(input, 64*1024))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("terminal error = %v, want io.EOF", err)
	}
	want := []string{`{"first":1}`, `{"after":2}`}
	if len(frames) != len(want) || frames[0] != want[0] || frames[1] != want[1] {
		t.Errorf("frames = %q, want %q (the frame AFTER the oversize one must still arrive)", frames, want)
	}
	if len(drops) != 1 {
		t.Fatalf("dropped counts = %v, want exactly one oversize report", drops)
	}
	if drops[0] < scannerLineCap {
		t.Errorf("dropped %d bytes, want at least the cap (%d)", drops[0], scannerLineCap)
	}
}

// The budget is per FRAME and in BYTES, not a count of oversize frames: each
// drain provably ends on a frame boundary, so a replay of several oversize but
// TERMINATED frames stays survivable. A count would kill the bridge on exactly
// that replay.
func TestFrameReader_ManyOversizeFramesEachGetTheirOwnBudget(t *testing.T) {
	huge := strings.Repeat("y", scannerLineCap+1)
	var sb strings.Builder
	for range 4 {
		sb.WriteString(huge)
		sb.WriteString("\n")
	}
	sb.WriteString("{\"survivor\":true}\n")

	frames, drops, err := drainFrames(t, newTestFrameReader(sb.String(), 64*1024))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("terminal error = %v, want io.EOF (four terminated oversize frames must not exhaust anything)", err)
	}
	if len(drops) != 4 {
		t.Errorf("oversize reports = %d, want 4 (one per frame)", len(drops))
	}
	if len(frames) != 1 || frames[0] != `{"survivor":true}` {
		t.Errorf("frames = %q, want the one survivor after all four", frames)
	}
}

// Only a single blob that never terminates exhausts the budget, and that ends
// the read loop: there is no frame boundary left to resynchronise on.
func TestFrameReader_UnterminatedBlobExhaustsTheBudget(t *testing.T) {
	fr := newFrameReader(bufio.NewReaderSize(&endlessReader{b: 'z'}, 64*1024))
	line, dropped, err := fr.readFrame()
	if !errors.Is(err, errFrameDrainExhausted) {
		t.Fatalf("err = %v, want errFrameDrainExhausted", err)
	}
	if line != nil {
		t.Errorf("returned %d bytes of frame, want none", len(line))
	}
	if dropped <= oversizeDrainCap {
		t.Errorf("dropped %d bytes, want more than the budget (%d)", dropped, oversizeDrainCap)
	}
}

// A read error mid-frame is terminal and reports no frame, so a truncated
// payload is never handed to json.Unmarshal as though it were complete.
func TestFrameReader_ReadErrorIsTerminal(t *testing.T) {
	sentinel := errors.New("pipe broke")
	fr := newFrameReader(bufio.NewReaderSize(errReader{failErr: sentinel}, 64))
	line, dropped, err := fr.readFrame()
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if line != nil || dropped != 0 {
		t.Errorf("line = %q dropped = %d, want neither", line, dropped)
	}
}

// endlessReader never returns a delimiter, which is the one input that can
// exhaust the drain budget.
type endlessReader struct{ b byte }

func (e *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
	}
	return len(p), nil
}

// FuzzFrameReader pins the reader's real invariant rather than merely asserting
// it does not panic: every frame it returns must be exactly the bytes between two
// delimiters, and the reader must be left positioned immediately after the second
// one. The oracle is strings.Split over the same input, which is the definition of
// newline framing — so a mis-split, a swallowed frame or a duplicated one fails
// here even though none of them crashes.
//
// Only inputs whose frames all fit the cap are compared against the oracle;
// oversize framing has its own table tests above, and the seeds keep this target
// standing alone (the weekly fuzz runs it with -run='^$').
func FuzzFrameReader(f *testing.F) {
	f.Add([]byte("{\"a\":1}\n{\"b\":2}\n"))
	f.Add([]byte("\n\n\n"))
	f.Add([]byte("no terminator"))
	f.Add([]byte("trailing\n"))
	f.Add([]byte("\r\n\r\n"))
	f.Add([]byte{0x00, 0x0a, 0xff, 0x0a})
	f.Add([]byte("\xc3\n\xa9\n")) // a UTF-8 rune split across two frames
	f.Add([]byte("a\nb"))

	f.Fuzz(func(t *testing.T, input []byte) {
		fr := newFrameReader(bufio.NewReaderSize(bytes.NewReader(input), 16))
		var got [][]byte
		for range len(input) + 2 {
			line, dropped, err := fr.readFrame()
			if dropped != 0 {
				t.Fatalf("no seed exceeds the %d-byte cap, yet %d bytes were dropped", scannerLineCap, dropped)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("unexpected error over an in-memory reader: %v", err)
				}
				break
			}
			got = append(got, append([]byte(nil), line...))
		}

		// The oracle: bytes between newlines, plus a final unterminated
		// remainder when the input does not end on a delimiter.
		var want [][]byte
		for i, part := range bytes.Split(input, []byte("\n")) {
			last := i == bytes.Count(input, []byte("\n"))
			if last && len(part) == 0 {
				continue // the empty tail after a trailing delimiter is not a frame
			}
			want = append(want, part)
		}

		if len(got) != len(want) {
			t.Fatalf("read %d frames, want %d\n got=%q\nwant=%q", len(got), len(want), got, want)
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("frame %d = %q, want %q", i, got[i], want[i])
			}
		}
	})
}
