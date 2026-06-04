package checkpoint

import (
	"strings"
	"testing"
)

func FuzzBytesToLines(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("\n"))
	f.Add([]byte("a\nb\n"))
	f.Add([]byte("no trailing newline"))

	f.Fuzz(func(t *testing.T, data []byte) {
		lines := bytesToLines(data)
		if len(data) == 0 {
			if lines != nil {
				t.Fatal("empty input should return nil")
			}
			return
		}
		rebuilt := strings.Join(lines, "\n")
		trimmed := strings.TrimSuffix(string(data), "\n")
		if rebuilt != trimmed {
			t.Errorf("roundtrip failed: got %q, want %q", rebuilt, trimmed)
		}
	})
}
