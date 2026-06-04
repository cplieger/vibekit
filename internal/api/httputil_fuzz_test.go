package api

import (
	"bytes"
	"testing"
)

func FuzzLimitedWriter(f *testing.F) {
	f.Add(int64(0), []byte("hello"))
	f.Add(int64(3), []byte("hello"))
	f.Add(int64(100), []byte("hi"))
	f.Add(int64(-1), []byte("x"))
	f.Add(int64(1), []byte{})

	f.Fuzz(func(t *testing.T, n int64, p []byte) {
		var buf bytes.Buffer
		lw := &LimitedWriter{W: &buf, N: n}
		wrote, err := lw.Write(p)
		_ = err // underlying bytes.Buffer never errors
		if wrote < 0 || wrote > len(p) {
			t.Errorf("Write returned %d for len(p)=%d", wrote, len(p))
		}
		limit := n
		if limit < 0 {
			limit = 0
		}
		if int64(buf.Len()) > limit {
			t.Errorf("underlying got %d bytes, cap was %d", buf.Len(), n)
		}
	})
}
