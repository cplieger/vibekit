package api

import "testing"

func TestStripCodeFence(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain_fence", "```\ncontent\n```", "content"},
		{"lang_fence", "```go\nfmt.Println()\n```", "fmt.Println()"},
		{"trailing_whitespace_after_close", "```\ncontent\n```\n\n", "content"},
		{"inner_fences_preserved", "```\nkeep ```inner``` text\n```", "keep ```inner``` text"},
		{"fence_only_no_newline", "```", "```"},
		{"no_fence_plaintext", "plain text", "plain text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripCodeFence(tc.in); got != tc.want {
				t.Errorf("StripCodeFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
