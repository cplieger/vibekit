package bridge

// Newline-delimited frame reading off the bridge's stdout, with an oversize
// frame SURVIVED rather than fatal.
//
// This replaced a bufio.Scanner, and the swap is the whole point rather than a
// style preference. bufio.ErrTooLong is terminal for the Scanner that raised it
// and leaves no resumable position, so one frame past the cap ended the scan,
// readLoop treated the bridge as exited, and a single large tool result killed
// the chat's session. bufio.Reader.ReadSlice returns ErrBufferFull and leaves the
// reader positioned MID-LINE, which is what makes a drain-to-the-delimiter
// possible: the oversize frame is consumed to its terminator and the stream
// resynchronises on a real frame boundary.
//
// Adopted from KiroCrew's _drain_oversize_line (see #kiro-crew-research), with
// its three load-bearing details kept:
//
//  1. The recovered remainder is DISCARDED, never parsed. It is a byte slice cut
//     at an arbitrary offset, so it is not JSON and can split a multi-byte UTF-8
//     rune; the accumulated prefix is dropped for the same reason.
//  2. The drain budget is per FRAME and in BYTES, not a count of oversize frames.
//     Each drain provably ends on a frame boundary, so a replay of many
//     legitimately-oversize-but-terminated frames each gets its own budget and
//     stays survivable. A count would kill the bridge on exactly that replay.
//     Only a single blob that never terminates can exhaust it.
//  3. Crew's own bounded reader can afford a bare discard because every one of
//     its callers runs a deadline. vibekit's Bridge.Call deliberately does not,
//     so the Go port MUST answer the pending requests a discard would otherwise
//     orphan forever. That half is readLoop's (see reportDroppedFrame); this file
//     only reports the loss upward.

import (
	"bufio"
	"errors"
)

// oversizeDrainCap bounds the bytes discarded while draining ONE oversize frame.
// 16x the frame cap mirrors Crew's ratio: generous enough that a frame merely
// bigger than expected always drains, small enough that an unterminated blob is
// declared garbage before it can be read forever.
const oversizeDrainCap = 16 * scannerLineCap

// errFrameDrainExhausted ends the read loop: a single frame consumed the whole
// drain budget without producing a terminator, so the stream is not
// newline-delimited JSON any more and there is no boundary left to resynchronise
// on. Distinct from a read error so the log line can say which happened.
var errFrameDrainExhausted = errors.New("oversize ACP frame did not terminate within the drain budget")

// frameReader reads newline-delimited frames off one io.Reader.
//
// Not safe for concurrent use, which is fine and deliberate: exactly one
// goroutine (readLoop) owns it for the bridge's lifetime.
type frameReader struct {
	r   *bufio.Reader
	buf []byte

	// Per-call accumulation state, reset at the top of every readFrame. Fields
	// rather than locals so the chunk-folding decision can be its own method
	// without threading three values in and out of it; the buffer is reused
	// across calls, which is why it cannot live in a local either.
	dropped  int
	draining bool
}

// newFrameReader wraps r with the bridge's stdout buffer size. The buffer is the
// ReadSlice window, NOT the frame cap: a frame larger than the window is
// assembled across several ErrBufferFull reads.
func newFrameReader(r *bufio.Reader) *frameReader {
	return &frameReader{r: r}
}

// readFrame returns the next frame's bytes, the number of bytes discarded
// because a frame exceeded scannerLineCap, and a terminal error.
//
// Exactly one of the first two is meaningful per call: a frame that fitted
// returns (bytes, 0, nil); one that did not returns (nil, n>0, nil) after
// draining to its terminator. err is io.EOF at end of stream, a read error, or
// errFrameDrainExhausted.
//
// The returned slice aliases the reader's own accumulation buffer and is
// invalidated by the next call, which is bufio.Scanner.Bytes' contract too and
// is what the caller was already written against: json.Unmarshal copies
// everything it retains, json.RawMessage fields included.
//
// The trailing delimiter is stripped, so the length accounting is frame CONTENT
// bytes and matches the Scanner cap this replaced. A frame is otherwise passed
// through verbatim, an empty one included: a blank line reaches json.Unmarshal
// and fails there, which keeps it inside the parse-error circuit breaker's
// coverage rather than becoming an unbounded silent spin.
func (fr *frameReader) readFrame() (frame []byte, dropped int, err error) {
	fr.buf, fr.dropped, fr.draining = fr.buf[:0], 0, false
	for {
		chunk, readErr := fr.r.ReadSlice('\n')
		terminated := readErr == nil
		if terminated {
			chunk = chunk[:len(chunk)-1] // ReadSlice guarantees the last byte is '\n'
		}
		fr.absorb(chunk)
		// dropped is nonzero only in the draining branches, so this needs no
		// separate draining test.
		if fr.dropped > oversizeDrainCap {
			return nil, fr.dropped, errFrameDrainExhausted
		}
		if terminated {
			return fr.complete()
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return fr.truncated(readErr)
	}
}

// absorb folds one chunk into the frame in progress, or into the drain count
// once this frame has crossed the cap.
func (fr *frameReader) absorb(chunk []byte) {
	switch {
	case fr.draining:
		fr.dropped += len(chunk)
	case len(fr.buf)+len(chunk) > scannerLineCap:
		// Crossing the cap. Abandon the prefix in hand as well: it is the front
		// of a frame we will never complete, so keeping it would only hand
		// json.Unmarshal a truncated object.
		fr.draining = true
		fr.dropped = len(fr.buf) + len(chunk)
		fr.buf = fr.buf[:0]
	default:
		fr.buf = append(fr.buf, chunk...)
	}
}

// complete answers a frame that reached its terminator: the bytes, or the drop
// count when the frame was over the cap.
func (fr *frameReader) complete() (frame []byte, dropped int, err error) {
	if fr.draining {
		return nil, fr.dropped, nil
	}
	return fr.buf, 0, nil
}

// truncated answers a read that ended without a terminator (io.EOF or a real
// read error). A partial frame still in hand is returned like
// bufio.Scanner's final token, so a process that died mid-write is reported the
// same way it always was; the next call answers the error.
func (fr *frameReader) truncated(cause error) (frame []byte, dropped int, err error) {
	if !fr.draining && len(fr.buf) > 0 {
		return fr.buf, 0, nil
	}
	return nil, fr.dropped, cause
}
