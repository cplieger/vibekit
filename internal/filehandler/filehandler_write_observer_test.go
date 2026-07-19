package filehandler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// observed records one WriteObserver invocation.
type observed struct {
	abs        string
	content    string
	diskAtFire string // file content on disk at the moment the observer ran
}

// installObserver wires a recording observer onto h and returns the
// call log. diskAtFire proves the pre-write contract: the observer
// must run while the OLD content is still on disk.
func installObserver(h *Handler) *[]observed {
	calls := &[]observed{}
	h.SetWriteObserver(func(_ context.Context, abs string, content []byte) {
		disk, _ := os.ReadFile(abs)
		*calls = append(*calls, observed{abs: abs, content: string(content), diskAtFire: string(disk)})
	})
	return calls
}

func TestWriteFile_FiresObserverBeforeWrite(t *testing.T) {
	h, dir, prefix := testDir(t)
	calls := installObserver(h)

	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := putReq(t, h, "/api/file?path=/"+prefix+"/f.txt", `{"content":"new"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.abs != target {
		t.Errorf("observer abs = %q, want %q", c.abs, target)
	}
	if c.content != "new" {
		t.Errorf("observer content = %q, want %q", c.content, "new")
	}
	if c.diskAtFire != "old" {
		t.Errorf("disk at observer time = %q, want %q (observer must fire pre-write)", c.diskAtFire, "old")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("final content = %q, want %q", got, "new")
	}
}

// TestWriteFile_ObserverSkippedOnRejectedRequests: requests that never
// reach the write (invalid JSON, directory target) must not fire the
// observer — a capture for a save that never happened would append a
// phantom checkpoint event for nothing.
func TestWriteFile_ObserverSkippedOnRejectedRequests(t *testing.T) {
	h, dir, prefix := testDir(t)
	calls := installObserver(h)

	if err := os.MkdirAll(filepath.Join(dir, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"invalid json", "/api/file?path=/" + prefix + "/f.txt", `{"content":`, http.StatusBadRequest},
		{"directory target", "/api/file?path=/" + prefix + "/adir", `{"content":"x"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := putReq(t, h, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
	if len(*calls) != 0 {
		t.Errorf("observer calls = %d, want 0", len(*calls))
	}
}

// TestWriteFile_NilObserverIsFine: a handler without an observer (the
// default; every pre-existing construction) writes exactly as before.
func TestWriteFile_NilObserverIsFine(t *testing.T) {
	h, dir, prefix := testDir(t)
	rec := putReq(t, h, "/api/file?path=/"+prefix+"/f.txt", `{"content":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", rec.Code)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Errorf("content = %q, want %q", got, "x")
	}
}
