package settings

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// fieldWithin runs Field on its own goroutine and fails if it has not returned
// inside budget.
//
// A deadline rather than a plain call, because the defect this pins HANGS rather
// than fails: os.Open over a FIFO blocks in open(2) until a writer appears, no
// context deadline rescues it, and the load runs inside a singleflight slot — so
// one mkfifo at <configDir>/config.json wedged every concurrent settings reader
// behind it, the agent-ignore filter and the prompt path included. Reverting the
// OpenRegular adoption makes this run to the go-test timeout.
func fieldWithin(t *testing.T, budget time.Duration, dir string) (bool, bool) {
	t.Helper()
	type res struct {
		v  bool
		ok bool
	}
	out := make(chan res, 1)
	go func() {
		v, ok := Field[bool](t.Context(), dir, KeyDebugLogs)
		out <- res{v, ok}
	}()
	select {
	case r := <-out:
		return r.v, r.ok
	case <-time.After(budget):
		t.Fatalf("Field still blocked after %v: the settings read followed a non-regular file into open(2)", budget)
		return false, false
	}
}

func TestField_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, Filename), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if _, ok := fieldWithin(t, 3*time.Second, dir); ok {
		t.Error("Field reported a value from a FIFO planted at config.json")
	}
}

func TestReadBytes_RefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, Filename), 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readBytes(t.Context(), dir)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Errorf("readBytes over a FIFO = %v, want atomicfile.ErrNotRegular", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readBytes still blocked after 3s over a FIFO")
	}
}

// TestReadBytes_RefusesASymlink: a link at config.json made another file's bytes
// the app's settings, which decide the agent read filter and the retention window.
func TestReadBytes_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"debug_logs":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, Filename)); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if v, ok := Field[bool](t.Context(), dir, KeyDebugLogs); ok || v {
		t.Errorf("Field followed a symlink at config.json: (%v, %v)", v, ok)
	}
}
