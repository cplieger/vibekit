package command

import "testing"

// FuzzShellCappedBufferWrite exercises the ShellCappedBuffer with
// arbitrary multi-write sequences and verifies:
//  1. Write never returns an error.
//  2. Write always reports len(p) bytes consumed (callers rely on this).
//  3. Internal buffer length never exceeds ShellOutputCap.
//  4. Truncated flag is set iff total offered bytes exceeded the cap.
func FuzzShellCappedBufferWrite(f *testing.F) {
	f.Add([]byte("hello"), []byte(" world"))
	f.Add(make([]byte, ShellOutputCap+1), []byte("x"))
	f.Add([]byte{}, []byte("abc"))
	f.Add([]byte("a"), make([]byte, ShellOutputCap))

	f.Fuzz(func(t *testing.T, chunk1, chunk2 []byte) {
		var b ShellCappedBuffer

		n1, err1 := b.Write(chunk1)
		if err1 != nil {
			t.Fatalf("Write(chunk1) error: %v", err1)
		}
		if n1 != len(chunk1) {
			t.Fatalf("Write(chunk1) = %d, want %d", n1, len(chunk1))
		}

		n2, err2 := b.Write(chunk2)
		if err2 != nil {
			t.Fatalf("Write(chunk2) error: %v", err2)
		}
		if n2 != len(chunk2) {
			t.Fatalf("Write(chunk2) = %d, want %d", n2, len(chunk2))
		}

		if b.Buf.Len() > ShellOutputCap {
			t.Fatalf("buffer length %d exceeds cap %d", b.Buf.Len(), ShellOutputCap)
		}

		totalOffered := len(chunk1) + len(chunk2)
		if totalOffered > ShellOutputCap && !b.Truncated {
			t.Fatal("Truncated not set despite exceeding cap")
		}
		if totalOffered <= ShellOutputCap && b.Truncated {
			t.Fatal("Truncated set despite not exceeding cap")
		}
	})
}
