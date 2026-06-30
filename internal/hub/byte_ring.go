// Shared circular byte buffer for subprocess output replay.
//
// Agent terminals keep the last N bytes of output for replay on
// reconnect. (The PTY shell migrated to web-terminal-engine, which owns its own
// buffer.) This type is UTF-8 boundary aware on read (drops any invalid
// byte sequences left by a multi-byte character cut at the ring
// boundary or by a partial write).

package hub

import "strings"

// byteRing is a byte-limited circular buffer that keeps the most
// recent bytes written. It implements io.Writer.
type byteRing struct {
	buf       []byte
	pos       int
	full      bool
	truncated bool
}

// newByteRing creates a ring buffer with the given capacity.
func newByteRing(capacity int) *byteRing {
	return &byteRing{buf: make([]byte, capacity)}
}

// Write appends p to the ring buffer, overwriting oldest data when
// capacity is exceeded. byteRing never fails internally (all storage
// is pre-allocated in the constructor), so Write is infallible by
// design. It deliberately does NOT implement io.Writer because no
// caller needs that and returning (int, error) would force boilerplate
// error-checking at every site.
func (r *byteRing) Write(p []byte) {
	n := len(p)
	if n == 0 {
		return
	}

	capacity := len(r.buf)
	if n >= capacity {
		// Input fills or overfills the buffer: keep only the trailing
		// `capacity` bytes. Data is evicted only when the input is strictly
		// larger than the buffer (its own leading bytes are dropped) or the
		// buffer already held bytes that this write overwrites. An
		// exactly-capacity write into an empty buffer loses nothing.
		if n > capacity || r.full || r.pos > 0 {
			r.truncated = true
		}
		copy(r.buf, p[n-capacity:])
		r.pos = 0
		r.full = true
		return
	}

	// Writing into an already-full ring overwrites the oldest bytes.
	if r.full {
		r.truncated = true
	}
	for len(p) > 0 {
		space := capacity - r.pos
		copied := copy(r.buf[r.pos:], p[:min(len(p), space)])
		r.pos += copied
		p = p[copied:]
		if r.pos >= capacity {
			r.pos = 0
			r.full = true
			// Bytes still left after the buffer fills overwrite the oldest
			// data — that is an eviction. Filling to exactly capacity with
			// nothing left over evicts nothing.
			if len(p) > 0 {
				r.truncated = true
			}
		}
	}
}

// Bytes returns the buffer contents in chronological order.
// The returned slice is a copy safe for concurrent use.
func (r *byteRing) Bytes() []byte {
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	// Buffer has wrapped: [pos..end] + [0..pos]
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}

// String returns the buffer contents as a string with any invalid
// UTF-8 byte sequences dropped — a multi-byte character cut at the ring
// boundary (leading continuation bytes) or by a partial write (trailing
// incomplete sequence), as well as any interior corruption — so the
// output is always valid UTF-8. This keeps JSON persistence and browser
// terminal replay from choking on a fragment captured mid-character.
func (r *byteRing) String() string {
	return strings.ToValidUTF8(string(r.Bytes()), "")
}

// Len returns the number of bytes currently stored.
func (r *byteRing) Len() int {
	if r.full {
		return len(r.buf)
	}
	return r.pos
}

// Truncated returns true if any data was evicted from the buffer.
func (r *byteRing) Truncated() bool { return r.truncated }
