package chat

import (
	"strings"
	"testing"
)

func FuzzSafeExportName(f *testing.F) {
	f.Add("my chat", "fallback")
	f.Add("", "fallback")
	f.Add("a/b:c*d?e<f>g|h\"i\\j", "fb")
	f.Add(strings.Repeat("x", 200), "fb")
	f.Add("\x00\x1f\x7f", "fb")

	const forbidden = "/\\:*?<>|\""

	f.Fuzz(func(t *testing.T, raw, fallback string) {
		result := safeExportName(raw, fallback)

		if !strings.HasSuffix(result, ".json") {
			t.Fatalf("missing .json suffix: %q", result)
		}

		stem := strings.TrimSuffix(result, ".json")
		if len(stem) > 80 {
			t.Fatalf("stem exceeds 80 chars: len=%d", len(stem))
		}

		for _, c := range forbidden {
			if strings.ContainsRune(result[:len(stem)], c) {
				t.Fatalf("forbidden char %q in stem %q", string(c), stem)
			}
		}
	})
}
