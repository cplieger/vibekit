package command

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/rpcerr"
)

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want string
		n    int
	}{
		{name: "empty string", s: "", want: "", n: 5},
		{name: "ascii under limit", s: "hello", want: "hello", n: 10},
		{name: "ascii at limit", s: "hello", want: "hello", n: 5},
		{name: "ascii over limit", s: "hello world", want: "hello", n: 5},
		{name: "emoji", s: "🎉🎊🎈🎁", want: "🎉🎊", n: 2},
		{name: "n=0", s: "hello", want: "", n: 0},
		{name: "n=1", s: "hello", want: "h", n: 1},
		{name: "multibyte CJK", s: "你好世界", want: "你好", n: 2},
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

// errText renders a handler's returned error the way the dispatcher writes it,
// so a test asserting on the client-visible message asserts on the same string
// ServeHTTP would send. It is rpcerr.Text, not err.Error(): four handlers forward
// a bridge Call failure verbatim, and on a -32603 err.Error() is KAS's literal
// "Internal error" while the cause sits in error.data.
//
// It replaced reading w.Body.String() off a recorder. The handlers no longer
// write a response, so there is no body to read — which is the point: a test that
// wants the message asks the error for it.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return rpcerr.Text(err)
}
