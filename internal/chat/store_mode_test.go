package chat

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStoreSlog swaps the default logger for a buffer. slog's default is
// process-global, so any test using this must run serially (no t.Parallel).
func captureStoreSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestNewStore_VerifiesTheModeItCreated drives the widening the kernel does on
// its own, with no mocking: on Linux a directory created under a setgid parent
// inherits the bit whether or not it was asked for, so MkdirAll(dir, 0o700)
// stores a mode the caller did not request. atomicfile.EnforceMode compares
// setgid deliberately, so this is a real request-versus-disk difference.
//
// The witness makes the test fail as INVALID rather than pass vacuously if the
// kernel ever stops inheriting the bit. Model: subflux's
// TestEnsureAdminSocketDir_verifiesTheModeItCreated.
//
// Honest limit, worth knowing before trusting this test: the mode ASSERTION here
// does not distinguish EnforceDir from the bare os.Chmod it replaced —
// chmod(2) clears setgid either way, and no filesystem a test can create stores a
// mode other than the one chmod asked for. What the enforcement adds is the
// read-back, and the observable proof of the read-back is the logged mode plus
// the refusals pinned in the two tests below.
func TestNewStore_VerifiesTheModeItCreated(t *testing.T) {
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
			"this test cannot distinguish a verified create from an unverified one here", wfi.Mode())
	}

	dir := filepath.Join(parent, "chats")
	buf := captureStoreSlog(t)
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode(); got != os.ModeDir|0o700 {
		t.Fatalf("created chat dir mode = %v, want %v: the mode the kernel stored was not corrected",
			got, os.ModeDir|0o700)
	}
	// The breadcrumb must report the mode read off the handle. It used to print
	// the dirMode CONSTANT, so it claimed 0700 on exactly the filesystems where
	// that was false. The value carries the chmod-settable bits only (the type
	// bit is not settable and was never in this line), so setgid WOULD show here
	// if it had survived.
	if log := buf.String(); !strings.Contains(log, "mode="+fi.Mode().Perm().String()) {
		t.Errorf("startup line does not report the stored mode %v; log=%q", fi.Mode().Perm(), log)
	}
}

// TestNewStore_EnforcesTheModeOnAHandleNotAPathname pins the difference the mode
// assertion above cannot see. os.Chmod(dir, dirMode) resolves the name at the
// instant of the call, and MkdirAll happily returns nil when the name is a
// symlink to an existing directory — so the old sequence tightened whatever
// directory the link pointed at, anywhere on the host. O_NOFOLLOW|O_DIRECTORY
// makes the kernel refuse instead.
//
// The store still OPENS, deliberately: aborting boot over persistent-volume state
// the container neither created nor owns leaves the operator no way in to repair
// it (vibekit invariant 6). The exposure is reported, not enforced.
func TestNewStore_EnforcesTheModeOnAHandleNotAPathname(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o777); err != nil {
		t.Fatal(err)
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if tfi.Mode().Perm() != 0o777 {
		t.Skipf("filesystem stored %v for a 0o777 chmod; a redirected chmod would be "+
			"indistinguishable from a refused one here", tfi.Mode().Perm())
	}

	dir := filepath.Join(t.TempDir(), "chats")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}

	buf := captureStoreSlog(t)
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore over a symlinked chat dir = %v; a refused mode enforcement must not abort boot", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o777 {
		t.Errorf("symlink target mode = %v, want 0777: the chmod followed the pathname and "+
			"tightened a directory outside the config dir", got)
	}
	log := buf.String()
	if !strings.Contains(log, "could not be made 0700") {
		t.Errorf("refused enforcement logged no warning; the exposure would be silent. log=%q", log)
	}
	// The breadcrumb must not claim a mode on the branch where the open failed:
	// nothing was observed, so "unverified" is the only honest value.
	if !strings.Contains(log, "mode=unverified") {
		t.Errorf("startup line claimed a mode it never read; log=%q", log)
	}
}

// TestNewStore_RefusesANonDirectoryAtTheChatDirName pins the O_DIRECTORY half:
// a regular file planted at the chat-dir name is refused rather than chmod'ed.
// MkdirAll already fails here, so this asserts the pair stays consistent.
func TestNewStore_RefusesANonDirectoryAtTheChatDirName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chats")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("NewStore over a regular file = nil, want an error")
	}
}
