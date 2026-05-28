package api

import (
	"strings"
	"testing"
)

func TestValidMessageID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty", in: "", want: false},
		{name: "valid_alpha", in: "msg-123", want: true},
		{name: "valid_dots_hyphens_colons_underscores", in: "a.b-c:d_e", want: true},
		{name: "128_bytes", in: strings.Repeat("a", 128), want: true},
		{name: "129_bytes", in: strings.Repeat("a", 129), want: false},
		{name: "contains_space", in: "msg 123", want: false},
		{name: "contains_slash", in: "msg/123", want: false},
		{name: "contains_null", in: "msg\x00123", want: false},
		{name: "unicode", in: "msg-ñ", want: false},
		{name: "all_valid_chars", in: "A-Z.a-z_0:9", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidMessageID(tc.in); got != tc.want {
				t.Errorf("ValidMessageID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidRequestID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty_is_valid", in: "", want: true},
		{name: "valid", in: "req-abc.123", want: true},
		{name: "over_128_bytes", in: strings.Repeat("x", 129), want: false},
		{name: "invalid_chars_space", in: "req 123", want: false},
		{name: "invalid_chars_slash", in: "req/123", want: false},
		{name: "128_bytes", in: strings.Repeat("b", 128), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidRequestID(tc.in); got != tc.want {
				t.Errorf("ValidRequestID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
