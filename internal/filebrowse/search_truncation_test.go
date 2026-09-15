package filebrowse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/atomicfile/v3"
)

// This file pins ONE rule in both directions: a part of the tree the search meant
// to read and could not marks the answer Truncated, and a part it deliberately
// skipped does not. A file read only to its ceiling is the first kind; a cap on
// the ROWS is neither, because Matched exceeding the row count reports a cut.
//
// "No matches" would otherwise mean two things — the text is not there, or it may
// be in a subtree nobody could open — and one field serves both answers because a
// caller can do exactly one thing with either: say the result is partial.

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
// file was admitted by every gate and never read, so it is not in `scanned` —
// counting it would claim bytes the search never saw — and Truncated is what
// says the answer has a hole where it sat.
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
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1: the walled file was never read, which is why the answer is partial", res.Scanned)
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

// TestReadCandidate_VanishedFileIsCoveredNotLost is the same rule at the read: a
// file that vanishes between the dirent and the open is the ordinary state of a
// tree the agent is writing to, so it is a skip the answer covers. It stays in
// Scanned, because the walk classified it and nothing was refused to it, and it
// does not mark the answer partial.
func TestReadCandidate_VanishedFileIsCoveredNotLost(t *testing.T) {
	h, backing := searchHandlerAt(t, "/workspace")
	writeTree(t, backing, map[string]string{"gone.txt": "the needle was here\n"})
	dir := searchDirAt(t, h, "/workspace")
	cand := searchCandidate{name: "gone.txt", abs: "/workspace/gone.txt"}
	if err := os.Remove(filepath.Join(backing, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	sc := newFileScan(t.Context(), "needle", false, nil, nil)
	got := sc.readCandidate(dir, cand)
	if got.unread {
		t.Error("readCandidate reported a vanished file as unread; a file that is gone was not refused to the search")
	}
	if len(got.hits) != 0 {
		t.Errorf("readCandidate returned %d hits from a file that no longer exists", len(got.hits))
	}
	sc.fold(got)
	if sc.files != 1 {
		t.Errorf("scanned = %d after folding a vanished file, want 1: a covered skip stays scanned", sc.files)
	}
	if sc.truncated {
		t.Error("truncated = true after folding a vanished file: a covered skip is not a hole")
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
			err: atomicfile.ErrFileTooLarge, wantLost: true,
			why: "the ceiling is a read bound, not a skip: a file over it is read to the ceiling and " +
				"reported partial, so nothing may classify the sentinel as covered",
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

// --- The name path against the three bounding sites -----------------------
//
// A name hit opens nothing, so it spends no FILE, DIRECTORY or DEPTH budget —
// and it DOES spend the match budget, through the same `collect` a content hit
// goes through. Those are two different claims about three different mechanisms
// (`capped`, `collect`, and `results`'s post-sort clamp), so they get one case
// each with disjoint scopes: a single case cannot say which mechanism answered.

// TestSearch_NameMatchSpendsNoFileOrDirBudget is the NARROW claim, and its name
// says which budgets it is about so nobody reads it as "the name path never
// truncates". Under the match cap, a tree of matching names is free: the same
// tree searched for a needle nothing holds reports the same `scanned` and the
// same `truncated`.
func TestSearch_NameMatchSpendsNoFileOrDirBudget(t *testing.T) {
	h, dir, prefix := testDir(t)
	for i := range 5 {
		sub := filepath.Join(dir, fmt.Sprintf("needle-dir-%02d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTree(t, sub, map[string]string{
			fmt.Sprintf("needle-file-%02d.txt", i): "nothing matching inside\n",
		})
	}

	hits := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))
	control := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "zzz-absent"}))

	if len(hits.Matches) != 10 {
		t.Fatalf("matches = %d, want 10 (five directories and five files by name)", len(hits.Matches))
	}
	if len(control.Matches) != 0 {
		t.Fatalf("control matches = %d, want none: the fixture holds the needle in no file's bytes", len(control.Matches))
	}
	if hits.Scanned != control.Scanned {
		t.Errorf("scanned = %d with name hits and %d without; a name hit must open nothing",
			hits.Scanned, control.Scanned)
	}
	if hits.Truncated != control.Truncated {
		t.Errorf("truncated = %v with name hits and %v without", hits.Truncated, control.Truncated)
	}
	if hits.Truncated {
		t.Error("truncated = true on a tree well under every cap")
	}
}

// TestSearch_MatchCapStopsTheWalk is the `capped()` site: the match budget is
// read at the TOP of each entry iteration, so enough name hits stop the walk
// mid-tree. `Scanned` BELOW the tree's own file count is what proves the walk
// stopped rather than the tail being cut — the clamp cannot lower it.
func TestSearch_MatchCapStopsTheWalk(t *testing.T) {
	h, dir, prefix := testDir(t)
	const files = maxSearchMatches + 100
	for i := range files {
		name := filepath.Join(dir, fmt.Sprintf("needle-%04d.txt", i))
		if err := os.WriteFile(name, []byte("nothing matching inside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if !res.Truncated {
		t.Error("truncated = false with more matching names than the match budget")
	}
	if res.Scanned >= files {
		t.Errorf("scanned = %d with %d files in the tree; the walk was meant to stop, not to have its tail cut",
			res.Scanned, files)
	}
	if len(res.Matches) != maxSearchMatches {
		t.Errorf("matches = %d, want exactly the cap %d", len(res.Matches), maxSearchMatches)
	}
}

// TestSearch_MatchCapClampsTheAnswer is the `results()` site, and the pin for the
// RANK-AWARE ruling. The fixture collects far more than the cap — the name hits
// stop the walk, and the candidates already accepted are then read, so their
// content rows arrive AFTER the budget is spent — and the clamp cuts the SORTED
// tail, so every surviving row is a name row and no content row survives at all.
//
// Content-bearing files are named to sort BEFORE the name matches, so path order
// alone would keep them: only `nameFirst` leading the comparator can produce
// this answer. Red-checked by reverting it out.
func TestSearch_MatchCapClampsTheAnswer(t *testing.T) {
	h, dir, prefix := testDir(t)
	const (
		contentFiles = 15
		nameFiles    = maxSearchMatches + 10
	)
	body := strings.Repeat("the needle is on this line\n", maxFileMatches)
	for i := range contentFiles {
		name := filepath.Join(dir, fmt.Sprintf("aaa-%02d.txt", i))
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range nameFiles {
		name := filepath.Join(dir, fmt.Sprintf("needle-%04d.txt", i))
		if err := os.WriteFile(name, []byte("nothing matching inside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"path": prefix, "q": "needle"}))

	if len(res.Matches) != maxSearchMatches {
		t.Fatalf("matches = %d, want exactly the cap %d", len(res.Matches), maxSearchMatches)
	}
	if res.Matched <= len(res.Matches) {
		t.Errorf("matched = %d with %d rows, want more: the count is what reports the cut", res.Matched, len(res.Matches))
	}
	if !res.Truncated {
		t.Error("truncated = false after the reply cap stopped the walk")
	}
	for _, m := range res.Matches {
		if m.Kind != MatchKindName {
			t.Errorf("%s: kind = %q line %d survived the clamp; the clamp cuts the SORTED tail, so a content row must not outrank a name row",
				m.Path, m.Kind, m.Line)
			break
		}
	}
}
