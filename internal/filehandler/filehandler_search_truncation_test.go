package filehandler

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/atomicfile/v2"
)

// This file pins ONE rule in both directions: a part of the tree the search meant
// to read and could not marks the answer Truncated, and a part it deliberately
// skipped does not.
//
// The rule matters because "no matches" otherwise means two different things —
// the text is not there, or the text may be there in a subtree nobody could open
// — and honest reporting is the entire contract of this endpoint. It is folded
// into Truncated rather than a third response field because a caller can do
// exactly one thing with either answer: say the result is partial.

// requireUnprivileged skips a fixture whose subject is a permission wall when the
// test runs as root, because root opens a 0000 directory and the assertion would
// pass without the property under test ever being exercised. The classifier table
// and the ReadDir case below are the witnesses that hold at any privilege.
func requireUnprivileged(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission bits this fixture is built from; " +
			"TestLogSearchReadError_ClassifiesLossVersusSkip and " +
			"TestWalkDir_ReadDirFailureMarksTruncated cover the same rule unprivileged")
	}
}

// TestSearch_UnreadableDirectoryMarksTruncated is the reported defect: a walk that
// cannot descend into a subdirectory used to warn, continue, and report a complete
// answer, so a hit inside that subtree was indistinguishable from no hit at all.
func TestSearch_UnreadableDirectoryMarksTruncated(t *testing.T) {
	requireUnprivileged(t)
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"open/found.txt":   "the needle is here\n",
		"closed/hidden.md": "the needle is in here too\n",
	})
	closed := filepath.Join(dir, "closed")
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restore before TempDir's own cleanup, which cannot remove a 0000 directory.
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if !res.Truncated {
		t.Error("truncated = false with a subtree the walk could not open; the reply claims to have covered it")
	}
	// The readable half still answers: an unreadable subtree makes the result
	// partial, not empty.
	if got := matchPaths(res); len(got) != 1 || !strings.HasSuffix(got[0], "open/found.txt") {
		t.Errorf("matches = %v, want just open/found.txt", got)
	}
}

// TestSearch_UnreadableFileMarksTruncated is the same rule one level down. The
// file was admitted by every gate and counted in `scanned`, so a reply that both
// counts it and calls itself complete is claiming to have read bytes it never saw.
func TestSearch_UnreadableFileMarksTruncated(t *testing.T) {
	requireUnprivileged(t)
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"readable.txt": "the needle is here\n",
		"walled.txt":   "the needle may be in here\n",
	})
	walled := filepath.Join(dir, "walled.txt")
	if err := os.Chmod(walled, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(walled, 0o600) })

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if !res.Truncated {
		t.Error("truncated = false with an admitted file the walk could not open")
	}
	if got := matchPaths(res); len(got) != 1 || !strings.HasSuffix(got[0], "readable.txt") {
		t.Errorf("matches = %v, want just readable.txt", got)
	}
	if res.Scanned != 2 {
		t.Errorf("scanned = %d, want 2: the walled file was opened and counted, which is why the answer is partial", res.Scanned)
	}
}

// TestSearch_DeliberateSkipsDoNotMarkTruncated is the other direction, and the
// reason the flag is worth anything: it must stay EXACT. A tree full of entries
// the search chose not to read is completely covered, and a result that called
// itself partial whenever a symlink or a binary sat in the tree would train the
// reader to ignore the word.
func TestSearch_DeliberateSkipsDoNotMarkTruncated(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"found.txt":            "the needle is here\n",
		"node_modules/dep.txt": "needle inside an excluded directory\n",
		"blob.bin":             "binary\x00needle\n",
		"link-target.txt":      "needle behind a symlink\n",
	})
	if err := os.Symlink(filepath.Join(dir, "link-target.txt"), filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	// A file that vanishes between the dirent and the open is the ordinary state of
	// a tree the agent is writing to; it is not a loss either. Approximated here by
	// a dangling symlink, which the walk classifies and then refuses.
	if err := os.Symlink(filepath.Join(dir, "gone.txt"), filepath.Join(dir, "dangling.txt")); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{
		"path": prefix, "q": "needle", "exclude": "node_modules",
	}))

	if res.Truncated {
		t.Errorf("truncated = true on a tree whose unread entries were all deliberate skips (matches %v)", matchPaths(res))
	}
	if got := matchPaths(res); len(got) != 2 {
		// found.txt and link-target.txt; the symlink alias is skipped rather than
		// reporting link-target.txt's content twice.
		t.Errorf("matches = %v, want found.txt and link-target.txt", got)
	}
}

