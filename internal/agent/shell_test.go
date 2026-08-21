package agent

import (
	"testing"
)

// Scrollback tests now exercise byteRing directly (the old shellSession
// wrapper is gone; byteRing lives in byte_ring.go and is used by
// agent_terminal.go).

func TestByteRing_Empty(t *testing.T) {
	r := newByteRing(64)
	got := r.Bytes()
	if len(got) != 0 {
		t.Errorf("empty ring returned %d bytes", len(got))
	}
}

func TestByteRing_PartialFill(t *testing.T) {
	r := newByteRing(64)
	r.Write([]byte("hello"))
	got := r.Bytes()
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestByteRing_Wrap(t *testing.T) {
	r := newByteRing(8)
	r.Write([]byte("ABCDEFGH")) // fills exactly
	r.Write([]byte("IJ"))       // wraps: overwrites A,B
	got := r.Bytes()
	if string(got) != "CDEFGHIJ" {
		t.Errorf("got %q, want %q", got, "CDEFGHIJ")
	}
}

func TestByteRing_MultiWrap(t *testing.T) {
	r := newByteRing(4)
	// Write more than 2x the buffer size.
	r.Write([]byte("ABCDEFGHIJ"))
	got := r.Bytes()
	// Only the last 4 bytes survive.
	if string(got) != "GHIJ" {
		t.Errorf("got %q, want %q", got, "GHIJ")
	}
}

func TestKillShell_NoShellIsOK(t *testing.T) {
	h, _, _ := newTestHub()
	// Should not panic.
	h.shellMgr.kill(t.Context())
}

func FuzzByteRing(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte("ABCDEFGHIJ"))
	f.Add(make([]byte, 128))

	f.Fuzz(func(t *testing.T, data []byte) {
		const bufSize = 64
		r := newByteRing(bufSize)

		// Write in random-sized chunks.
		chunkSize := 7
		var totalWritten []byte
		for i := 0; i < len(data); i += chunkSize {
			end := min(i+chunkSize, len(data))
			r.Write(data[i:end])
			totalWritten = append(totalWritten, data[i:end]...)
		}

		got := r.Bytes()
		// Invariant: Bytes() returns the last min(total_written, bufSize) bytes.
		wantLen := min(len(totalWritten), bufSize)
		if len(got) != wantLen {
			t.Fatalf("Bytes() len=%d, want %d", len(got), wantLen)
		}
		// Content must be the tail of totalWritten.
		tail := totalWritten[len(totalWritten)-wantLen:]
		if string(got) != string(tail) {
			t.Fatalf("Bytes() content mismatch:\n got: %q\nwant: %q", got, tail)
		}
	})
}
