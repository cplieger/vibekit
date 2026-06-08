package gc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/checkpoint/types"
)

// FuzzStreamEventSHAsRobustness writes arbitrary data to a temp file
// and exercises streamEventSHAs, asserting:
//  1. Never panics on any input.
//  2. Every returned SHA is non-empty.
//  3. Returned SHAs are a subset of what a line-by-line JSON parse yields.
func FuzzStreamEventSHAsRobustness(f *testing.F) {
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

		// Every SHA must be non-empty.
		for i, sha := range shas {
			if sha == "" {
				t.Fatalf("shas[%d] is empty", i)
			}
		}

		// Cross-check: build expected set from line-by-line parse.
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
