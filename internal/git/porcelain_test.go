package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Record builders, so a table case reads as its record type, status pair and
// path rather than as eight filler fields.
//
// The mode and object-id fields are inert to the parser — it reads the record's
// TYPE, its XY pair and the last field — but the COUNT of them is the whole
// grammar, which is why the builders spell them out and why every shape here is
// also exercised against real git output in TestReadStatus_* below. A builder
// with the wrong field count would agree with a parser that had the same bug;
// real git output is what cannot.
const (
	oid  = "0000000000000000000000000000000000000000"
	oid2 = "1111111111111111111111111111111111111111"
)

// changed builds a `1` (ordinary change) record: 9 fields, path last.
func changed(xy, path string) string {
	return "1 " + xy + " N... 100644 100644 100644 " + oid + " " + oid2 + " " + path
}

// renamed builds a `2` (rename or copy) record: 10 fields, path last, with the
// origin path following as its own NUL-separated record.
func renamed(xy, score, path, orig string) string {
	return "2 " + xy + " N... 100644 100644 100644 " + oid + " " + oid2 + " " + score + " " + path + "\x00" + orig
}

// unmerged builds a `u` (conflicted) record: 11 fields, path last.
func unmerged(xy, path string) string {
	return "u " + xy + " N... 100644 100644 100644 100644 " + oid + " " + oid2 + " " + oid + " " + path
}

// nul joins records the way git's -z output does.
func nul(records ...string) []byte {
	return []byte(strings.Join(records, "\x00") + "\x00")
}

