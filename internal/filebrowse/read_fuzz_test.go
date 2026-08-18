package filebrowse

import (
	"bytes"
	"testing"
)

func FuzzLooksBinary(f *testing.F) {
	f.Add([]byte("hello world"))
	f.Add([]byte{0})
	f.Add([]byte{})
	f.Add([]byte("text\x00binary"))

	f.Fuzz(func(t *testing.T, data []byte) {
		got := looksBinary(data)
		sniffLen := min(len(data), binarySniffN)
		want := bytes.IndexByte(data[:sniffLen], 0) >= 0
		if got != want {
			t.Errorf("looksBinary(%d bytes) = %v, want %v", len(data), got, want)
		}
	})
}
