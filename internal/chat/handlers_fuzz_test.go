package chat

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzExportFilename(f *testing.F) {
	f.Add("my chat", "c1")
	f.Add("", "c1")
	f.Add("a/b:c*d?e<f>g|h\"i\\j", "cAFE")
	f.Add(strings.Repeat("x", 200), "c1")
	f.Add("\x00\x1f\x7f", "c1")
	f.Add("", "")

	const forbidden = "/\\:*?<>|\""

	f.Fuzz(func(t *testing.T, name, id string) {
		result := exportFilename(name, id, ".md")

		if !strings.HasSuffix(result, ".md") {
			t.Fatalf("missing .md suffix: %q", result)
		}
		if !utf8.ValidString(result) {
			t.Fatalf("result is not valid UTF-8: %q", result)
		}
		if strings.ContainsAny(result, "\x00") {
			t.Fatalf("result contains NUL: %q", result)
		}
		for _, c := range forbidden {
			if strings.ContainsRune(result, c) {
				t.Fatalf("forbidden char %q in %q", string(c), result)
			}
		}
	})
}
