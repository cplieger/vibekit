package api

import (
	"strings"
	"testing"
)

func FuzzTagExcluded(f *testing.F) {
	f.Add("[DEPRECATED] model", "[deprecated]")
	f.Add("[legacy]", "[legacy]")
	f.Add("café [Legacy]", "[legacy]")
	f.Add("no tags here", "[deprecated]")

	f.Fuzz(func(t *testing.T, text, tag string) {
		if tag == "" || text == "" {
			t.Skip("empty tag or text is not a meaningful input")
		}
		tags := []string{tag}
		got := TagExcluded(text, tags)
		expect := strings.Contains(strings.ToLower(text), tag)
		if got != expect {
			t.Errorf("TagExcluded(%q, %q) = %v, want %v", text, tag, got, expect)
		}
	})
}
