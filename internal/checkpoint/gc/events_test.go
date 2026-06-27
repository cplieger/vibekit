package gc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// The size cap is a strict '>': a log exactly at the cap is accepted, one
// byte over is rejected.
func TestReadEventLog_SizeCapBoundaryStrict(t *testing.T) {
	t.Parallel()
	if _, err := readEventLog(sparseFile(t, maxEventLogBytes)); isSizeCapErr(err) {
		t.Errorf("readEventLog at exactly the cap returned a size-cap error; boundary must be strict '>' (got %v)", err)
	}
	if _, err := readEventLog(sparseFile(t, maxEventLogBytes+1)); !isSizeCapErr(err) {
		t.Errorf("readEventLog one byte over the cap: err = %v, want a size-cap error", err)
	}
}

// streamEventSHAs shares the same strict-'>' size-cap boundary as readEventLog.
func TestStreamEventSHAs_SizeCapBoundaryStrict(t *testing.T) {
	t.Parallel()
	if _, err := streamEventSHAs(sparseFile(t, maxEventLogBytes)); isSizeCapErr(err) {
		t.Errorf("streamEventSHAs at exactly the cap returned a size-cap error; boundary must be strict '>' (got %v)", err)
	}
	if _, err := streamEventSHAs(sparseFile(t, maxEventLogBytes+1)); !isSizeCapErr(err) {
		t.Errorf("streamEventSHAs one byte over the cap: err = %v, want a size-cap error", err)
	}
}

// sparseFile creates a sparse file of exactly size bytes named events.jsonl
// and returns its path. Cheap on disk, so a test can hit the size-cap
// boundary exactly without writing the full cap's worth of bytes.
func sparseFile(t *testing.T, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func isSizeCapErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exceeds size cap")
}
