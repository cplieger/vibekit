// Package filemode makes a file's mode a fact rather than a request.
//
// A mode ARGUMENT is a request: open(2) and mkdir(2) put it through umask, and a
// filesystem carrying an inheritable group-write ACL stores 0660 for a 0o600 ask
// (measured on a ZFS nfs4acl dataset). Everything here reads the mode off a
// DESCRIPTOR, so what it reports is what the filesystem kept.
//
// This was half of internal/fileutil, a package holding two capabilities bound
// only by a suffix: these two functions and a git-repo predicate that shared
// nothing with them but the word "file". The predicate moved to internal/git,
// where the rest of the git knowledge is.
package filemode

import (
	"os"
	"syscall"

	"github.com/cplieger/atomicfile/v3"
)

// EnforceFile makes the regular file at path carry want, and returns the
// mode the FILESYSTEM stored rather than the one that was asked for.
//
// It replaces os.Chmod at the three 0600/0700 objects vibekit keeps in
// <configDir>, and the difference is the second half of the sequence. A mode
// argument is a REQUEST: open(2) and mkdir(2) put it through umask, and a
// filesystem carrying an inheritable group-write ACL stores 0660 for a 0o600
// request (measured on a ZFS nfs4acl dataset). A caller that stops at chmod has
// issued a second request and still does not know what is on disk, so a wide
// mcp.json or mcp-secrets.json reads as success. atomicfile.EnforceMode fchmods
// and then fstats ONE descriptor, which is the only thing that turns "I asked
// for 0600" into "it is 0600", and it refuses a mismatch with
// atomicfile.ErrModeNotStored.
//
// The handle is also what the pathname cannot give: os.Chmod(path) tightens
// whatever the name resolves to at that instant, so a symlink planted at the
// name, or a rename landing between the stat and the chmod, sends the chmod at
// an object the caller never inspected — and against these three paths that is
// precisely the sequence to attack, because the reward is a chmod on a file
// chosen by somebody else. O_NOFOLLOW makes the KERNEL refuse a final-component
// symlink instead of following it, and fchmod+fstat on one descriptor cannot be
// redirected by any later rename.
//
// O_NONBLOCK carries its own weight and must not be dropped: two of the three
// call sites are on the boot path, and a plain O_RDONLY open of a FIFO left at
// the name blocks in open(2) until a writer appears — which would turn a widened
// mode into a container that never finishes starting. os.Chmod never had that
// exposure, so the flag is what keeps this a strict improvement.
//
// The mode is read from the descriptor BEFORE anything is asked for, and an
// object already carrying want is left alone. That keeps the one deployment the
// old code was quiet on quiet: a read-only bind mount holding an already-0600
// file rejects every chmod, and warning about it on every boot would train the
// operator to ignore the line that matters.
func EnforceFile(path string, want os.FileMode) (os.FileMode, error) {
	return enforceMode(path, want, 0)
}

// EnforceDir is EnforceFile for a directory. O_DIRECTORY makes the
// kernel refuse anything that is not a directory at the name, so a regular file
// or a FIFO planted there is rejected rather than chmod'ed; a directory is an
// *os.File like any other, and atomicfile.EnforceMode compares only the bits
// chmod(2) can set, so the type bits never read as a mismatch.
//
// The comparison DOES include setgid, deliberately: Linux gives a directory
// created under a setgid parent the bit whether or not it was asked for, which
// is a real difference between the request and the disk.
func EnforceDir(path string, want os.FileMode) (os.FileMode, error) {
	return enforceMode(path, want, syscall.O_DIRECTORY)
}

// enforceMode holds the shared sequence: open without following, read the mode
// off the descriptor, and ask only when the answer is wrong.
func enforceMode(path string, want os.FileMode, extraFlags int) (os.FileMode, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|extraFlags, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if stored := chmodBits(fi.Mode()); stored == chmodBits(want) {
		return stored, nil
	}
	return atomicfile.EnforceMode(f, want)
}

// chmodBits reduces a mode to the bits chmod(2) can set, mirroring the
// comparison atomicfile.EnforceMode makes internally so the skip above and the
// library's verdict cannot disagree about what "already correct" means.
func chmodBits(m os.FileMode) os.FileMode {
	return m.Perm() | m&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}