func TestParsePorcelainV2_Entries(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []gitFile
	}{
		{"nil input", nil, nil},
		{"empty input", []byte(""), nil},
		{"unstaged modified", nul(changed(".M", "file.go")), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
		{"staged added", nul(changed("A.", "new.go")), []gitFile{
			{Path: "new.go", Status: "A", Display: "Added", Staged: true},
		}},
		{"staged and unstaged emits two entries", nul(changed("MM", "both.go")), []gitFile{
			{Path: "both.go", Status: "M", Display: "Modified", Staged: true},
			{Path: "both.go", Status: "M", Display: "Modified", Staged: false},
		}},
		{"untracked", nul("? newfile.go"), []gitFile{
			{Path: "newfile.go", Status: "?", Display: "Untracked"},
		}},
		// A rename's origin arrives as a second NUL record. Path is the CURRENT
		// path (what stage, discard and diff need); OrigPath carries where it came
		// from, so the panel can say "new.go ← old.go".
		{"rename keeps new path, carries origin", nul(renamed("R.", "R100", "new.go", "old.go")), []gitFile{
			{Path: "new.go", Status: "R", Display: "Renamed", Staged: true, OrigPath: "old.go"},
		}},
		// The worktree half of a staged rename describes an ordinary edit to the
		// file at its NEW path, so it carries no origin: the move is the staged
		// entry's fact, not this one's.
		{"rename plus worktree modify emits two entries", nul(renamed("RM", "R096", "renamed.go", "orig.go")), []gitFile{
			{Path: "renamed.go", Status: "R", Display: "Renamed", Staged: true, OrigPath: "orig.go"},
			{Path: "renamed.go", Status: "M", Display: "Modified", Staged: false},
		}},
		{"copy carries origin", nul(renamed("C.", "C100", "copy.go", "src.go")), []gitFile{
			{Path: "copy.go", Status: "C", Display: "Copied", Staged: true, OrigPath: "src.go"},
		}},
		{"unstaged rename carries origin", nul(renamed(".R", "R100", "new.go", "old.go")), []gitFile{
			{Path: "new.go", Status: "R", Display: "Renamed", OrigPath: "old.go"},
		}},
		// A truncated tail must not read past the record slice: the origin is
		// simply unknown, and the entry still renders.
		{"rename with a missing origin record", []byte(renamed("R.", "R100", "new.go", "")[:len(renamed("R.", "R100", "new.go", ""))-1]), []gitFile{
			{Path: "new.go", Status: "R", Display: "Renamed", Staged: true},
		}},
		// 'T' (typechange) used to fall through statusLabel's default and render
		// as "Unknown". Measured on git 2.x: `rm f && ln -s /tmp f`.
		{"unstaged typechange", nul(changed(".T", "link.txt")), []gitFile{
			{Path: "link.txt", Status: "T", Display: "Typechange"},
		}},
		{"staged typechange", nul(changed("T.", "link.txt")), []gitFile{
			{Path: "link.txt", Status: "T", Display: "Typechange", Staged: true},
		}},
		// -z never quotes: non-ASCII bytes arrive verbatim as UTF-8.
		{"non-ascii filename unquoted", nul(changed(".M", "café.txt")), []gitFile{
			{Path: "café.txt", Status: "M", Display: "Modified"},
		}},
		{"staged non-ascii rename", nul(renamed("R.", "R100", "café-new.txt", "café-old.txt")), []gitFile{
			{Path: "café-new.txt", Status: "R", Display: "Renamed", Staged: true, OrigPath: "café-old.txt"},
		}},
		// A path is the record's LAST field, so spaces inside it survive, and a
		// literal " -> " is not a rename separator in this format at all.
		{"spaced filename with arrow-like substring", nul(changed(".M", "foo -> bar.txt")), []gitFile{
			{Path: "foo -> bar.txt", Status: "M", Display: "Modified"},
		}},
		{"rename of spaced paths", nul(renamed("R.", "R100", "new name.txt", "old name.txt")), []gitFile{
			{Path: "new name.txt", Status: "R", Display: "Renamed", Staged: true, OrigPath: "old name.txt"},
		}},
		// An unmerged path is its own record type, and it still renders as a row.
		{"unmerged both modified", nul(unmerged("UU", "shared.txt")), []gitFile{
			{Path: "shared.txt", Status: "U", Display: "Unmerged", Staged: true},
			{Path: "shared.txt", Status: "U", Display: "Unmerged", Staged: false},
		}},
		{"directory entry skipped", nul("? somedir/", "? real.txt"), []gitFile{
			{Path: "real.txt", Status: "?", Display: "Untracked"},
		}},
		// Short, truncated and unknown records are skipped rather than failing the
		// parse: that is also what makes git's own stderr noise harmless, since
		// CombinedOutput folds it into the same bytes.
		{"malformed records skipped", nul("1 .M", "x", changed(".M", "file.go"), "warning: something"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
		{"multiple entries", nul(
			changed("M.", "staged.go"),
			changed(".M", "unstaged.go"),
			"? untracked.go",
			changed("A.", "added.go"),
		), []gitFile{
			{Path: "staged.go", Status: "M", Display: "Modified", Staged: true},
			{Path: "unstaged.go", Status: "M", Display: "Modified"},
			{Path: "untracked.go", Status: "?", Display: "Untracked"},
			{Path: "added.go", Status: "A", Display: "Added", Staged: true},
		}},
		{"no trailing NUL still parses the final record", []byte(changed(".M", "file.go")), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePorcelainV2(tt.in).Files
			if !slices.Equal(got, tt.want) {
				t.Errorf("parsePorcelainV2(%q).Files = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// The header records are the half that replaced four subprocesses, so each one
// has to be read off the same output the entries come from.
func TestParsePorcelainV2_Headers(t *testing.T) {
	tests := map[string]struct {
		in          []byte
		wantBranch  string
		wantAhead   int
		wantBehind  int
		wantStashes int
	}{
		"branch, divergence and stash together": {
			in: nul("# branch.oid "+oid, "# branch.head main",
				"# branch.upstream origin/main", "# branch.ab +2 -3", "# stash 4"),
			wantBranch: "main", wantAhead: 2, wantBehind: 3, wantStashes: 4,
		},
		// No upstream: git omits both branch.upstream and branch.ab, so the counts
		// stay 0 — the same answer being in sync gives, which is the distinction
		// this shape deliberately does not make.
		"no upstream leaves the counts at zero": {
			in:         nul("# branch.head feature/x"),
			wantBranch: "feature/x",
		},
		// A detached HEAD has no branch name to show, and git spells that
		// "(detached)" rather than omitting the header.
		"detached HEAD reports no branch": {
			in: nul("# branch.oid "+oid, "# branch.head (detached)"),
		},
		"no stash header means no stashes": {
			in:         nul("# branch.head main", "# branch.ab +0 -0"),
			wantBranch: "main",
		},
		// A branch name with a slash still parses: the header value is everything
		// after the first space, and only the KEY is cut on it.
		"malformed header values are ignored": {
			in:         nul("# branch.head main", "# branch.ab garbage", "# stash notanumber"),
			wantBranch: "main",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := parsePorcelainV2(tt.in)
			if got.Branch != tt.wantBranch {
				t.Errorf("Branch = %q, want %q", got.Branch, tt.wantBranch)
			}
			if got.Ahead != tt.wantAhead || got.Behind != tt.wantBehind {
				t.Errorf("ahead/behind = %d/%d, want %d/%d",
					got.Ahead, got.Behind, tt.wantAhead, tt.wantBehind)
			}
			if got.Stashes != tt.wantStashes {
				t.Errorf("Stashes = %d, want %d", got.Stashes, tt.wantStashes)
			}
		})
	}
}

// Conflicted is the record TYPE, not a reading of the letters, which is what
// makes a both-added conflict (`AA`, no U on either side) visible without a table
// of pairs.
func TestParsePorcelainV2_ConflictedIsTheRecordType(t *testing.T) {
	for _, xy := range []string{"UU", "AA", "DD", "AU", "UD", "UA", "DU"} {
		if !parsePorcelainV2(nul(unmerged(xy, "p.txt"))).Conflicted {
			t.Errorf("%s: an unmerged record was not reported as conflicted", xy)
		}
	}
	clean := nul(changed("M.", "a.txt"), changed(".M", "b.txt"),
		renamed("R.", "R100", "new.txt", "old.txt"), "? c.txt")
	if parsePorcelainV2(clean).Conflicted {
		t.Error("ordinary changes were reported as a conflict")
	}
}

// The end-to-end proof, against real git output rather than a builder: everything
// four separate subprocesses used to report has to come out of the one call.
func TestReadStatus_ReadsTheWholeStatusFromOneInvocation(t *testing.T) {
	skipNoGit(t)
	work := behindRepo(t) // HEAD at C1, origin/main at C2 -> behind 1, ahead 0
	// A stash, so the count comes from --show-stash rather than `stash list`.
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("stash me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "stash")
	// A staged add and an untracked file inside a NEW directory: the latter is
	// what -uall is for, and it is invisible without it.
	if err := os.WriteFile(filepath.Join(work, "staged.txt"), []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "staged.txt")
	if err := os.MkdirAll(filepath.Join(work, "fresh"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "fresh", "inside.txt"), []byte("u\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readStatus(t.Context(), work)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	if got.Branch == "" {
		t.Error("Branch is empty for a repo on a branch")
	}
	if got.Ahead != 0 || got.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 0/1", got.Ahead, got.Behind)
	}
	if got.Stashes != 1 {
		t.Errorf("Stashes = %d, want 1", got.Stashes)
	}
	if got.Conflicted {
		t.Error("Conflicted = true for a repo with no conflict")
	}
	byPath := map[string]gitFile{}
	for _, f := range got.Files {
		byPath[f.Path] = f
	}
	if f, ok := byPath["staged.txt"]; !ok || !f.Staged || f.Status != "A" {
		t.Errorf("staged.txt = %+v (present %v), want a staged add", f, ok)
	}
	if f, ok := byPath[filepath.Join("fresh", "inside.txt")]; !ok || f.Status != "?" {
		t.Errorf("fresh/inside.txt = %+v (present %v), want an untracked entry: "+
			"without -uall git collapses it to the directory and the panel loses it", f, ok)
	}

	// A local commit on top leaves the tree both ahead and behind.
	writeCommit(t, work, "local.txt", "local\n", "local commit")
	after, err := readStatus(t.Context(), work)
	if err != nil {
		t.Fatalf("readStatus after a local commit: %v", err)
	}
	if after.Ahead != 1 || after.Behind != 1 {
		t.Errorf("ahead/behind after a local commit = %d/%d, want 1/1", after.Ahead, after.Behind)
	}
}

// Paths git C-quotes in its default output — non-ASCII bytes, embedded spaces,
// and a staged rename to such a name — arrive verbatim.
//
// This is the end-to-end half of the -z decision: under a newline parser these
// came back double-quoted with C-escapes (café.txt → "caf\303\251.txt"), which
// the panel displayed AND fed back to git, where `git add -- '"caf\303\251.txt"'`
// matched nothing and every operation on the file failed.
func TestReadStatus_UnquotesSpecialFilenames(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir) // commits README.md on main

	const nonASCII = "café.txt"
	const spaced = "with space.txt"
	for _, name := range []string{nonASCII, spaced} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A staged rename to a non-ASCII + spaced name (git mv stages it).
	const renamedTo = "rénamed doc.md"
	runGit(t, dir, "mv", "README.md", renamedTo)

	st, err := readStatus(t.Context(), dir)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	byPath := make(map[string]gitFile, len(st.Files))
	for _, f := range st.Files {
		byPath[f.Path] = f
		if strings.HasPrefix(f.Path, `"`) || strings.Contains(f.Path, `\`) {
			t.Errorf("path %q looks C-quoted; -z output must be verbatim", f.Path)
		}
	}
	if f, ok := byPath[nonASCII]; !ok {
		t.Errorf("non-ASCII untracked file %q missing from %+v", nonASCII, st.Files)
	} else if f.Status != "?" {
		t.Errorf("%q status = %q, want untracked", nonASCII, f.Status)
	}
	if _, ok := byPath[spaced]; !ok {
		t.Errorf("spaced untracked file %q missing from %+v", spaced, st.Files)
	}
	// The rename's current path must be present, staged and unquoted whether git
	// reports it as R (rename detected, origin record consumed) or A (add).
	if f, ok := byPath[renamedTo]; !ok {
		t.Errorf("renamed path %q missing from %+v", renamedTo, st.Files)
	} else if !f.Staged {
		t.Errorf("renamed path %q Staged = false, want true", renamedTo)
	} else if f.Status == "R" && f.OrigPath != "README.md" {
		t.Errorf("renamed path %q OrigPath = %q, want README.md", renamedTo, f.OrigPath)
	}
}

// A directory that is not a repository fails rather than reporting a clean tree:
// pull-all's pre-flight and the discard path both treat an unreadable status as
// unsafe, and they can only do that if the error survives.
func TestReadStatus_ReportsAFailureOnANonRepo(t *testing.T) {
	skipNoGit(t)
	if _, err := readStatus(t.Context(), t.TempDir()); err == nil {
		t.Error("readStatus on a non-repository returned no error")
	}
}

// A failed status still answers the BRANCH. Collapsing five invocations into two
// took `branch --show-current` with it, and that spawn could succeed where
// `status` failed — so a wedged repository's dashboard row lost its counts AND its
// branch, where before it lost only the counts.
//
// The failure is induced by corrupting the index, which is a real shape (a wedged
// repository) and leaves .git/HEAD intact. Corruption rather than a mode change:
// root reads a 0000 file, so a permission-based injection would skip here and only
// ever run on the unprivileged CI runner.
func TestReadStatus_AFailedReadStillAnswersTheBranch(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir) // commits README.md on main
	index := filepath.Join(dir, gitDirName, "index")
	if err := os.WriteFile(index, []byte("not an index"), 0o600); err != nil {
		t.Fatalf("Setup: corrupt the index: %v", err)
	}

	got, err := readStatus(t.Context(), dir)
	if err == nil {
		t.Fatal("Setup: the status read succeeded with an unreadable index, so this test " +
			"asserts nothing")
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q after a failed status read, want %q from .git/HEAD",
			got.Branch, "main")
	}
}

// headBranch reads the branch with NO subprocess, in an ordinary clone and in a
// linked worktree, whose .git is a FILE pointing at the real git directory.
func TestHeadBranch(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)

	t.Run("an ordinary clone", func(t *testing.T) {
		if got := headBranch(dir); got != "main" {
			t.Errorf("headBranch = %q, want %q", got, "main")
		}
	})

	t.Run("a linked worktree, whose .git is a pointer file", func(t *testing.T) {
		linked := filepath.Join(t.TempDir(), "wt")
		runGit(t, dir, "worktree", "add", "-b", "side", linked)
		info, err := os.Stat(filepath.Join(linked, gitDirName))
		if err != nil {
			t.Fatalf("Setup: stat the worktree's .git: %v", err)
		}
		if info.IsDir() {
			t.Fatal("Setup: the worktree's .git is a directory, so the pointer arm is " +
				"not exercised")
		}
		if got := headBranch(linked); got != "side" {
			t.Errorf("headBranch of a linked worktree = %q, want %q", got, "side")
		}
	})

	t.Run("a detached HEAD answers nothing, as branch --show-current did", func(t *testing.T) {
		detached := t.TempDir()
		initFixtureRepo(t, detached)
		runGit(t, detached, "checkout", "--detach")
		if got := headBranch(detached); got != "" {
			t.Errorf("headBranch on a detached HEAD = %q, want empty", got)
		}
	})

	t.Run("a directory that is not a repository", func(t *testing.T) {
		if got := headBranch(t.TempDir()); got != "" {
			t.Errorf("headBranch = %q, want empty", got)
		}
	})
}

// The ref-name check is what makes the file read safe on a repository this server
// did not write: a `.git` file may point its `gitdir:` anywhere, so a document that
// is not a HEAD must answer nothing rather than putting an unrelated file's first
// line on the dashboard as a branch name.
func TestParseHeadRef(t *testing.T) {
	tests := map[string]struct {
		doc  string
		want string
	}{
		"a branch":                       {doc: "ref: refs/heads/main\n", want: "main"},
		"a slashed branch":               {doc: "ref: refs/heads/perf/overhaul\n", want: "perf/overhaul"},
		"no trailing newline":            {doc: "ref: refs/heads/main", want: "main"},
		"a detached HEAD":                {doc: "9f2c1a0e5b7d3f8a1c4e6b9d2f5a8c1e4b7d0f3a\n", want: ""},
		"a tag ref is not a branch":      {doc: "ref: refs/tags/v1.0.0\n", want: ""},
		"a remote ref is not a branch":   {doc: "ref: refs/remotes/origin/main\n", want: ""},
		"the prefix with no name":        {doc: "ref: refs/heads/\n", want: ""},
		"an unrelated file's first line": {doc: "root:x:0:0:root:/root:/bin/sh\n", want: ""},
		"an empty document":              {doc: "", want: ""},
		"a ref name git itself refuses":  {doc: "ref: refs/heads/bad^name\n", want: ""},
		"a ref name carrying a space":    {doc: "ref: refs/heads/two words\n", want: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := parseHeadRef(tc.doc); got != tc.want {
				t.Errorf("parseHeadRef(%q) = %q, want %q", tc.doc, got, tc.want)
			}
		})
	}
}

func FuzzParsePorcelainV2(f *testing.F) {
	// Seeds are the table's own records plus the degenerate shapes: a rename whose
	// origin record is missing, a header with no value, and NUL-only input.
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte("\x00\x00"),
		nul(changed(".M", "file.go")),
		nul(changed("MM", "both.go")),
		nul("? newfile.go"),
		nul(renamed("R.", "R100", "new.go", "old.go")),
		nul(renamed("RM", "R096", "renamed.go", "orig.go")),
		nul(unmerged("UU", "shared.txt")),
		nul("! ignored.txt"),
		nul("# branch.head main", "# branch.ab +2 -3", "# stash 4"),
		nul("# branch.head"),
		nul("# branch.ab +x -y"),
		nul("1 .M", "2", "u", "?", "!", "#"),
		[]byte("2 R. N... 100644 100644 100644 " + oid + " " + oid2 + " R100 new.go"),
		nul(changed(".M", "café.txt"), changed(".M", "foo -> bar.txt")),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		st := parsePorcelainV2(data)
		// The invariants a caller depends on, beyond not panicking: every row
		// carries a path and a label, and no row's path is a directory (the panel
		// stages paths, and a directory would be staged wholesale).
		for _, file := range st.Files {
			if file.Path == "" {
				t.Fatalf("row with an empty path from %q: %+v", data, file)
			}
			if strings.HasSuffix(file.Path, "/") {
				t.Fatalf("row with a directory path from %q: %+v", data, file)
			}
			if file.Display == "" {
				t.Fatalf("row with no display label from %q: %+v", data, file)
			}
		}
		if st.Ahead < 0 || st.Behind < 0 || st.Stashes < 0 {
			t.Fatalf("negative counts from %q: %+v", data, st)
		}
	})
}
