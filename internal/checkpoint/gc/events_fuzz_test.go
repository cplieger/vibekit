package gc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/checkpoint/types"
)

// FuzzStreamEventSHAs exercises streamEventSHAs on arbitrary data, asserting
// it never panics, every returned SHA is non-empty, and every returned SHA is
// one a line-by-line JSON parse would also extract.
func FuzzStreamEventSHAs(f *testing.F) {
	f.Add([]byte(`{"before_sha":"aaa","after_sha":"bbb"}` + "\n"))
	f.Add([]byte(`not json` + "\n"))
	f.Add([]byte{})
	f.Add([]byte(`{"before_sha":"x"}` + "\n" + `{}` + "\n" + `{"after_sha":"y"}` + "\n"))
	f.Add([]byte("\x00\xff\xfe"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return // avoid writing huge temp files
		}

		path := filepath.Join(t.TempDir(), "events.jsonl")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}

		shas, err := streamEventSHAs(path)
		if err != nil {
			return // size-cap or IO errors are acceptable
		}

		// Every returned SHA must be non-empty.
		for i, sha := range shas {
			if sha == "" {
				t.Fatalf("shas[%d] is empty", i)
			}
		}

		// Cross-check: every returned SHA must come from a parseable line.
		expected := make(map[string]struct{})
		for line := range strings.SplitSeq(string(data), "\n") {
			if line == "" {
				continue
			}
			var ev types.BlobRef
			if json.Unmarshal([]byte(line), &ev) == nil {
				if ev.BeforeSHA != "" {
					expected[ev.BeforeSHA] = struct{}{}
				}
				if ev.AfterSHA != "" {
					expected[ev.AfterSHA] = struct{}{}
				}
			}
		}

		for _, sha := range shas {
			if _, ok := expected[sha]; !ok {
				t.Fatalf("streamEventSHAs returned %q not in expected set", sha)
			}
		}
	})
}
