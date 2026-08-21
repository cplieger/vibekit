package ids

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

func TestValidChatID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "upper_Z", in: "Z", want: true},
		{name: "upper_A", in: "A", want: true},
		{name: "lower_a", in: "a", want: true},
		{name: "lower_z", in: "z", want: true},
		{name: "digit_0", in: "0", want: true},
		{name: "digit_9", in: "9", want: true},
		{name: "underscore", in: "_", want: true},
		{name: "hyphen", in: "-", want: true},
		{name: "mixed_charset", in: "abcZ09_-", want: true},
		{name: "space_rejected", in: " ", want: false},
		{name: "empty_rejected", in: "", want: false},
		{name: "slash_rejected", in: "a/b", want: false},
		{name: "len_128_accepted", in: strings.Repeat("a", 128), want: true},
		{name: "len_129_rejected", in: strings.Repeat("a", 129), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidChatID(tc.in); got != tc.want {
				t.Errorf("ValidChatID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidIdent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty_is_valid", in: "", want: true},
		{name: "plain", in: "abc", want: true},
		{name: "with_dot", in: "a.b", want: true},
		{name: "model_with_hyphen_and_dot", in: "model-1.2", want: true},
		{name: "leading_dot_rejected", in: ".hidden", want: false},
		{name: "leading_hyphen_rejected", in: "-flag", want: false},
		{name: "all_dots_rejected", in: "...", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidIdent(tc.in); got != tc.want {
				t.Errorf("ValidIdent(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidSessionID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "plain", in: "abc", want: true},
		{name: "dot_dot_rejected", in: "..", want: false},
		{name: "single_dot_rejected", in: ".", want: false},
		{name: "slash_rejected", in: "a/b", want: false},
		{name: "empty_rejected", in: "", want: false},
		{name: "len_128_accepted", in: strings.Repeat("s", 128), want: true},
		{name: "len_129_rejected", in: strings.Repeat("s", 129), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidSessionID(tc.in); got != tc.want {
				t.Errorf("ValidSessionID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
