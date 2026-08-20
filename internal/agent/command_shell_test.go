package agent

import (
	"testing"

	"github.com/cplieger/vibekit/internal/command"
)

func FuzzShellCappedBuffer(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add(make([]byte, command.ShellOutputCap+1))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		var b command.ShellCappedBuffer

		// Write in random-sized chunks derived from data length.
		chunkSize := 137
		for i := 0; i < len(data); i += chunkSize {
			end := min(i+chunkSize, len(data))
			n, err := b.Write(data[i:end])
			if err != nil {
				t.Fatalf("Write returned error: %v", err)
			}
			if n != end-i {
				t.Fatalf("Write returned n=%d, want %d", n, end-i)
			}
			if b.Buf.Len() > command.ShellOutputCap {
				t.Fatalf("buf.Len()=%d exceeds cap %d", b.Buf.Len(), command.ShellOutputCap)
			}
		}
	})
}
