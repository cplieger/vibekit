// ONE `git status` invocation per repository, plus its parser. Porcelain v2
// carries the branch, ahead/behind and stash counts as header records beside the
// file list; the panel still spawns `remote get-url origin` separately, because v2
// reports the upstream REF and not the URL. Format reference: gitstatus(1)
// "Porcelain Format Version 2".

package git

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/logsafe"
)

// statusArgs is the one status invocation. `-z` for NUL-delimited records (see
// parsePorcelainV2) and `-uall` so untracked files inside a new directory are
// listed individually rather than collapsing to a directory the walk skips.
var statusArgs = []string{
	"status", "--porcelain=v2", "--branch", "--show-stash", "-z", "-uall",
}

// porcelainStatus is everything one status invocation reports. Branch is empty for
// a detached HEAD. Ahead and Behind are 0 when the branch tracks nothing (git
// omits the header), deliberately indistinguishable from being in sync.
// Conflicted comes from v2's unmerged RECORD TYPE, not from reading XY letters.
type porcelainStatus struct {
	Branch     string
	Files      []gitFile
	Ahead      int
	Behind     int
	Stashes    int
	Conflicted bool
}

// readStatus runs the status invocation in dir and parses it.
//
// The error survives because the callers want different things from a failure: the
// dashboard degrades to empty counts, a discard refuses to act on a status it does
// not have, and pull-all must treat an unreadable tree as unsafe rather than clean.
// A FAILED read still answers the branch, read off .git/HEAD, so a wedged
// repository's row keeps its one piece of orientation.
func readStatus(ctx context.Context, dir string) (porcelainStatus, error) {
	raw, err := gitExec(ctx, dir, statusArgs...).CombinedOutput()
	if err != nil {
		// Cancellation SIGKILLs the subprocess: an expected partial result, so WARN
		// stays for genuine errors and a busy dashboard logs no kill noise.
		if ctx.Err() != nil {
			slog.Debug("git status canceled", "repo", logsafe.Field(dir), "cause", ctx.Err())
			return porcelainStatus{Branch: headBranch(dir)}, err
		}
		slog.Warn("git status failed", "repo", logsafe.Field(dir),
			"error", logsafe.Field(err.Error()), "out", scrubAuth(string(raw)))
		return porcelainStatus{Branch: headBranch(dir)}, err
	}
	return parsePorcelainV2(raw), nil
}

// headRefPrefix is what .git/HEAD holds for a checked-out branch. Anything else
// is a detached HEAD (a bare object id) or not a HEAD document at all.
const headRefPrefix = "ref: refs/heads/"

// headDocMaxBytes bounds the HEAD and .git reads. Both documents are one line, so
// this is purely the hostile-content bound.
const headDocMaxBytes = 4 << 10

// headBranch reads the checked-out branch straight off .git/HEAD, with no
// subprocess. Empty for a detached HEAD, and for anything it cannot read or
// recognise.
func headBranch(dir string) string {
	gitPath := filepath.Join(dir, gitDirName)
	info, err := os.Stat(gitPath) // #nosec G703 -- dir is resolved through repoDir, which refuses ".." and absolute paths
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		// A linked worktree or submodule: .git is a FILE naming the real git
		// directory, and HEAD lives there.
		gitPath = resolveGitDirFile(dir, gitPath)
		if gitPath == "" {
			return ""
		}
	}
	return parseHeadRef(readSmallFile(filepath.Join(gitPath, "HEAD")))
}

// resolveGitDirFile reads a `.git` FILE and returns the git directory it names,
// resolving a relative pointer against the file's own directory (git's rule).
// Empty when the file is not a gitdir pointer.
func resolveGitDirFile(dir, gitFile string) string {
	target, ok := strings.CutPrefix(strings.TrimSpace(readSmallFile(gitFile)), "gitdir:")
	if !ok {
		return ""
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(dir, target)
}

// readSmallFile reads at most headDocMaxBytes of path, answering "" for anything it
// cannot read. Capped rather than os.ReadFile so a file that is not the expected
// one-liner cannot be pulled into memory whole.
func readSmallFile(path string) string {
	f, err := os.Open(path) // #nosec G703 -- see headBranch; the value is only used when it parses as a HEAD document
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, headDocMaxBytes))
	if err != nil {
		return ""
	}
	return string(raw)
}

