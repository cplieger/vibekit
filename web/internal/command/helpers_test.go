package command

import (
	"strings"
	"testing"
)

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty string", "", 5, ""},
		{"ascii under limit", "hello", 10, "hello"},
		{"ascii at limit", "hello", 5, "hello"},
		{"ascii over limit", "hello world", 5, "hello"},
		{"emoji", "🎉🎊🎈🎁", 2, "🎉🎊"},
		{"n=0", "hello", 0, ""},
		{"n=1", "hello", 1, "h"},
		{"multibyte CJK", "你好世界", 2, "你好"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateRunes(tc.s, tc.n)
			if got != tc.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

func FuzzTruncateRunes(f *testing.F) {
	f.Add("hello", uint8(3))
	f.Add("", uint8(0))
	f.Add("🎉🎊🎈", uint8(2))
	f.Add(strings.Repeat("x", 200), uint8(100))
	f.Fuzz(func(t *testing.T, s string, n uint8) {
		result := TruncateRunes(s, int(n))
		runes := []rune(result)
		if n > 0 && len(runes) > int(n) {
			t.Errorf("len([]rune(result))=%d > n=%d", len(runes), n)
		}
		if !strings.HasPrefix(s, result) {
			t.Errorf("result %q is not a prefix of input %q", result, s)
		}
		if len([]rune(s)) <= int(n) && result != s {
			t.Errorf("input shorter than n but result differs: %q vs %q", result, s)
		}
	})
}
