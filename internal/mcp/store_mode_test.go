package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_ModeIsVerifiedNotRequested pins that load reads the mode BACK off the
// file rather than assuming the chmod it asked for landed. The drift is driven by
// an explicit widening chmod so the enforcement is what brings it back, and the
// witness below fails the test as INVALID rather than letting it pass vacuously
// on a filesystem that would not store the drift in the first place.
//
// It complements TestLoad_ReenforcesTightPermsOnDrift, which asserts the same
// outcome without the witness: on a filesystem whose umask or ACL refuses 0644,
// that test's seed never drifts and its assertion holds for the wrong reason.
func TestLoad_ModeIsVerifiedNotRequested(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o646); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode().Perm() != 0o646 {
		t.Skipf("filesystem stored %v for a 0o646 chmod; there is no drift here for load to enforce away, "+
			"so this test cannot tell a verified mode from a requested one", wfi.Mode().Perm())
	}

	buf := captureSlog(t)
	if _, err := New(context.Background(), dir, nil, WithKASConfigPath(filepath.Join(dir, "kas-mcp.json"))); err != nil {
		t.Fatalf("New: %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mcp.json mode = %v, want 0600: load did not enforce the mode it reported", got)
	}
	if strings.Contains(buf.String(), "could not be made 0600") {
		t.Errorf("warned about an enforcement that succeeded; log=%q", buf.String())
	}
}

// TestLoad_EnforcesTheModeOnAHandleNotAPathname is the assertion that separates
// the enforcement from the os.Chmod it replaced, and the only one a test on a
// well-behaved filesystem CAN make: chmod-then-stat and a bare chmod store the
// same bits wherever the filesystem honours the request, so the difference is
// which OBJECT is tightened.
//
// os.Chmod(s.path, 0o600) resolves the name at the instant of the call, so a
// symlink planted at mcp.json — by anything that can create a name in the config
// dir — makes vibekit chmod a file it never inspected, anywhere on the host.
// O_NOFOLLOW makes the kernel refuse instead, so the target keeps its mode and
// the operator gets a warning naming the exposure.
//
// The store still LOADS: the posture at this site is warn-and-continue, because
// load's error is fatal to New and therefore to startup, and a /config the
// operator reshaped must still come up to be repairable.
func TestLoad_EnforcesTheModeOnAHandleNotAPathname(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "planted.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"servers":[]}`), 0o600); err != nil {
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
		t.Skipf("filesystem stored %v for a 0o666 chmod; a redirected chmod would be "+
			"indistinguishable from a refused one here", tfi.Mode().Perm())
	}
	if err := os.Symlink(target, filepath.Join(dir, "mcp.json")); err != nil {
		t.Fatal(err)
	}

	buf := captureSlog(t)
	if _, err := New(context.Background(), dir, nil, WithKASConfigPath(filepath.Join(dir, "kas-mcp.json"))); err != nil {
		t.Fatalf("New: %v (a refused mode enforcement must not fail the store at this site)", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o666 {
		t.Errorf("planted symlink target mode = %v, want 0666: the chmod followed the pathname "+
			"and tightened a file outside the config dir", got)
	}
	if !strings.Contains(buf.String(), "could not be made 0600") {
		t.Errorf("refused enforcement logged no warning; the exposure would be silent. log=%q", buf.String())
	}
}
