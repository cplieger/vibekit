package gc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/checkpoint/types"
)

func FuzzReadEventLog(f *testing.F) {
	f.Add([]byte(`{"before_sha":"abc","after_sha":"def"}` + "\n"))
	f.Add([]byte(`not json` + "\n"))
	f.Add([]byte{})
	f.Add([]byte(`{"before_sha":"a"}` + "\n" + `garbage` + "\n" + `{"after_sha":"b"}` + "\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}

		events, err := readEventLog(path)
		if err != nil {
			t.Fatal(err)
		}

		// Count valid JSON lines with non-empty SHAs.
		lines := strings.Split(string(data), "\n")
		validCount := 0
		for _, line := range lines {
			if line == "" {
				continue
			}
			var ev types.BlobRef
			if json.Unmarshal([]byte(line), &ev) == nil {
				if ev.BeforeSHA != "" || ev.AfterSHA != "" {
					validCount++
				}
			}
		}

		// Events returned must not exceed line count.
		if len(events) > len(lines) {
			t.Errorf("events (%d) > lines (%d)", len(events), len(lines))
		}
	})
}

func TestReadEventLog_MalformedLineSkipped(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantCount int
	}{
		{"empty file", "", 0},
		{"single valid line", `{"before_sha":"abc","after_sha":"def"}` + "\n", 1},
		{"valid + malformed + valid", `{"before_sha":"a"}` + "\n" + `{bad json` + "\n" + `{"after_sha":"b"}` + "\n", 2},
		{"all malformed", `not json` + "\n" + `also bad` + "\n", 0},
		{"empty JSON objects", `{}` + "\n" + `{}` + "\n", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			events, err := readEventLog(path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(events) != tt.wantCount {
				t.Errorf("got %d events, want %d", len(events), tt.wantCount)
			}
		})
	}
}
