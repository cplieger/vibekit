package policyfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// loadWithin runs Load on its own goroutine and fails if it has not returned
// inside budget. A deadline rather than a plain call, because the defect this
// pins HANGS: os.ReadFile over a FIFO blocks in open(2) until a writer appears
// and no context deadline can rescue it, so reverting the OpenRegular adoption
// makes this run to the go-test timeout instead of reporting a failure. The
// permissions file lives under $HOME/.kiro, which the agent's own shell can
// write, so one mkfifo wedged every request on the permissions REST surface.
func loadWithin(t *testing.T, budget time.Duration, path string) (*File, error) {
	t.Helper()
	type res struct {
		f   *File
		err error
	}
	out := make(chan res, 1)
	go func() {
		f, err := Load(path)
		out <- res{f, err}
	}()
	select {
	case r := <-out:
		return r.f, r.err
	case <-time.After(budget):
		t.Fatalf("Load still blocked after %v: the read followed a non-regular file into open(2)", budget)
		return nil, nil
	}
}

func TestLoad_RefusesAFifoInsteadOfBlockingForever(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if _, err := loadWithin(t, 3*time.Second, path); !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("Load over a FIFO = %v, want atomicfile.ErrNotRegular", err)
	}
}

// TestLoad_RefusesASymlink matches Save, which cannot write through one:
// atomicfile's write entry points already refuse a symlink at the target, so a
// policy vibekit would not write is now one it will not read either.
func TestLoad_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("rules:\n  - capability: shell\n    effect: allow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, filename)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	f, err := Load(link)
	if err == nil {
		t.Fatalf("Load followed a symlink; got %d rules", len(f.Rules))
	}
}

// TestLoad_BoundsTheFileBeforeAllocating pins the second defect at the same
// site: os.ReadFile sized its buffer from the file and read all of it, and the
// maxPolicyFileSize check then ran on the RESULT.
func TestLoad_BoundsTheFileBeforeAllocating(t *testing.T) {
	path := filepath.Join(t.TempDir(), filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPolicyFileSize + 1); err != nil {
		_ = f.Close()
		t.Skipf("truncate unsupported here: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Errorf("Load over an oversize policy = %v, want atomicfile.ErrFileTooLarge", err)
	}
}

// TestLoad_MissingFileIsStillTheEmptyCase guards the branch the OpenRegular swap
// moved: the ENOENT arm used to key on os.IsNotExist over os.ReadFile's error and
// now keys on fs.ErrNotExist over the library's, which must still mean "no rules
// yet" rather than a failure the handler surfaces as an unparseable policy.
func TestLoad_MissingFileIsStillTheEmptyCase(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nothing", filename))
	if err != nil {
		t.Fatalf("Load of a missing policy = %v, want nil", err)
	}
	if f == nil || len(f.Rules) != 0 {
		t.Errorf("Load of a missing policy = %+v, want an empty File", f)
	}
}
