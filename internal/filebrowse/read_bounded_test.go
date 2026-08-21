package filebrowse

import (
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadFile_Fifo_DoesNotHang covers a capability the hand-rolled read did not have.
// open(2) on a FIFO with no writer blocks indefinitely, so a named pipe placed under a
// granted browse root could wedge the handler goroutine forever. atomicfile's confined
// read opens non-blocking and refuses anything that is not a regular file, so this is a
// prompt 400 instead.
func TestReadFile_Fifo_DoesNotHang(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.txt"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}

	done := make(chan int, 1)
	go func() { done <- getReq(t, h, "/api/file?path="+prefix+"/pipe.txt").Code }()

	select {
	case code := <-done:
		if code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for a non-regular file", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readFile blocked on a FIFO; the confined read must open non-blocking")
	}
}