// parseHeadRef extracts the branch name from a .git/HEAD document, empty for a
// detached HEAD or any other content. The ref-name check is load-bearing: a `.git`
// file can point its `gitdir:` anywhere, so requiring the prefix AND a valid ref
// name keeps an unrelated file's first line off the dashboard.
func parseHeadRef(doc string) string {
	name, ok := strings.CutPrefix(strings.TrimSpace(doc), headRefPrefix)
	if !ok || !isValidGitRef(name) {
		return ""
	}
	return name
}

// Field counts of the three entry records, from gitstatus(1): the path is the last
// field of a SplitN at that count, so it keeps any spaces it contains.
//
//	1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
//	2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>
//	u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
const (
	changedFields  = 9
	renamedFields  = 10
	unmergedFields = 11
)

// parsePorcelainV2 parses the status output; pure, so the grammar is testable
// without git. Records are NUL-separated, headers included: git never quotes a path
// in the -z form, where the newline form C-quotes non-ASCII (café.txt →
// "caf\303\251.txt") and those strings match nothing when fed back to git.
// Malformed records are skipped, which also makes CombinedOutput's stderr harmless.
func parsePorcelainV2(raw []byte) porcelainStatus {
	var st porcelainStatus
	records := strings.Split(string(raw), "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 2 {
			continue
		}
		switch rec[0] {
		case '#':
			st.applyHeader(rec)
		case '1':
			st.appendEntry(rec, changedFields, "")
		case '2':
			// Consume the origin path even when the entry does not parse, or it would
			// be walked as its own entry next iteration.
			orig := ""
			if i+1 < len(records) {
				orig = records[i+1]
				i++
			}
			st.appendEntry(rec, renamedFields, orig)
		case 'u':
			st.Conflicted = true
			st.appendEntry(rec, unmergedFields, "")
		case '?':
			// `? <path>` has no XY pair, so it carries v1's spelling of untracked.
			st.appendPath('?', '?', rec[2:], "")
		case '!':
			// Ignored files: only emitted with --ignored, which statusArgs omits.
		}
	}
	return st
}

// appendHeader applies one `# <key> <value>` header record.
func (st *porcelainStatus) applyHeader(rec string) {
	key, val, found := strings.Cut(strings.TrimPrefix(rec, "# "), " ")
	if !found {
		return
	}
	switch key {
	case "branch.head":
		// git spells a detached HEAD "(detached)"; the wire contract spells it empty.
		if val != "(detached)" {
			st.Branch = val
		}
	case "branch.ab":
		st.Ahead, st.Behind = parseAheadBehind(val)
	case "stash":
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			st.Stashes = n
		}
	}
}

// appendEntry parses one changed, renamed or unmerged record and appends its
// rows. fields is the record's field count; orig is the rename origin, empty for
// every other record.
func (st *porcelainStatus) appendEntry(rec string, fields int, orig string) {
	parts := strings.SplitN(rec, " ", fields)
	if len(parts) < fields {
		return
	}
	xy := parts[1]
	if len(xy) != 2 {
		return
	}
	// v2 writes '.' where v1 writes a space for "unchanged on this side".
	st.appendPath(unchangedToSpace(xy[0]), unchangedToSpace(xy[1]), parts[fields-1], orig)
}

// appendPath appends the rows for one status pair through the shared row builder,
// which owns what a status letter means to the UI.
func (st *porcelainStatus) appendPath(x, y byte, path, orig string) {
	// A trailing "/" is a collapsed untracked tree, which -uall already prevents;
	// this guards a git that does not honour it.
	if path == "" || strings.HasSuffix(path, "/") {
		return
	}
	st.Files = appendStatusEntries(st.Files, x, y, path, orig)
}

// unchangedToSpace maps porcelain v2's '.' onto v1's space, so one row builder
// serves both spellings of "nothing changed on this side".
func unchangedToSpace(c byte) byte {
	if c == '.' {
		return ' '
	}
	return c
}

// parseAheadBehind parses a `# branch.ab` value ("+1 -2"), answering 0, 0 for
// anything that does not parse.
func parseAheadBehind(val string) (ahead, behind int) {
	parts := strings.Fields(val)
	if len(parts) != 2 {
		return 0, 0
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(parts[0], "+")); err == nil && n >= 0 {
		ahead = n
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(parts[1], "-")); err == nil && n >= 0 {
		behind = n
	}
	return ahead, behind
}
