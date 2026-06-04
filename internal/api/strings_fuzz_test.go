package api

import (
	"strings"
	"testing"
)

func FuzzTagExcluded(f *testing.F) {
	f.Add("", "[deprecated]")
	f.Add("[DEPRECATED] model", "[deprecated]")
	f.Add("[legacy]", "[legacy]")
	f.Add("café [Legacy]", "[legacy]")
	f.Add("no tags here", "[deprecated]")

	f.Fuzz(func(t *testing.T, text, tag string) {
		tags := []string{tag}
		got := TagExcluded(text, tags)
		low := strings.ToLower(text)
		expect := text != "" && tag != "" && strings.Contains(low, strings.ToLower(tag))
		if got != expect {
			t.Errorf("TagExcluded(%q, %q) = %v, want %v", text, tag, got, expect)
		}
	})
}
