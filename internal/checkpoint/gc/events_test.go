package gc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The size cap is a strict '>': a log exactly at the cap is accepted, one
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
