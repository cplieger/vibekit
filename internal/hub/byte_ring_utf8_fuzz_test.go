package hub

import (
	"testing"
	"unicode/utf8"
)

func FuzzByteRing_String_UTF8(f *testing.F) {
	f.Add([]byte("hello world"), 16)
	f.Add([]byte("日本語テスト"), 8)
	f.Add([]byte{0xF0, 0x9F, 0x98, 0x80, 0xE2, 0x9C, 0x93}, 5)
	f.Add([]byte{0x80, 0xBF, 0x41, 0x42}, 4)

	f.Fuzz(func(t *testing.T, data []byte, cap int) {
		if cap < 1 || cap > 4096 {
			return
		}
		r := newByteRing(cap)
		r.Write(data)
		s := r.String()
		if !utf8.ValidString(s) {
			t.Fatalf("String() not valid UTF-8 for cap=%d data=%x result=%x", cap, data, s)
		}
		if len(s) > r.Len() {
			t.Fatalf("String() longer than Len(): %d > %d", len(s), r.Len())
		}
	})
}
