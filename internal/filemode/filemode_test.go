package filemode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/atomicfile/v2"
)

// TestEnforceFile_VerifiesTheModeItStored pins that the returned mode is an
// OBSERVATION of the file rather than an echo of the argument: the drift is
// driven by an explicit widening chmod, and the enforcement is what brings it
// back.
//
// The witness assertion is what keeps this honest. If the filesystem under the
// test refuses to store 0666, there is no drift to correct and every assertion
// below would hold vacuously — so the test declares itself INVALID instead of
// passing.
func TestEnforceFile_VerifiesTheModeItStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode().Perm() != 0o666 {
		t.Skipf("filesystem stored %v for a 0o666 chmod; there is no drift here to enforce away", wfi.Mode().Perm())
	}

	stored, err := EnforceFile(path, 0o600)
	if err != nil {
		t.Fatalf("EnforceFile: %v", err)
	}
	if stored != 0o600 {
		t.Errorf("returned mode = %v, want 0600", stored)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("on-disk mode = %v, want 0600", got)
	}
	if got := fi.Mode().Perm(); got != stored.Perm() {
		t.Errorf("returned %v but disk holds %v: the return value is not an observation", stored, got)
	}
}

// TestEnforceFile_RefusesASymlinkAtTheName is the assertion that separates
// this primitive from the os.Chmod it replaced. os.Chmod(path) tightens whatever
// the NAME resolves to, so a symlink planted at the name sends the chmod at a
// file somebody else chose; O_NOFOLLOW makes the kernel refuse the open, so the
// target keeps the mode it had.
func TestEnforceFile_RefusesASymlinkAtTheName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatal(err)
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if tfi.Mode().Perm() != 0o666 {
		t.Skipf("filesystem stored %v for a 0o666 chmod; a redirected chmod would be indistinguishable here", tfi.Mode().Perm())
	}

	link := filepath.Join(dir, "f.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := EnforceFile(link, 0o600); err == nil {
		t.Fatal("EnforceFile through a symlink = nil, want a refusal")
	}
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o666 {
		t.Errorf("symlink target mode = %v, want 0666: the chmod followed the name", got)
	}
}

// TestEnforceDir_VerifiesTheModeTheKernelStored uses the one widening a test
// can create for real: Linux gives a directory created under a setgid parent the
// setgid bit whether or not it was asked for. atomicfile.EnforceMode compares
// setgid deliberately, so this is a genuine request-versus-disk difference.
//
// The witness skips the test as invalid if the kernel stops inheriting the bit,
// rather than letting it pass on a filesystem where there was nothing to catch.
func TestEnforceDir_VerifiesTheModeTheKernelStored(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(parent, "witness")
	if err := os.Mkdir(witness, 0o700); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(witness)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode()&os.ModeSetgid == 0 {
		t.Skipf("kernel did not widen a 0o700 mkdir under a setgid parent (got %v); "+
			"this test cannot distinguish a verified mode from an unverified one here", wfi.Mode())
	}

	dir := filepath.Join(parent, "kid")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored, err := EnforceDir(dir, 0o700)
	if err != nil {
		t.Fatalf("EnforceDir: %v", err)
	}
	if stored != 0o700 {
		t.Errorf("returned mode = %v, want 0700 (setgid must be gone)", stored)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode(); got != os.ModeDir|0o700 {
		t.Errorf("on-disk mode = %v, want %v: the mode the kernel stored was not corrected",
			got, os.ModeDir|0o700)
	}
}

// TestEnforceDir_RefusesANonDirectoryAtTheName pins the O_DIRECTORY half:
// a regular file planted where a directory is expected is refused rather than
// chmod'ed.
func TestEnforceDir_RefusesANonDirectoryAtTheName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := EnforceDir(path, 0o700); err == nil {
		t.Fatal("EnforceDir on a regular file = nil, want a refusal")
	}
}

// TestEnforceFile_LeavesAnAlreadyCorrectModeAlone pins the skip that keeps a
// read-only mount holding an already-0600 file quiet: the mode is read off the
// descriptor, and nothing is asked for when the answer is already right. Without
// it every boot on such a volume warns about an exposure that does not exist.
func TestEnforceFile_LeavesAnAlreadyCorrectModeAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := EnforceFile(path, 0o600)
	if err != nil {
		t.Fatalf("EnforceFile: %v", err)
	}
	if stored != 0o600 {
		t.Errorf("returned mode = %v, want 0600", stored)
	}
}

// TestEnforceFile_ReportsAMissingFileAsNotExist pins that a caller can still
// tell "no file here" from "a file I could not secure" — the mcp store's
// warn-only site and the secret store's refusal both read the error.
func TestEnforceFile_ReportsAMissingFileAsNotExist(t *testing.T) {
	_, err := EnforceFile(filepath.Join(t.TempDir(), "absent.json"), 0o600)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want one matching os.ErrNotExist", err)
	}
}

// TestEnforceMode_SentinelIsTheLibrarysOwn documents the contract the two call
// sites depend on: a mode the filesystem refuses to store arrives as
// atomicfile.ErrModeNotStored, not as a generic error, so a consumer can tell a
// widening filesystem from a permission failure. It cannot be provoked here — no
// filesystem a test can create widens a chmod — so the assertion is that the
// sentinel exists and is distinct from a permission error.
func TestEnforceMode_SentinelIsTheLibrarysOwn(t *testing.T) {
	if atomicfile.ErrModeNotStored == nil {
		t.Fatal("atomicfile.ErrModeNotStored is nil")
	}
	if errors.Is(atomicfile.ErrModeNotStored, os.ErrPermission) {
		t.Error("ErrModeNotStored matches os.ErrPermission; the two failures must stay distinguishable")
	}
}