// TestLogSearchReadError_ClassifiesLossVersusSkip pins the discriminator itself,
// at any privilege. One switch answers both questions — what to log and whether
// the answer is now partial — so the log level and the truncation flag cannot
// disagree about whether a file was lost.
func TestLogSearchReadError_ClassifiesLossVersusSkip(t *testing.T) {
	tests := map[string]struct {
		err      error
		wantLost bool
		why      string
	}{
		"permission wall": {
			err: syscall.EACCES, wantLost: true,
			why: "the search meant to read it and the kernel refused; nothing else reports that",
		},
		"io error": {
			err: syscall.EIO, wantLost: true,
			why: "the bytes exist and were not read",
		},
		"vanished file": {
			err: fs.ErrNotExist, wantLost: false,
			why: "ordinary on a tree the agent is writing to",
		},
		"not a regular file": {
			err: atomicfile.ErrNotRegular, wantLost: false,
			why: "a FIFO or device node was never in scope",
		},
		"oversize file": {
			err: atomicfile.ErrFileTooLarge, wantLost: false,
			why: "the ceiling is a stated part of what the search covers",
		},
		"symlink swapped in after admission": {
			err: syscall.ELOOP, wantLost: false,
			why: "the refusal IS the confinement guarantee",
		},
		"directory swapped in after admission": {
			err: syscall.ENOTDIR, wantLost: false,
			why: "same swap, other direction",
		},
		"request cancelled": {
			err: context.Canceled, wantLost: false,
			why: "the handler discards the body, so there is nothing to qualify",
		},
		"request deadline exceeded": {
			err: context.DeadlineExceeded, wantLost: false,
			why: "same as cancellation",
		},
		"wrapped permission wall": {
			err: errors.Join(errors.New("openat"), syscall.EACCES), wantLost: true,
			why: "the classification is by errors.Is, so a wrapped errno still counts",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := logSearchReadError("/mount/file", tc.err); got != tc.wantLost {
				t.Errorf("logSearchReadError(%v) lost = %v, want %v (%s)", tc.err, got, tc.wantLost, tc.why)
			}
		})
	}
}

// TestWalkDir_ReadDirFailureMarksTruncated covers the enumeration half at any
// privilege: the chunk in hand is consumed, but the REST of the directory was
// never listed, so entries the search would have matched are unaccounted for.
//
// Driven with a handle on a regular file, which is what the kernel refuses to
// enumerate (ENOTDIR) — the same shape as a directory whose listing fails
// mid-walk, without needing a filesystem that can fail on demand.
func TestWalkDir_ReadDirFailureMarksTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notadir.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// walkDir closes the handle on the way out.

	sc := newFileScan(t.Context(), "needle", false, nil, nil)
	if !sc.walkDir(searchDir{f: f, abs: dir}) {
		t.Error("walkDir returned false (stop the whole scan) on one unenumerable directory; the other roots must still answer")
	}
	if !sc.results().Truncated {
		t.Error("truncated = false after a directory listing failed; the unread remainder is unreported")
	}
}

// TestWalkDir_EndOfDirectoryIsNotTruncation is the negative twin: EOF is how every
// successful enumeration ends, so it must never be read as a loss.
func TestWalkDir_EndOfDirectoryIsNotTruncation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	sc := newFileScan(t.Context(), "needle", false, nil, nil)
	if !sc.walkDir(searchDir{f: f, abs: dir}) {
		t.Fatal("walkDir stopped the scan on an ordinary directory")
	}
	res := sc.results()
	if res.Truncated {
		t.Error("truncated = true after a directory was fully enumerated")
	}
	if len(res.Matches) != 1 {
		t.Errorf("matches = %d, want 1: the fixture's only file holds the needle", len(res.Matches))
	}
}
