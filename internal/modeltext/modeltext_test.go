package modeltext

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

// TestHidden pins the named policy against the tag list it is built from, and
// the ambient-set boundary: [internal] and [experimental] are SHOWN in the
// picker, so Hidden must answer false for them or hub's wider policy would be
// indistinguishable from this one.
func TestHidden(t *testing.T) {
	cases := []struct {
		name, in string
		want     bool
	}{
		{"deprecated_lowercase", "a model [deprecated]", true},
		{"deprecated_titlecase", "a model [Deprecated]", true},
		{"deprecated_uppercase", "A MODEL [DEPRECATED]", true},
		{"legacy", "old thing [legacy]", true},
		{"untagged", "a perfectly good model", false},
		{"empty", "", false},
		{"internal_is_not_hidden", "a model [Internal]", false},
		{"experimental_is_not_hidden", "a model [Experimental]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Hidden(tc.in); got != tc.want {
				t.Errorf("Hidden(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHiddenTagsIsACopy pins that a caller extending the list cannot reach the
// package's own. The exported slice this replaced was writable by every
// importer.
func TestHiddenTagsIsACopy(t *testing.T) {
	got := HiddenTags()
	if len(got) == 0 {
		t.Fatal("HiddenTags() is empty")
	}
	got[0] = "[mutated]"
	if HiddenTags()[0] == "[mutated]" {
		t.Error("HiddenTags() hands out the package's own slice")
	}
	if !Hidden("x [deprecated]") {
		t.Error("mutating the returned slice changed the policy")
	}
}
