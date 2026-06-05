package gc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibekit/internal/checkpoint/types"
)

// FuzzReadEventLogParity writes arbitrary JSONL to a temp file and
// verifies readEventLog returns entries consistent with a line-by-line
// JSON parse. It also asserts no panics on any input.
func FuzzReadEventLogParity(f *testing.F) {
	f.Add([]byte(`{"before_sha":"abc","after_sha":"def"}` + "\n"))
	f.Add([]byte(`not json` + "\n"))
	f.Add([]byte{})
	f.Add([]byte(`{"before_sha":"x"}` + "\n" + `garbage` + "\n" + `{"after_sha":"y"}` + "\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		path := filepath.Join(t.TempDir(), "events.jsonl")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}

		events, err := readEventLog(path)
		if err != nil {
			t.Fatal(err)
		}

		// Count parseable JSON lines as reference.
		var want int
		for line := range strings.SplitSeq(string(data), "\n") {
			if line == "" {
				continue
			}
			var ev types.BlobRef
			if json.Unmarshal([]byte(line), &ev) == nil {
				want++
			}
		}

		if len(events) != want {
			t.Errorf("readEventLog returned %d events, expected %d parseable lines", len(events), want)
		}
	})
}
