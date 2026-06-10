// Shared circular byte buffer for subprocess output replay.
//
// Agent terminals keep the last N bytes of output for replay on
// reconnect. (The PTY shell migrated to vterm, which owns its own
// buffer.) This type is UTF-8 boundary aware on read (advances past
// partial multi-byte characters at the cut point).

package hub

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
		// Data larger than buffer: keep only the tail.
		copy(r.buf, p[n-capacity:])
		r.pos = 0
		r.full = true
		r.truncated = true
		return
	}

	for len(p) > 0 {
		space := capacity - r.pos
		copied := copy(r.buf[r.pos:], p[:min(len(p), space)])
		r.pos += copied
		if r.pos >= capacity {
			r.pos = 0
			r.full = true
			r.truncated = true
		}
		p = p[copied:]
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

// String returns the buffer contents as a string, advancing past any
// partial UTF-8 sequence at the start (so the output is always valid
// UTF-8 when the input was valid UTF-8).
func (r *byteRing) String() string {
	b := r.Bytes()
	// Skip leading continuation bytes (0x80..0xBF) that indicate a
	// partial multi-byte character was cut at the ring boundary.
	for len(b) > 0 && b[0]&0xC0 == 0x80 {
		b = b[1:]
	}
	return string(b)
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
