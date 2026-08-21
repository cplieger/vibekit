package modeltext

import (
	"strings"
	"testing"
)

func FuzzHasAnyTag(f *testing.F) {
	f.Add("[DEPRECATED] model", "[deprecated]")
	f.Add("[legacy]", "[legacy]")
	f.Add("café [Legacy]", "[legacy]")
	f.Add("no tags here", "[deprecated]")

	f.Fuzz(func(t *testing.T, text, tag string) {
		if tag == "" || text == "" {
			t.Skip("empty tag or text is not a meaningful input")
		}
		tags := []string{tag}
		got := HasAnyTag(text, tags)
		expect := strings.Contains(strings.ToLower(text), tag)
		if got != expect {
			t.Errorf("HasAnyTag(%q, %q) = %v, want %v", text, tag, got, expect)
		}
	})
}

func FuzzStripCodeFence(f *testing.F) {
	// Seed corpus from existing table-driven cases + edge cases.
	seeds := []string{
		"",
		"hello world",
		"```\ncode\n```",
		"```go\npackage main\n```",
		"```\n```",
		"```",
		"no fence here",
		"```json\n{\"key\": \"value\"}\n```",
		"```\nnested\n```inner```\n```",
		"```verylonglanguagetag\ncontent\n```",
		"```\n\n\n```",
		"```\ncontent\n```  \n",
		"```go\nfunc main() {}\n```\n",
		"```\n" + strings.Repeat("x", 4096) + "\n```",
		"```日本語\ncontent\n```",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := StripCodeFence(s)
		// Invariant: must not panic (implicit by reaching here).
		// Invariant: if input did NOT start with ```, output must equal input (no-op).
		if !strings.HasPrefix(s, "```") && out != s {
			t.Errorf("StripCodeFence(%q) modified non-fenced input: %q", s, out)
		}
		// Invariant: if input started with ``` and contained a newline,
		// the output must not contain the opening fence line as a prefix.
		if strings.HasPrefix(s, "```") && strings.Contains(s, "\n") {
			fenceLine := s[:strings.IndexByte(s, '\n')]
			// The output should be a substring of the input (content after
			// the opening fence line), so it must be shorter or equal.
			if len(out) > len(s)-len(fenceLine)-1 {
				t.Errorf("StripCodeFence(%q) output longer than expected: %q", s, out)
			}
		}
	})
}
