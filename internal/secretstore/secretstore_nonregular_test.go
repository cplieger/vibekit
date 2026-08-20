package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// newWithin runs New on its own goroutine and fails if it has not returned
// inside budget.
//
// A deadline rather than a plain call, because the defect this pins does not
// fail — it HANGS. This runs on the BOOT path, so reverting the OpenRegular
// adoption in load() makes the container never finish starting rather than
// report an error, and reverting it here makes this test run to the go-test
// timeout. The parked goroutine is abandoned deliberately: it is blocked in
// open(2) and nothing in userspace can reclaim it.
func newWithin(t *testing.T, budget time.Duration, configDir string) (*Store, error) {
	t.Helper()
	type res struct {
		s   *Store
		err error
	}
	out := make(chan res, 1)
	go func() {
		s, err := New(configDir)
		out <- res{s, err}
	}()
	select {
	case r := <-out:
		return r.s, r.err
	case <-time.After(budget):
		t.Fatalf("New still blocked after %v: the credential read followed a non-regular file into open(2)", budget)
		return nil, nil
	}
}

// TestNew_RefusesAFifoInsteadOfBlockingTheBoot pins the ordering fix. load()
// used to os.ReadFile the path and only then ask filemode.EnforceFile whether
// the mode could be verified, so the O_NONBLOCK that EnforceFile's own doc calls
// load-bearing was defeated by the line above it.
func TestNew_RefusesAFifoInsteadOfBlockingTheBoot(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, fileName), fileMode); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	_, err := newWithin(t, 3*time.Second, dir)
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("New over a FIFO = %v, want atomicfile.ErrNotRegular", err)
	}
}

// TestNew_RefusesASymlinkBeforeReadingItsTarget is the other half of the same
// ordering fix: the target's bytes used to be read and parsed as the credential
// store, and only then refused.
func TestNew_RefusesASymlinkBeforeReadingItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"secrets":{"k":"dg=="}}`), fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, fileName)); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	s, err := New(dir)
	// Fatal, not Errorf: New returns a nil Store alongside a non-nil error, so
	// there is nothing left to assert on once this holds and a sibling read would
	// dereference nil.
	if err == nil {
		t.Fatalf("New followed a symlink at %s and loaded %d entries", fileName, len(s.secrets))
	}
}

// TestNew_BoundsTheFileBeforeAllocating pins the second defect at the same site:
// os.ReadFile sized its buffer from the file and read all of it, and the
// maxFileBytes check then ran on the RESULT — so the bound was enforced after an
// arbitrarily large file had already been pulled into memory, on the boot path,
// over a file the agent's own shell can grow. ReadBoundedFile stats first.
func TestNew_BoundsTheFileBeforeAllocating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file one byte over the bound: no disk is consumed, but a
	// read-then-check would still allocate every byte of it.
	if err := f.Truncate(maxFileBytes + 1); err != nil {
		_ = f.Close()
		t.Skipf("truncate unsupported here: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Errorf("New over an oversize store = %v, want atomicfile.ErrFileTooLarge", err)
	}
}
